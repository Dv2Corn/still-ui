package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/egressfilter"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type fakeEgressHost struct {
	spec                   egressfilter.Spec
	applyErr               error
	applied, ensured, gone int
}

func (f *fakeEgressHost) Apply(s egressfilter.Spec) error {
	f.applied++
	f.spec = s
	return f.applyErr
}

func (f *fakeEgressHost) Ensure(s egressfilter.Spec) error {
	f.ensured++
	f.spec = s
	return f.applyErr
}
func (f *fakeEgressHost) Remove() error { f.gone++; return nil }
func (f *fakeEgressHost) Status() egressfilter.Status {
	return egressfilter.Status{Supported: true, NftInstalled: true, Active: f.applied > f.gone}
}

// useFakeEgress swaps the host, this server's addresses, sshd_config and the node
// transport for test doubles, and forgets node sync state between tests.
func useFakeEgress(t *testing.T) *fakeEgressHost {
	t.Helper()
	fake := &fakeEgressHost{}
	prevHost, prevAddrs, prevSshd, prevPush := egressFilterHost, egressLocalAddrs, sshdConfigPath, egressFilterPush
	egressFilterHost = fake
	egressLocalAddrs = func() []string { return []string{"192.0.2.1", "2001:db8::1"} }
	dir := t.TempDir()
	sshdConfigPath = filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(sshdConfigPath, []byte("# test\nPort 2222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	egressFilterPush = func(context.Context, *model.Node, ManagedEgressFilterRequest) error {
		return errors.New("no node transport in this test")
	}
	resetEgressNodeSync()
	t.Cleanup(func() {
		egressFilterHost, egressLocalAddrs, sshdConfigPath, egressFilterPush = prevHost, prevAddrs, prevSshd, prevPush
		egressFilterErr = ""
		resetEgressNodeSync()
	})
	return fake
}

func resetEgressNodeSync() {
	egressNodeSync.Lock()
	egressNodeSync.m = map[int]*egressNodeSyncState{}
	egressNodeSync.Unlock()
}

func routingRules(t *testing.T, cfg *xray.Config) []map[string]any {
	t.Helper()
	var routing struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatalf("routing %s: %v", cfg.RouterConfig, err)
	}
	return routing.Rules
}

func outboundsOf(t *testing.T, cfg *xray.Config) []map[string]any {
	t.Helper()
	var outbounds []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatalf("outbounds %s: %v", cfg.OutboundConfigs, err)
	}
	return outbounds
}

func egressRuleTags(rules []map[string]any) []string {
	var tags []string
	for _, r := range rules {
		if tag, _ := r["ruleTag"].(string); strings.HasPrefix(tag, "egress-filter-") {
			tags = append(tags, tag)
		}
	}
	return tags
}

func buildFreedom(t *testing.T, outbound map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(outbound["settings"])
	fc := new(conf.FreedomConfig)
	if err := json.Unmarshal(raw, fc); err != nil {
		t.Fatalf("freedom settings %s: %v", raw, err)
	}
	if _, err := fc.Build(); err != nil {
		t.Fatalf("xray-core refuses the freedom settings %s: %v", raw, err)
	}
}

