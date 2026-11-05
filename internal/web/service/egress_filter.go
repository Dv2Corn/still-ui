package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/egressfilter"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const egressFilterSettingKey = "egressFilter"

// egressFilterOutboundTag names the blackhole added when the template has none.
const egressFilterOutboundTag = "egress-block"

// Node scopes: which of this panel's nodes get its egress filter.
const (
	EgressNodeScopeNone     = ""
	EgressNodeScopeAll      = "all"
	EgressNodeScopeSelected = "selected"
)

// EgressFilterSettings is the stored "Egress filter" setting of this panel.
type EgressFilterSettings struct {
	egressfilter.Filter
	// NodeScope and NodeIds choose the nodes this panel hands its filter to.
	NodeScope string `json:"nodeScope"`
	NodeIds   []int  `json:"nodeIds"`
	// NodeScopeUsed is set once nodes were ever in scope: only then can one need a release.
	NodeScopeUsed bool `json:"nodeScopeUsed,omitempty"`
	// ManagedBy is the panelGuid of the central panel that set this filter; empty
	// when the filter is this panel's own.
	ManagedBy string `json:"managedBy,omitempty"`
	ManagedAt int64  `json:"managedAt,omitempty"`
}

// EgressFilterNodeState is how far a node is with this panel's filter.
type EgressFilterNodeState struct {
	Id     int    `json:"id"`
	Name   string `json:"name"`
	Wanted string `json:"wanted"` // apply | release
	Synced bool   `json:"synced"`
	Error  string `json:"error,omitempty"`
	At     int64  `json:"at,omitempty"`
}

// EgressFilterStatus is the setting plus what the host and the nodes are doing.
type EgressFilterStatus struct {
	Settings EgressFilterSettings `json:"settings"`
	Host     egressfilter.Status  `json:"host"`
	// HostNeeded is false when nothing is blocked for AmneziaWG kernel-mode clients.
	HostNeeded  bool   `json:"hostNeeded"`
	Error       string `json:"error,omitempty"`
	ServerPorts []int  `json:"serverPorts"`
	// TemplatePrivateBlocked: the Xray template already sends geoip:private to a
	// blackhole for every client, so no second Xray rule is added for it.
	TemplatePrivateBlocked bool                    `json:"templatePrivateBlocked"`
	Nodes                  []EgressFilterNodeState `json:"nodes"`
}

// ManagedEgressFilterRequest is what a central panel sends a node.
type ManagedEgressFilterRequest struct {
	Filter    *egressfilter.Filter `json:"filter,omitempty"`
	Release   bool                 `json:"release,omitempty"`
	ManagedBy string               `json:"managedBy"`
}

// egressHost is the nftables side; swapped out in tests.
type egressHost interface {
	Apply(spec egressfilter.Spec) error
	Ensure(spec egressfilter.Spec) error
	Remove() error
	Status() egressfilter.Status
}

var (
	egressFilterHost egressHost = egressfilter.Default()
	egressFilterMu   sync.Mutex
	egressFilterErr  string

	// egressLocalAddrs lists this host's own addresses; swapped out in tests.
	egressLocalAddrs = interfaceAddrs
	// sshdConfigPath is read for the SSH ports; swapped out in tests.
	sshdConfigPath = "/etc/ssh/sshd_config"
	// egressFilterPush delivers a request to one node; swapped out in tests.
	egressFilterPush = pushEgressFilterToNode
)

// EgressFilterService is "Egress filter" in Xray Configs > Routing.
type EgressFilterService struct {
	settingService SettingService
	inboundService InboundService
	nodeService    NodeService
}

// Settings reads the stored setting; an unreadable value counts as nothing blocked.
func (s *EgressFilterService) Settings() EgressFilterSettings {
	var out EgressFilterSettings
	raw, err := s.settingService.getString(egressFilterSettingKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		logger.Warning("egress filter: stored setting unreadable, nothing is blocked: ", err)
		return EgressFilterSettings{}
	}
	if f, err := out.Normalize(); err == nil {
		out.Filter = f
	} else {
		logger.Warning("egress filter: stored custom list unreadable, ignoring it: ", err)
		out.Custom = nil
	}
	return out
}

func (s *EgressFilterService) save(v EgressFilterSettings) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.settingService.setString(egressFilterSettingKey, string(raw))
}

// ServerPorts are the panel and SSH ports. The subscription port (refreshed through the tunnel)
// and any port that also serves an inbound stay open.
func (s *EgressFilterService) ServerPorts() []uint16 {
	var ports []uint16
	add := func(p int) {
		if p > 0 && p <= 65535 {
			ports = append(ports, uint16(p))
		}
	}
	if p, err := s.settingService.GetPort(); err == nil {
		add(p)
	}
	for _, p := range sshdPorts(sshdConfigPath) {
		add(p)
	}
	inboundPorts := map[uint16]bool{}
	if inbounds, err := s.inboundService.GetAllInbounds(); err == nil {
		for _, in := range inbounds {
			if in.NodeID == nil && in.Port > 0 && in.Port <= 65535 {
				inboundPorts[uint16(in.Port)] = true
			}
		}
	}
	ports = slices.DeleteFunc(ports, func(p uint16) bool { return inboundPorts[p] || p == egressfilter.DNSPort })
	slices.Sort(ports)
	return slices.Compact(ports)
}

// sshdPorts reads "Port" lines from sshd_config and its drop-in directory; 22 when none.
func sshdPorts(path string) []int {
	files := []string{path}
	if more, err := filepath.Glob(filepath.Join(filepath.Dir(path), "sshd_config.d", "*.conf")); err == nil {
		files = append(files, more...)
	}
	var ports []int
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 || !strings.EqualFold(fields[0], "port") {
				continue
			}
			if p, err := strconv.Atoi(fields[1]); err == nil && p > 0 && p <= 65535 {
				ports = append(ports, p)
			}
		}
		_ = f.Close()
	}
	if len(ports) == 0 {
		return []int{22}
	}
	return ports
}

func interfaceAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || !ipNet.IP.IsGlobalUnicast() {
			continue
		}
		out = append(out, ipNet.IP.String())
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (s *EgressFilterService) spec(f egressfilter.Filter) egressfilter.Spec {
	spec := egressfilter.Spec{Filter: f}
	if f.Server {
		spec.ServerPorts = s.ServerPorts()
	}
	return spec
}

// Status is safe to poll: it never changes the host or the nodes.
func (s *EgressFilterService) Status() EgressFilterStatus {
	settings := s.Settings()
	spec := s.spec(settings.Filter)
	st := EgressFilterStatus{
		Settings:    settings,
		HostNeeded:  spec.Needed(),
		ServerPorts: []int{},
		Nodes:       egressNodeStates(),
	}
	for _, p := range s.ServerPorts() {
		st.ServerPorts = append(st.ServerPorts, int(p))
	}
	st.TemplatePrivateBlocked = s.templatePrivateBlocked()
	egressFilterMu.Lock()
	st.Host = egressFilterHost.Status()
	st.Error = egressFilterErr
	egressFilterMu.Unlock()
	return st
}

func (s *EgressFilterService) templatePrivateBlocked() bool {
	raw, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return false
	}
	var tmpl struct {
		Routing   json.RawMessage `json:"routing"`
		Outbounds json.RawMessage `json:"outbounds"`
	}
	if json.Unmarshal([]byte(raw), &tmpl) != nil {
		return false
	}
	return routingBlocksPrivate(tmpl.Routing, tmpl.Outbounds, map[string]bool{"api": true})
}

// SetLocal stores the operator's filter, converges the host table and hands it to the chosen nodes;
// the Xray half follows from the next config build, so the caller restarts Xray.
func (s *EgressFilterService) SetLocal(in EgressFilterSettings) (EgressFilterStatus, error) {
	f, err := in.Normalize()
	if err != nil {
		return EgressFilterStatus{}, err
	}
	switch in.NodeScope {
	case EgressNodeScopeNone, EgressNodeScopeAll:
		in.NodeIds = nil
	case EgressNodeScopeSelected:
		slices.Sort(in.NodeIds)
		in.NodeIds = slices.Compact(in.NodeIds)
	default:
		return EgressFilterStatus{}, fmt.Errorf("unknown node scope %q", in.NodeScope)
	}
	in.Filter = f
	in.ManagedBy, in.ManagedAt = "", 0
	in.NodeScopeUsed = in.NodeScope != EgressNodeScopeNone || s.Settings().NodeScopeUsed
	if err := s.save(in); err != nil {
		return EgressFilterStatus{}, err
	}
	s.applyHost(f)
	s.SyncNodes(true)
	return s.Status(), nil
}