// Every switch reaches the generated config as a blackhole rule ahead of all others,
// limited to client inbounds; the stock template's geoip:private rule is not doubled.
func TestGetXrayConfig_EgressFilterRules(t *testing.T) {
	setupConflictDB(t)
	useFakeEgress(t)
	addTorrentBlockInbound(t, "in-client", 30101, `{"enabled":false}`)
	svc := &EgressFilterService{}
	if _, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{
		SMTP: true, MailSubmission: true, Private: true, Server: true,
		Custom: []string{"203.0.113.0/24, 2001:db8:5::/48", "6667", "1000-2000"},
	}}); err != nil {
		t.Fatalf("SetLocal: %v", err)
	}

	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	rules := routingRules(t, cfg)
	want := []string{"egress-filter-mail", "egress-filter-loopback", "egress-filter-server", "egress-filter-custom-ip", "egress-filter-custom-port"}
	if got := egressRuleTags(rules); !reflect.DeepEqual(got, want) {
		t.Fatalf("egress rules = %v, want %v (no private rule: the template blocks geoip:private)", got, want)
	}
	for i, r := range rules[:len(want)] {
		if r["outboundTag"] != "blocked" {
			t.Errorf("rule %d targets %v, want the template's blackhole", i, r["outboundTag"])
		}
		if tags := stringList(r["inboundTag"]); !reflect.DeepEqual(tags, []string{"in-client"}) {
			t.Errorf("rule %v applies to %v, want only the client inbound", r["ruleTag"], tags)
		}
	}
	if rules[0]["port"] != "25,465,587" || rules[0]["network"] != "tcp" {
		t.Errorf("mail rule = %v", rules[0])
	}
	if got := rules[2]; got["port"] != "2053,2222" || !reflect.DeepEqual(stringList(got["ip"]), []string{"192.0.2.1", "2001:db8::1"}) {
		t.Errorf("server rule = %v, want panel and SSH ports on this server's addresses", got)
	}
	if got := stringList(rules[3]["ip"]); !reflect.DeepEqual(got, []string{"203.0.113.0/24", "2001:db8:5::/48"}) {
		t.Errorf("custom-ip rule = %v", got)
	}
	if rules[4]["port"] != "1000-2000,6667" {
		t.Errorf("custom-port rule = %v", rules[4])
	}
	// Only the filter's own rules: the template's geoip rules need geo data files.
	own := make([]any, 0, len(want))
	for _, r := range rules[:len(want)] {
		own = append(own, r)
	}
	assertXrayAccepts(t, "the egress filter rules", buildGoldenRouting(map[string]any{"rules": own}))

	// A client naming this server by domain is only resolved in freedom: the server
	// ports are also blocked there, ahead of the template's own final rules.
	for _, o := range outboundsOf(t, cfg) {
		if o["protocol"] != "freedom" {
			continue
		}
		final, _ := o["settings"].(map[string]any)["finalRules"].([]any)
		first, _ := final[0].(map[string]any)
		if first["action"] != "block" || first["port"] != "2053,2222" || len(final) < 3 {
			t.Fatalf("freedom %v finalRules = %v, want the server block first and the template's kept", o["tag"], final)
		}
		buildFreedom(t, map[string]any{"settings": map[string]any{"finalRules": []any{first}}})
	}
	if st := svc.Status(); !st.TemplatePrivateBlocked || !reflect.DeepEqual(st.ServerPorts, []int{2053, 2222}) {
		t.Fatalf("status = %+v, want template private block seen and ports 2053, 2222", st)
	}
}

// With nothing ticked the generated config is left exactly as it was.
func TestGetXrayConfig_EgressFilterOffChangesNothing(t *testing.T) {
	setupConflictDB(t)
	useFakeEgress(t)
	addTorrentBlockInbound(t, "in-client", 30102, `{"enabled":false}`)
	if _, err := (&EgressFilterService{}).SetLocal(EgressFilterSettings{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := egressRuleTags(routingRules(t, cfg)); len(got) != 0 {
		t.Fatalf("filter off but rules %v were added", got)
	}
	for _, o := range outboundsOf(t, cfg) {
		if o["tag"] == egressFilterOutboundTag {
			t.Fatal("filter off but a blackhole was added")
		}
	}
}

func TestInjectEgressFilter_PrivateRuleOnlyWhenNotAlreadyBlocked(t *testing.T) {
	cases := []struct {
		name    string
		routing string
		want    bool
	}{
		{"no rules", `{"rules":[]}`, true},
		{"template rule", `{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"},{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}]}`, false},
		{"after another blackhole rule", `{"rules":[{"type":"field","domain":["geosite:category-ads-all"],"outboundTag":"blocked"},{"type":"field","ip":["GEOIP:private"],"outboundTag":"blocked"}]}`, false},
		{"after a rule that routes elsewhere", `{"rules":[{"type":"field","domain":["example.com"],"outboundTag":"direct"},{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}]}`, true},
		{"only for some inbounds", `{"rules":[{"type":"field","inboundTag":["in-a"],"ip":["geoip:private"],"outboundTag":"blocked"}]}`, true},
		{"to a non-blackhole", `{"rules":[{"type":"field","ip":["geoip:private"],"outboundTag":"direct"}]}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &xray.Config{
				InboundConfigs:  []xray.InboundConfig{{Tag: "api"}, {Tag: "in-a"}},
				RouterConfig:    json_util.RawMessage(c.routing),
				OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}]`),
			}
			injectEgressFilter(cfg, []string{"in-a"}, egressfilter.Filter{Private: true}, egressOwnTargets{})
			got := slices.Contains(egressRuleTags(routingRules(t, cfg)), "egress-filter-private")
			if got != c.want {
				t.Fatalf("private rule added = %v, want %v; rules %s", got, c.want, cfg.RouterConfig)
			}
		})
	}
}