// ApplyManaged takes a filter from the central panel (node side). changed reports
// whether the filter itself changed, i.e. whether Xray needs reconciling.
func (s *EgressFilterService) ApplyManaged(req ManagedEgressFilterRequest) (changed bool, err error) {
	if strings.TrimSpace(req.ManagedBy) == "" {
		return false, errors.New("managedBy is required")
	}
	cur := s.Settings()
	if req.Release {
		if cur.ManagedBy != req.ManagedBy {
			return false, nil
		}
		cur.ManagedBy, cur.ManagedAt = "", 0
		return false, s.save(cur)
	}
	if req.Filter == nil {
		return false, errors.New("filter is required")
	}
	f, err := req.Filter.Normalize()
	if err != nil {
		return false, err
	}
	if cur.ManagedBy == req.ManagedBy && cur.Fingerprint() == f.Fingerprint() {
		return false, nil
	}
	changed = cur.Fingerprint() != f.Fingerprint()
	cur.Filter = f
	cur.ManagedBy = req.ManagedBy
	cur.ManagedAt = time.Now().Unix()
	if err := s.save(cur); err != nil {
		return false, err
	}
	if changed {
		s.applyHost(f)
		// A node can manage nodes of its own: they follow it.
		go s.SyncNodes(true)
	}
	return changed, nil
}

func (s *EgressFilterService) applyHost(f egressfilter.Filter) {
	spec := s.spec(f)
	egressFilterMu.Lock()
	defer egressFilterMu.Unlock()
	if spec.Needed() {
		recordEgressFilterErr(egressFilterHost.Apply(spec))
	} else {
		recordEgressFilterErr(egressFilterHost.Remove())
	}
}

// EnsureHostFilter reloads the table after a reboot or foreign flush and follows the panel's ports;
// with nothing to block it never runs nft.
func (s *EgressFilterService) EnsureHostFilter() {
	settings := s.Settings()
	spec := s.spec(settings.Filter)
	if !spec.Needed() {
		return
	}
	egressFilterMu.Lock()
	defer egressFilterMu.Unlock()
	recordEgressFilterErr(egressFilterHost.Ensure(spec))
}

// recordEgressFilterErr keeps the last host error for the UI; caller holds egressFilterMu.
func recordEgressFilterErr(err error) {
	prev := egressFilterErr
	egressFilterErr = ""
	if err != nil {
		egressFilterErr = err.Error()
		if egressFilterErr != prev {
			logger.Warning("egress filter: nftables filter not applied: ", err)
		}
	}
}

// Node sync -----------------------------------------------------------------

// egressRetryAfter spaces out retries to a node that refused or could not be reached.
const egressRetryAfter = 10 * time.Minute

type egressNodeSyncState struct {
	name    string
	want    string // "apply:<fingerprint>" or "release:<guid>"
	synced  bool
	err     string
	at      time.Time
	retryAt time.Time
}

var egressNodeSync = struct {
	sync.Mutex
	m map[int]*egressNodeSyncState
}{m: map[int]*egressNodeSyncState{}}

func egressNodeStates() []EgressFilterNodeState {
	egressNodeSync.Lock()
	defer egressNodeSync.Unlock()
	out := make([]EgressFilterNodeState, 0, len(egressNodeSync.m))
	for id, st := range egressNodeSync.m {
		wanted, _, _ := strings.Cut(st.want, ":")
		row := EgressFilterNodeState{Id: id, Name: st.name, Wanted: wanted, Synced: st.synced, Error: st.err}
		if !st.at.IsZero() {
			row.At = st.at.Unix()
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b EgressFilterNodeState) int { return a.Id - b.Id })
	return out
}

func egressNodeInScope(settings EgressFilterSettings, id int) bool {
	switch settings.NodeScope {
	case EgressNodeScopeAll:
		return true
	case EgressNodeScopeSelected:
		return slices.Contains(settings.NodeIds, id)
	}
	return false
}

func pushEgressFilterToNode(ctx context.Context, n *model.Node, req ManagedEgressFilterRequest) error {
	mgr := runtime.GetManager()
	if mgr == nil {
		return errors.New("runtime manager unavailable")
	}
	remote, err := mgr.RemoteFor(n)
	if err != nil {
		return err
	}
	return remote.ApplyManagedEgressFilter(ctx, req)
}