// A template without a blackhole gets one under a free tag; a freedom outbound that
// dials through another one gets no final rule, which Xray would ignore anyway.
func TestInjectEgressFilter_BlackholeAndDialerProxy(t *testing.T) {
	cfg := &xray.Config{
		InboundConfigs: []xray.InboundConfig{{Tag: "in-a"}},
		RouterConfig:   json_util.RawMessage(`{"rules":[{"type":"field","network":"tcp,udp","outboundTag":"direct"}]}`),
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"},` +
			`{"tag":"chained","protocol":"freedom","streamSettings":{"sockopt":{"dialerProxy":"direct"}}},` +
			`{"tag":"egress-block","protocol":"socks"}]`),
	}
	injectEgressFilter(cfg, []string{"in-a"}, egressfilter.Filter{Server: true}, egressOwnTargets{Addrs: []string{"192.0.2.1"}, Ports: []uint16{2053}})
	outbounds := outboundsOf(t, cfg)
	last := outbounds[len(outbounds)-1]
	if last["tag"] != "egress-block-2" || last["protocol"] != "blackhole" {
		t.Fatalf("outbounds = %v, want a new blackhole under a free tag", outbounds)
	}
	if _, ok := outbounds[0]["settings"].(map[string]any)["finalRules"]; !ok {
		t.Fatal("the direct freedom got no server block")
	}
	if s, _ := outbounds[1]["settings"].(map[string]any); s != nil && s["finalRules"] != nil {
		t.Fatalf("the dialerProxy freedom got final rules: %v", s)
	}
	rules := routingRules(t, cfg)
	if rules[0]["outboundTag"] != "egress-block-2" || rules[len(rules)-1]["outboundTag"] != "direct" {
		t.Fatalf("rules = %v, want the filter first and the template's rule kept last", rules)
	}
}