// SyncNodes hands the filter to nodes in scope and releases the rest, once per change or restart;
// unreachable or refusing nodes are retried later (force retries refused ones now).
func (s *EgressFilterService) SyncNodes(force bool) {
	nodes, err := s.nodeService.GetAll()
	if err != nil || len(nodes) == 0 {
		return
	}
	settings := s.Settings()
	if settings.NodeScope == EgressNodeScopeNone && !settings.NodeScopeUsed {
		return
	}
	guid, err := s.settingService.GetPanelGuid()
	if err != nil || guid == "" {
		return
	}
	fp := settings.Fingerprint()
	now := time.Now()

	type job struct {
		node *model.Node
		want string
		req  ManagedEgressFilterRequest
	}
	var jobs []job
	present := map[int]bool{}
	egressNodeSync.Lock()
	for _, n := range nodes {
		present[n.Id] = true
		want, req := "release:"+guid, ManagedEgressFilterRequest{Release: true, ManagedBy: guid}
		if egressNodeInScope(settings, n.Id) {
			f := settings.Filter
			want, req = "apply:"+fp, ManagedEgressFilterRequest{Filter: &f, ManagedBy: guid}
		}
		st := egressNodeSync.m[n.Id]
		if st == nil {
			st = &egressNodeSyncState{}
			egressNodeSync.m[n.Id] = st
		}
		st.name = n.Name
		if st.want != want {
			st.want, st.synced, st.err, st.retryAt = want, false, "", time.Time{}
		}
		if st.synced || !n.Enable || n.Status != "online" || (!force && now.Before(st.retryAt)) {
			continue
		}
		jobs = append(jobs, job{node: n, want: want, req: req})
	}
	for id := range egressNodeSync.m {
		if !present[id] {
			delete(egressNodeSync.m, id)
		}
	}
	egressNodeSync.Unlock()
	if len(jobs) == 0 {
		return
	}

	ids := make([]int, len(jobs))
	results, _ := fanoutInboundResults(ids, nodeFanoutConcurrency, func(i int) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return egressFilterPush(ctx, jobs[i].node, jobs[i].req)
	})

	egressNodeSync.Lock()
	defer egressNodeSync.Unlock()
	for i, j := range jobs {
		st := egressNodeSync.m[j.node.Id]
		if st == nil || st.want != j.want {
			continue
		}
		err := results[i]
		st.at = time.Now()
		// A node too old to have the endpoint cannot be managed, so it has nothing to release.
		if err != nil && j.req.Release && strings.Contains(err.Error(), "HTTP 404") {
			err = nil
		}
		if err == nil {
			st.synced, st.err = true, ""
			continue
		}
		msg := err.Error()
		if strings.Contains(msg, "HTTP 404") {
			msg = "the node's panel is too old for the egress filter; update it"
		}
		if st.err != msg {
			logger.Warning("egress filter: node ", j.node.Name, ": ", msg)
		}
		st.synced, st.err, st.retryAt = false, msg, st.at.Add(egressRetryAfter)
	}
}

// Xray config ---------------------------------------------------------------

// egressClientTags are the inbounds carrying client traffic, relays (AmneziaWG userspace, TUIC,
// MTProto) included; the API inbound and the panel's own egress bridges are never filtered.
func egressClientTags(cfg *xray.Config, inbounds []*model.Inbound) []string {
	local := map[string]bool{}
	for _, in := range inbounds {
		if in.NodeID == nil && in.Enable && in.Tag != "" {
			local[in.Tag] = true
		}
	}
	var tags []string
	for i := range cfg.InboundConfigs {
		tag := cfg.InboundConfigs[i].Tag
		if local[tag] && tag != "api" && tag != PanelEgressInboundTag {
			tags = append(tags, tag)
		}
	}
	slices.Sort(tags)
	return slices.Compact(tags)
}

// routingBlocksPrivate reports whether, for client traffic, the routing already sends
// geoip:private to a blackhole before any rule that could route it elsewhere.
func routingBlocksPrivate(routingRaw, outboundsRaw json.RawMessage, nonClient map[string]bool) bool {
	var routing struct {
		Rules []map[string]any `json:"rules"`
	}
	var outbounds []map[string]any
	if len(routingRaw) == 0 || json.Unmarshal(routingRaw, &routing) != nil {
		return false
	}
	if len(outboundsRaw) > 0 && json.Unmarshal(outboundsRaw, &outbounds) != nil {
		return false
	}
	blackholes := map[string]bool{}
	for _, o := range outbounds {
		if p, _ := o["protocol"].(string); strings.EqualFold(p, "blackhole") {
			if tag, _ := o["tag"].(string); tag != "" {
				blackholes[tag] = true
			}
		}
	}
	for _, rule := range routing.Rules {
		if tags := stringList(rule["inboundTag"]); len(tags) > 0 && !slices.ContainsFunc(tags, func(t string) bool { return !nonClient[t] }) {
			continue // only reachable from the API and the panel's own bridges
		}
		out, _ := rule["outboundTag"].(string)
		blanket := true
		for k := range rule {
			switch k {
			case "type", "ip", "outboundTag", "ruleTag":
			default:
				blanket = false
			}
		}
		if blanket && blackholes[out] && slices.ContainsFunc(stringList(rule["ip"]), func(ip string) bool { return strings.EqualFold(ip, "geoip:private") }) {
			return true
		}
		if !blackholes[out] {
			return false // a rule that can send private destinations elsewhere comes first
		}
		// another blackhole rule only blocks more; keep looking
	}
	return false
}

func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// egressOwnTargets is this server as a destination.
type egressOwnTargets struct {
	Addrs []string
	Ports []uint16
}

var egressLoopback = []string{"127.0.0.0/8", "::1/128"}

// injectEgressFilter puts blackhole rules first for client inbounds; address blocks also become
// freedom finalRules, since a destination named by domain is only resolved there.
func injectEgressFilter(cfg *xray.Config, clientTags []string, f egressfilter.Filter, own egressOwnTargets) {
	if len(clientTags) == 0 || !f.Any() {
		return
	}
	custom, err := egressfilter.ParseCustom(f.Custom)
	if err != nil {
		logger.Warning("egress filter: custom list unreadable, skipping it: ", err)
		custom = egressfilter.Custom{}
	}
	var routing map[string]any
	if len(cfg.RouterConfig) > 0 && json.Unmarshal(cfg.RouterConfig, &routing) != nil {
		logger.Warning("egress filter: routing section unreadable, skipping the Xray rules")
		return
	}
	if routing == nil {
		routing = map[string]any{}
	}
	var outbounds []map[string]any
	if len(cfg.OutboundConfigs) > 0 && json.Unmarshal(cfg.OutboundConfigs, &outbounds) != nil {
		logger.Warning("egress filter: outbounds unreadable, skipping the Xray rules")
		return
	}

	nonClient := map[string]bool{}
	clientSet := map[string]bool{}
	for _, t := range clientTags {
		clientSet[t] = true
	}
	for i := range cfg.InboundConfigs {
		if t := cfg.InboundConfigs[i].Tag; !clientSet[t] {
			nonClient[t] = true
		}
	}
	nonClient["api"] = true
	routingRaw, _ := json.Marshal(routing)
	outboundsRaw, _ := json.Marshal(outbounds)
	privateCovered := routingBlocksPrivate(routingRaw, outboundsRaw, nonClient)

	tags := make([]any, 0, len(clientTags))
	for _, t := range clientTags {
		tags = append(tags, t)
	}
	var serverPorts []egressfilter.PortRange
	if f.Server {
		for _, p := range own.Ports {
			serverPorts = append(serverPorts, egressfilter.PortRange{From: p, To: p})
		}
	}

	var newRules []any
	rule := func(name string, fields map[string]any) {
		r := map[string]any{"type": "field", "inboundTag": tags, "ruleTag": "egress-filter-" + name}
		for k, v := range fields {
			r[k] = v
		}
		newRules = append(newRules, r)
	}
	if mail := f.MailPorts(); len(mail) > 0 {
		rule("mail", map[string]any{"network": "tcp", "port": egressfilter.PortList(mail)})
	}
	if f.Private && !privateCovered {
		rule("private", map[string]any{"ip": []any{"geoip:private"}})
	}
	if f.Server {
		rule("loopback", map[string]any{"ip": toAnyList(egressLoopback)})
		if len(serverPorts) > 0 && len(own.Addrs) > 0 {
			rule("server", map[string]any{"network": "tcp", "ip": toAnyList(own.Addrs), "port": egressfilter.PortList(serverPorts)})
		}
	}
	if ips := custom.Strings()[:len(custom.V4)+len(custom.V6)]; len(ips) > 0 {
		rule("custom-ip", map[string]any{"ip": toAnyList(ips)})
	}
	if len(custom.Ports) > 0 {
		rule("custom-port", map[string]any{"port": egressfilter.PortList(custom.Ports)})
	}
	if len(newRules) == 0 {
		return
	}

	blackhole, outbounds := ensureBlackholeOutbound(outbounds, egressFilterOutboundTag)
	for _, r := range newRules {
		r.(map[string]any)["outboundTag"] = blackhole
	}
	addFreedomBlocks(outbounds, freedomBlocks{
		serverIPs:   append(slices.Clone(own.Addrs), egressLoopback...),
		serverPorts: egressfilter.PortList(serverPorts),
		private:     f.Private,
		loopback:    f.Server,
		customIPs:   custom.Strings()[:len(custom.V4)+len(custom.V6)],
	})
	rawOut, err := json.Marshal(outbounds)
	if err != nil {
		return
	}
	rules, _ := routing["rules"].([]any)
	routing["rules"] = append(newRules, rules...)
	rawRouting, err := json.Marshal(routing)
	if err != nil {
		return
	}
	cfg.OutboundConfigs = json_util.RawMessage(rawOut)
	cfg.RouterConfig = json_util.RawMessage(rawRouting)
}