func TestEgressFilterService_SetLocalValidates(t *testing.T) {
	setupConflictDB(t)
	fake := useFakeEgress(t)
	svc := &EgressFilterService{}
	for _, bad := range []EgressFilterSettings{
		{Filter: egressfilter.Filter{Custom: []string{"53"}}},
		{Filter: egressfilter.Filter{Custom: []string{"1-100"}}},
		{Filter: egressfilter.Filter{Custom: []string{"0.0.0.0/0"}}},
		{Filter: egressfilter.Filter{Custom: []string{"not-an-address"}}},
		{NodeScope: "some"},
	} {
		if _, err := svc.SetLocal(bad); err == nil {
			t.Errorf("SetLocal(%+v) accepted", bad)
		}
	}
	if fake.applied+fake.gone != 0 {
		t.Fatal("a refused setting reached the host")
	}

	st, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{SMTP: true, Custom: []string{"10.1.2.3/8", "10.0.0.1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Settings.Custom; !reflect.DeepEqual(got, []string{"10.0.0.0/8"}) {
		t.Fatalf("custom = %v, want one canonical prefix", got)
	}
	if fake.applied != 1 || !fake.spec.Filter.SMTP {
		t.Fatalf("host applied %d times with %+v", fake.applied, fake.spec)
	}

	// The server ports (2053, 2222) reach the table with the server switch.
	if _, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{Server: true}}); err != nil {
		t.Fatal(err)
	}
	if fake.applied != 2 || !reflect.DeepEqual(fake.spec.ServerPorts, []uint16{2053, 2222}) {
		t.Fatalf("server-only filter: applied %d with %+v", fake.applied, fake.spec)
	}
	// Nothing left to block: the table is removed, not loaded.
	if _, err := svc.SetLocal(EgressFilterSettings{}); err != nil {
		t.Fatal(err)
	}
	if fake.gone != 1 {
		t.Fatalf("an empty filter removed the table %d times, want 1", fake.gone)
	}
	ensured := fake.ensured
	svc.EnsureHostFilter()
	if fake.ensured != ensured {
		t.Fatal("EnsureHostFilter ran nft with nothing to block")
	}
}

// A port that also serves an inbound is public on purpose; the subscription port
// stays open because apps refresh through the tunnel.
func TestEgressFilterService_ServerPorts(t *testing.T) {
	setupConflictDB(t)
	useFakeEgress(t)
	if err := os.WriteFile(sshdConfigPath, []byte("Port 22\nport 2222\n#Port 23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(sshdConfigPath), "sshd_config.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(sshdConfigPath), "sshd_config.d", "10-port.conf"), []byte("Port 2200\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addTorrentBlockInbound(t, "in-on-ssh-port", 2222, `{"enabled":false}`)
	got := (&EgressFilterService{}).ServerPorts()
	if want := []uint16{22, 2053, 2200}; !reflect.DeepEqual(got, want) {
		t.Fatalf("server ports = %v, want %v (no subscription port 2096, no inbound port 2222)", got, want)
	}
}

func TestEgressFilterService_ApplyManaged(t *testing.T) {
	setupConflictDB(t)
	fake := useFakeEgress(t)
	svc := &EgressFilterService{}
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Filter: &egressfilter.Filter{SMTP: true}}); err == nil {
		t.Fatal("a request without managedBy was accepted")
	}
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{ManagedBy: "central"}); err == nil {
		t.Fatal("a request without a filter was accepted")
	}
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Filter: &egressfilter.Filter{Custom: []string{"53"}}, ManagedBy: "central"}); err == nil {
		t.Fatal("an invalid custom list was accepted")
	}

	want := egressfilter.Filter{SMTP: true, Private: true}
	changed, err := svc.ApplyManaged(ManagedEgressFilterRequest{Filter: &want, ManagedBy: "central"})
	if err != nil || !changed {
		t.Fatalf("ApplyManaged = %v, %v; want a change", changed, err)
	}
	got := svc.Settings()
	if got.ManagedBy != "central" || !got.Private || fake.applied != 1 {
		t.Fatalf("settings = %+v, host applied %d", got, fake.applied)
	}
	if changed, _ := svc.ApplyManaged(ManagedEgressFilterRequest{Filter: &want, ManagedBy: "central"}); changed || fake.applied != 1 {
		t.Fatal("the same filter again was applied again")
	}

	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Release: true, ManagedBy: "someone-else"}); err != nil || svc.Settings().ManagedBy != "central" {
		t.Fatal("another panel released this node's filter")
	}
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Release: true, ManagedBy: "central"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.Settings(); got.ManagedBy != "" || !got.Private || !got.SMTP {
		t.Fatalf("after release = %+v, want the same filter kept as the node's own", got)
	}

	// The operator's own save on the node takes the filter over.
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Filter: &want, ManagedBy: "central"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{SMTP: true}}); err != nil {
		t.Fatal(err)
	}
	if got := svc.Settings(); got.ManagedBy != "" || got.Private {
		t.Fatalf("after a local save = %+v", got)
	}
}

type pushRecord struct {
	node int
	req  ManagedEgressFilterRequest
}

func recordPushes(t *testing.T, fail map[int]error) func() []pushRecord {
	t.Helper()
	var mu sync.Mutex
	var got []pushRecord
	egressFilterPush = func(_ context.Context, n *model.Node, req ManagedEgressFilterRequest) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, pushRecord{n.Id, req})
		return fail[n.Id]
	}
	return func() []pushRecord {
		mu.Lock()
		defer mu.Unlock()
		out := got
		got = nil
		slices.SortFunc(out, func(a, b pushRecord) int { return a.node - b.node })
		return out
	}
}

func addEgressNode(t *testing.T, name, status string) int {
	t.Helper()
	n := &model.Node{Name: name, Address: "203.0.113.9", Port: 2053, ApiToken: "x", Enable: true}
	if err := database.GetDB().Create(n).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(n).Update("status", status).Error; err != nil {
		t.Fatal(err)
	}
	return n.Id
}

func TestEgressFilterService_SyncNodes(t *testing.T) {
	setupConflictDB(t)
	useFakeEgress(t)
	svc := &EgressFilterService{}
	a := addEgressNode(t, "a", "online")
	b := addEgressNode(t, "b", "online")
	off := addEgressNode(t, "off", "offline")
	pushes := recordPushes(t, nil)

	// A panel that never handed out its filter leaves every node alone.
	if _, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{SMTP: true}}); err != nil {
		t.Fatal(err)
	}
	if got := pushes(); len(got) != 0 {
		t.Fatalf("scope none contacted nodes: %+v", got)
	}

	if _, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{SMTP: true}, NodeScope: EgressNodeScopeSelected, NodeIds: []int{a}}); err != nil {
		t.Fatal(err)
	}
	got := pushes()
	if len(got) != 2 || got[0].node != a || got[0].req.Filter == nil || !got[0].req.Filter.SMTP ||
		got[1].node != b || !got[1].req.Release || got[0].req.ManagedBy == "" || got[0].req.ServerAddress != "203.0.113.9" {
		t.Fatalf("pushes = %+v, want the filter to a and a release to b, none to the offline node", got)
	}
	svc.SyncNodes(false)
	if again := pushes(); len(again) != 0 {
		t.Fatalf("synced nodes were contacted again: %+v", again)
	}

	// Nodes too old for the endpoint: a release counts as done, an apply is reported.
	recordPushes(t, map[int]error{a: errors.New("HTTP 404"), b: errors.New("HTTP 404")})
	if _, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{SMTP: true, Private: true}, NodeScope: EgressNodeScopeSelected, NodeIds: []int{a}}); err != nil {
		t.Fatal(err)
	}
	states := map[int]EgressFilterNodeState{}
	for _, s := range svc.Status().Nodes {
		states[s.Id] = s
	}
	if s := states[a]; s.Synced || !strings.Contains(s.Error, "too old") {
		t.Fatalf("node a = %+v, want an update hint", s)
	}
	if s := states[off]; s.Synced || s.Error != "" {
		t.Fatalf("offline node = %+v, want it simply waiting", s)
	}

	// A refused node waits for the retry delay unless forced.
	pushes = recordPushes(t, nil)
	svc.SyncNodes(false)
	if got := pushes(); len(got) != 0 {
		t.Fatalf("a refused node was retried before its delay: %+v", got)
	}
	svc.SyncNodes(true)
	if got := pushes(); len(got) != 1 || got[0].node != a {
		t.Fatalf("forced retry = %+v, want node a", got)
	}
}