func toAnyList(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// ensureBlackholeOutbound returns the first tagged blackhole, adding one when there is none.
func ensureBlackholeOutbound(outbounds []map[string]any, tag string) (string, []map[string]any) {
	tags := map[string]bool{}
	for _, o := range outbounds {
		t, _ := o["tag"].(string)
		tags[t] = true
		if p, _ := o["protocol"].(string); strings.EqualFold(p, "blackhole") && t != "" {
			return t, outbounds
		}
	}
	name := tag
	for i := 2; tags[name]; i++ {
		name = tag + "-" + strconv.Itoa(i)
	}
	return name, append(outbounds, map[string]any{"tag": name, "protocol": "blackhole"})
}

// freedomBlocks is what freedom refuses after resolving a domain: routing matches "ip" only against
// the address the client named, so a domain resolving to a blocked address would be dialled.
type freedomBlocks struct {
	serverIPs   []string
	serverPorts string // empty: no server block
	private     bool
	loopback    bool
	customIPs   []string
}

// egressFreedomPorts is every port but DNS, which the filter never blocks.
const egressFreedomPorts = "1-52,54-65535"

// addFreedomBlocks prepends block finalRules (checked after resolution) to every freedom outbound that
// dials itself (dialerProxy ignores them), so no later "allow" lets the destination through.
func addFreedomBlocks(outbounds []map[string]any, b freedomBlocks) {
	for _, o := range outbounds {
		if p, _ := o["protocol"].(string); !strings.EqualFold(p, "freedom") {
			continue
		}
		if stream, ok := o["streamSettings"].(map[string]any); ok {
			if sockopt, ok := stream["sockopt"].(map[string]any); ok {
				if dp, _ := sockopt["dialerProxy"].(string); dp != "" {
					continue
				}
			}
		}
		settings, _ := o["settings"].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		existing, _ := settings["finalRules"].([]any)
		var blocks []any
		if b.serverPorts != "" {
			blocks = append(blocks, map[string]any{"action": "block", "network": "tcp", "ip": toAnyList(b.serverIPs), "port": b.serverPorts})
		}
		var ips []string
		if b.private && !finalRulesBlockPrivate(existing) {
			ips = append(append(ips, egressfilter.PrivateV4...), egressfilter.PrivateV6...)
		}
		if b.loopback {
			ips = append(ips, egressLoopback...)
		}
		ips = append(ips, b.customIPs...)
		if len(ips) > 0 {
			slices.Sort(ips)
			blocks = append(blocks, map[string]any{"action": "block", "ip": toAnyList(slices.Compact(ips)), "port": egressFreedomPorts})
		}
		if len(blocks) == 0 {
			continue
		}
		settings["finalRules"] = append(blocks, existing...)
		o["settings"] = settings
	}
}

// finalRulesBlockPrivate reports whether the outbound's own final rules already block
// geoip:private on every network and port before any rule that could allow it.
func finalRulesBlockPrivate(rules []any) bool {
	for _, v := range rules {
		rule, _ := v.(map[string]any)
		if action, _ := rule["action"].(string); !strings.EqualFold(action, "block") {
			return false
		}
		if rule["network"] != nil || rule["port"] != nil {
			continue
		}
		if slices.ContainsFunc(stringList(rule["ip"]), func(ip string) bool { return strings.EqualFold(ip, "geoip:private") }) {
			return true
		}
	}
	return false
}

// xrayInputs is what GetXrayConfig injects.
func (s *EgressFilterService) xrayInputs() (egressfilter.Filter, egressOwnTargets) {
	f := s.Settings().Filter
	var own egressOwnTargets
	if f.Server {
		own = egressOwnTargets{Addrs: egressLocalAddrs(), Ports: s.ServerPorts()}
	}
	return f, own
}