// A domain resolving to a blocked address is caught only by freedom's final rules, which see the
// resolved address; they come ahead of the template's allow.
func TestInjectEgressFilter_FreedomBlocksResolvedAddresses(t *testing.T) {
	freedomFinal := func(t *testing.T, template string, f egressfilter.Filter, own egressOwnTargets) []any {
		t.Helper()
		cfg := &xray.Config{
			InboundConfigs:  []xray.InboundConfig{{Tag: "in-a"}},
			RouterConfig:    json_util.RawMessage(`{"rules":[]}`),
			OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom","settings":{"finalRules":` + template + `}},{"tag":"blocked","protocol":"blackhole"}]`),
		}
		injectEgressFilter(cfg, []string{"in-a"}, f, own)
		out := outboundsOf(t, cfg)[0]
		final, _ := out["settings"].(map[string]any)["finalRules"].([]any)
		// Xray must accept the rules; geoip ones need geo data files the test lacks.
		var buildable []any
		for _, r := range final {
			if !slices.ContainsFunc(stringList(r.(map[string]any)["ip"]), func(ip string) bool { return strings.HasPrefix(ip, "geoip:") }) {
				buildable = append(buildable, r)
			}
		}
		buildFreedom(t, map[string]any{"settings": map[string]any{"finalRules": buildable}})
		return final
	}
	ipsOf := func(rule any) []string { return stringList(rule.(map[string]any)["ip"]) }

	t.Run("allow-only template", func(t *testing.T) {
		final := freedomFinal(t, `[{"action":"allow"}]`,
			egressfilter.Filter{Private: true, Server: true, Custom: []string{"203.0.113.0/24", "2001:db8:5::/48", "6667"}},
			egressOwnTargets{Addrs: []string{"192.0.2.1"}, Ports: []uint16{2053}})
		if len(final) != 3 {
			t.Fatalf("finalRules = %v, want the server block, the address block and the template's allow", final)
		}
		block := final[1].(map[string]any)
		if block["action"] != "block" || block["port"] != "1-52,54-65535" || block["network"] != nil {
			t.Fatalf("address block = %v, want every network and every port but DNS", block)
		}
		ips := ipsOf(block)
		for _, want := range []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "fc00::/7", "fe80::/10", "::1/128", "203.0.113.0/24", "2001:db8:5::/48"} {
			if !slices.Contains(ips, want) {
				t.Errorf("address block lacks %s: %v", want, ips)
			}
		}
		if final[2].(map[string]any)["action"] != "allow" {
			t.Fatalf("the template's allow must stay last: %v", final)
		}
	})

	t.Run("template already blocks geoip:private", func(t *testing.T) {
		final := freedomFinal(t, `[{"action":"block","ip":["geoip:private"]},{"action":"allow"}]`,
			egressfilter.Filter{Private: true, Custom: []string{"203.0.113.0/24"}}, egressOwnTargets{})
		if len(final) != 3 {
			t.Fatalf("finalRules = %v", final)
		}
		if ips := ipsOf(final[0]); !reflect.DeepEqual(ips, []string{"203.0.113.0/24"}) {
			t.Fatalf("address block = %v, want only the listed address: the template blocks private already", ips)
		}
	})

	t.Run("private only", func(t *testing.T) {
		final := freedomFinal(t, `[{"action":"allow"}]`, egressfilter.Filter{Private: true}, egressOwnTargets{})
		if len(final) != 2 || !slices.Contains(ipsOf(final[0]), "192.168.0.0/16") {
			t.Fatalf("finalRules = %v, want the private block ahead of the allow", final)
		}
	})

	t.Run("mail only changes no final rule", func(t *testing.T) {
		final := freedomFinal(t, `[{"action":"allow"}]`, egressfilter.Filter{SMTP: true}, egressOwnTargets{})
		if len(final) != 1 {
			t.Fatalf("finalRules = %v, want the template's own", final)
		}
	})
}

// failOnDNS fails the test if anything resolves a name through the Go or system resolver.
func failOnDNS(t *testing.T) {
	t.Helper()
	prev := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
		t.Errorf("a DNS lookup went to %s while the egress filter was rendered", addr)
		return nil, errors.New("no DNS in this test")
	}}
	t.Cleanup(func() { net.DefaultResolver = prev })
}

// Behind NAT the server's public address is on no interface: the server block covers IP
// literals the panel is configured with, never what a name resolves to (a CDN, a shared host).
func TestEgressFilterService_PublicAddressesAreBlocked(t *testing.T) {
	setupConflictDB(t)
	fake := useFakeEgress(t)
	failOnDNS(t)
	addTorrentBlockInbound(t, "in-client", 30101, `{"enabled":false}`)
	svc := &EgressFilterService{}
	settings := &SettingService{}
	// A name behind a CDN, plus subscription hosts on another server: none of them are blocked.
	for k, v := range map[string]string{"webDomain": "panel.example.com", "subDomain": "sub.example.com", "subURI": "https://203.0.113.50:2096/sub/"} {
		if err := settings.setString(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{Server: true}}); err != nil {
		t.Fatal(err)
	}
	if got := svc.PublicAddrs(); len(got) != 0 || len(fake.spec.ServerAddrs) != 0 {
		t.Fatalf("names and subscription hosts gave %v (nft %v), want nothing", got, fake.spec.ServerAddrs)
	}
	for k, v := range map[string]string{"webListen": "10.0.0.5", "webDomain": "[2001:db8::7]"} {
		if err := settings.setString(k, v); err != nil {
			t.Fatal(err)
		}
	}
	// On a node, the owning master's address for it (a floating IP, say).
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Filter: &egressfilter.Filter{Server: true}, ManagedBy: "central", ServerAddress: "198.51.100.8:2053"}); err != nil {
		t.Fatal(err)
	}
	public := []string{"10.0.0.5", "198.51.100.8", "2001:db8::7"}
	if got := svc.PublicAddrs(); !reflect.DeepEqual(got, public) {
		t.Fatalf("PublicAddrs = %v, want %v", got, public)
	}
	if !reflect.DeepEqual(fake.spec.ServerAddrs, public) {
		t.Fatalf("nft spec addresses = %v, want %v", fake.spec.ServerAddrs, public)
	}
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	var server map[string]any
	for _, r := range routingRules(t, cfg) {
		if r["ruleTag"] == "egress-filter-server" {
			server = r
		}
	}
	want := []string{"10.0.0.5", "192.0.2.1", "198.51.100.8", "2001:db8::1", "2001:db8::7"}
	if server == nil || !reflect.DeepEqual(stringList(server["ip"]), want) {
		t.Fatalf("server rule = %v, want the interface and public addresses %v", server, want)
	}
	// A master that does not own the filter cannot move the address.
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Release: true, ManagedBy: "other", ServerAddress: "198.51.100.66"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.Settings(); got.MasterAddress != "198.51.100.8" || got.ManagedBy != "central" {
		t.Fatalf("a non-owner's release changed the node: %+v", got)
	}
	// A master address that is a name is not resolved and not kept.
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Filter: &egressfilter.Filter{Server: true}, ManagedBy: "central", ServerAddress: "node.example.com:2053"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.PublicAddrs(); !reflect.DeepEqual(got, []string{"10.0.0.5", "2001:db8::7"}) {
		t.Fatalf("PublicAddrs with a named master address = %v", got)
	}
	if _, err := svc.ApplyManaged(ManagedEgressFilterRequest{Filter: &egressfilter.Filter{Server: true}, ManagedBy: "central", ServerAddress: "198.51.100.8"}); err != nil {
		t.Fatal(err)
	}
	// A local save on the node keeps the address its master uses.
	if _, err := svc.SetLocal(EgressFilterSettings{Filter: egressfilter.Filter{Server: true}}); err != nil {
		t.Fatal(err)
	}
	if svc.Settings().MasterAddress != "198.51.100.8" {
		t.Fatalf("a local save dropped the master's address: %+v", svc.Settings())
	}
}
