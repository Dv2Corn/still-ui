package panelimport

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport/marzbantest"
)

const fixturePort = 20000

func readFixture(t *testing.T) *Plan {
	t.Helper()
	dbPath, cfg := marzbantest.Build(t, fixturePort)
	plan, err := ReadMarzban(dbPath, cfg)
	if err != nil {
		t.Fatalf("ReadMarzban: %v", err)
	}
	return plan
}

func planClients(t *testing.T, plan *Plan, tag string) (map[string]model.Client, map[string]any) {
	t.Helper()
	for _, in := range plan.Inbounds {
		if in.Tag != tag {
			continue
		}
		var settings map[string]any
		var parsed struct {
			Clients []model.Client `json:"clients"`
		}
		if err := json.Unmarshal([]byte(in.Settings), &settings); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(in.Settings), &parsed); err != nil {
			t.Fatal(err)
		}
		out := map[string]model.Client{}
		for _, c := range parsed.Clients {
			out[c.Email] = c
		}
		return out, settings
	}
	t.Fatalf("inbound %q is not in the plan", tag)
	return nil, nil
}

func TestReadMarzbanInbounds(t *testing.T) {
	plan := readFixture(t)
	var got []string
	for i, in := range plan.Inbounds {
		got = append(got, in.Tag)
		if in.Port != fixturePort+i+1 || !in.Enable || in.Listen != "0.0.0.0" {
			t.Errorf("%q: port %d enable %v listen %q", in.Tag, in.Port, in.Enable, in.Listen)
		}
	}
	want := []string{
		marzbantest.TagReality, marzbantest.TagVlessWS, marzbantest.TagVlessTLS, marzbantest.TagVmessWS,
		marzbantest.TagVmessHTTP, marzbantest.TagTrojanGRPC, marzbantest.TagHTTPUpgrade, marzbantest.TagXHTTP, marzbantest.TagSS,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("inbounds = %q, want %q", got, want)
	}
	if !slices.Equal(plan.Admins, []string{"root", "reseller"}) {
		t.Errorf("admins = %q", plan.Admins)
	}
	wantNotes := []string{
		`inbound "VLESS FALLBACK WS": no single numeric port`,
		`user "erin": the yearly traffic reset has no equivalent`,
		`user "alice": its vless and vmess credentials differ, so vmess is the separate client "alice.vmess"`,
		`user "alice": its trojan and shadowsocks credentials differ, so shadowsocks is the separate client "alice.ss"`,
		`host "{USERNAME} cdn" of inbound "VLESS WS TLS": only its first address "cdn.example.com" is kept`,
	}
	for _, w := range wantNotes {
		if !slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.HasPrefix(n, w) }) {
			t.Errorf("no note starting with %q in %q", w, plan.Notes)
		}
	}
}

func TestReadMarzbanRealityKeepsKeys(t *testing.T) {
	plan := readFixture(t)
	var stream struct {
		Security string `json:"security"`
		Reality  struct {
			Target      string   `json:"target"`
			PrivateKey  string   `json:"privateKey"`
			ShortIds    []string `json:"shortIds"`
			ServerNames []string `json:"serverNames"`
			Settings    struct {
				PublicKey   string `json:"publicKey"`
				Fingerprint string `json:"fingerprint"`
			} `json:"settings"`
		} `json:"realitySettings"`
	}
	if err := json.Unmarshal([]byte(plan.Inbounds[0].StreamSettings), &stream); err != nil {
		t.Fatal(err)
	}
	r := stream.Reality
	if r.PrivateKey != marzbantest.RealityPrivateKey || !slices.Equal(r.ShortIds, []string{marzbantest.ShortID}) ||
		!slices.Equal(r.ServerNames, []string{"www.example.org"}) || r.Target != "www.example.org:443" {
		t.Errorf("reality settings changed: %+v", r)
	}
	if r.Settings.PublicKey != marzbantest.RealityPublicKey || r.Settings.Fingerprint != "chrome" {
		t.Errorf("link settings = %+v, want the public key of the private key and chrome", r.Settings)
	}
}

func TestReadMarzbanClients(t *testing.T) {
	plan := readFixture(t)
	reality, _ := planClients(t, plan, marzbantest.TagReality)
	want := map[string]model.Client{
		"alice": {
			ID: marzbantest.AliceVless, Flow: "xtls-rprx-vision", Email: "alice", Enable: true,
			TotalGB: 10 * marzbantest.GiB, ExpiryTime: marzbantest.AliceExpire * 1000, Comment: "paid until 2100",
			TrafficReset: "monthly", CreatedAt: 1735725600500,
		},
		"bob":   {ID: marzbantest.BobVless, Email: "bob", Enable: false, TrafficReset: "never", CreatedAt: 1735725600500},
		"carol": {ID: marzbantest.CarolVless, Flow: "xtls-rprx-vision", Email: "carol", Enable: true, TotalGB: 5 * marzbantest.GiB, TrafficReset: "weekly", CreatedAt: 1735725600500},
		"dave":  {ID: marzbantest.DaveVless, Email: "dave", Enable: true, ExpiryTime: marzbantest.DaveExpire * 1000, TrafficReset: "daily", CreatedAt: 1735725600500},
		"erin":  {ID: marzbantest.ErinVless, Email: "erin", Enable: true, TotalGB: 3 * marzbantest.GiB, ExpiryTime: -marzbantest.ErinHoldSecs * 1000, TrafficReset: "never", CreatedAt: 1735725600500},
	}
	for email, w := range want {
		g := reality[email]
		g.SubID, w.SubID = "", ""
		if !reflect.DeepEqual(g, w) {
			t.Errorf("%s on %s:\n got %+v\nwant %+v", email, marzbantest.TagReality, g, w)
		}
	}
	if len(reality) != len(want) {
		t.Errorf("%s has %d clients, want %d (frank has no VLESS proxy)", marzbantest.TagReality, len(reality), len(want))
	}
	aliceSub := reality["alice"].SubID
	if len(aliceSub) != 16 || aliceSub == marzbantest.DerivableAliceSubID || aliceSub == reality["bob"].SubID {
		t.Errorf("alice subId = %q, want a random 16-character id of her own", aliceSub)
	}
	if again, _ := planClients(t, readFixture(t), marzbantest.TagReality); again["alice"].SubID == aliceSub {
		t.Errorf("two reads of the same database gave alice the same subId %q: it is derived, not random", aliceSub)
	}

	ws, _ := planClients(t, plan, marzbantest.TagVlessWS)
	if _, ok := ws["bob"]; ok {
		t.Errorf("bob is on %s although Marzban excludes that inbound for him", marzbantest.TagVlessWS)
	}
	if ws["alice"].Flow != "" || ws["alice"].ID != marzbantest.AliceVless {
		t.Errorf("alice on a WebSocket inbound = %+v, want her VLESS id and no flow", ws["alice"])
	}
	for _, tag := range []string{marzbantest.TagVlessTLS} {
		if c, _ := planClients(t, plan, tag); c["alice"].Flow != "xtls-rprx-vision" {
			t.Errorf("alice flow on %s = %q, want vision", tag, c["alice"].Flow)
		}
	}
	if c, _ := planClients(t, plan, marzbantest.TagXHTTP); c["alice"].Flow != "" {
		t.Errorf("alice flow on XHTTP = %q, want none", c["alice"].Flow)
	}

	vmess, _ := planClients(t, plan, marzbantest.TagVmessWS)
	if vmess["alice.vmess"].SubID != aliceSub {
		t.Errorf("alice's VMess client has subId %q, want her one subscription %q", vmess["alice.vmess"].SubID, aliceSub)
	}
	if vmess["alice.vmess"].ID != marzbantest.AliceVmess || vmess["frank"].ID != marzbantest.FrankVmess || len(vmess) != 2 {
		t.Errorf("vmess clients = %+v", vmess)
	}
	trojan, _ := planClients(t, plan, marzbantest.TagTrojanGRPC)
	if trojan["alice"].Password != marzbantest.AliceTrojan || len(trojan) != 1 {
		t.Errorf("trojan clients = %+v", trojan)
	}
	ss, settings := planClients(t, plan, marzbantest.TagSS)
	if ss["alice.ss"].Password != marzbantest.AliceSS || settings["method"] != "chacha20-ietf-poly1305" || settings["network"] != "tcp,udp" {
		t.Errorf("shadowsocks clients = %+v, settings = %v", ss, settings)
	}
}

func TestReadMarzbanUsageAndSubUsers(t *testing.T) {
	plan := readFixture(t)
	used := map[string]int64{}
	for _, in := range plan.Inbounds {
		for _, stat := range in.ClientStats {
			if _, dup := used[stat.Email]; dup {
				t.Errorf("%s has two traffic rows", stat.Email)
			}
			used[stat.Email] = stat.Down + stat.Up
		}
	}
	want := map[string]int64{"alice": marzbantest.GiB, "bob": 0, "carol": 5 * marzbantest.GiB, "dave": 100, "erin": 0, "frank": 42}
	if !reflect.DeepEqual(used, want) {
		t.Errorf("used traffic = %v, want %v", used, want)
	}
	reality, _ := planClients(t, plan, marzbantest.TagReality)
	vmess, _ := planClients(t, plan, marzbantest.TagVmessWS)
	wantSubs := []model.LegacySubUser{
		{Username: "alice", SubID: reality["alice"].SubID, NotBefore: marzbantest.CreatedCeil},
		{Username: "frank", SubID: vmess["frank"].SubID, NotBefore: marzbantest.FrankRevoked},
	}
	for _, w := range wantSubs {
		if !slices.Contains(plan.SubUsers, w) {
			t.Errorf("sub users lack %+v: %+v", w, plan.SubUsers)
		}
	}
	if len(plan.SubUsers) != 6 || plan.SubSecret != marzbantest.Secret {
		t.Errorf("%d sub users, secret %q", len(plan.SubUsers), plan.SubSecret)
	}
}

func TestReadMarzbanHosts(t *testing.T) {
	plan := readFixture(t)
	hosts := plan.Hosts[marzbantest.TagVlessWS]
	if len(hosts) != 3 {
		t.Fatalf("%d hosts, want 3", len(hosts))
	}
	cdn := model.Host{
		SortOrder: 0, Remark: "{USERNAME} cdn", Address: "cdn.example.com", Port: 443, Security: "tls",
		Sni: "sni.example.com", Snis: []string{"sni.example.com"}, HostHeader: "host.example.com", Path: "/cdn-path",
		Alpn: []string{"h2", "http/1.1"}, Fingerprint: "firefox", RandomCount: 1,
	}
	if !reflect.DeepEqual(hosts[0], cdn) {
		t.Errorf("cdn host:\n got %+v\nwant %+v", hosts[0], cdn)
	}
	if hosts[1].Address != "" || hosts[1].Sni != "vpn.example.com" || hosts[1].Security != "same" {
		t.Errorf("{SERVER_IP} host = %+v, want an empty address that follows the request", hosts[1])
	}
	if !hosts[2].IsDisabled {
		t.Errorf("the disabled host came over enabled")
	}
}

func TestReadMarzbanLeavesSourceUntouched(t *testing.T) {
	dbPath, cfg := marzbantest.Build(t, fixturePort)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMarzban(dbPath, cfg); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("reading changed the source database file")
	}
	entries, _ := os.ReadDir(strings.TrimSuffix(dbPath, "/db.sqlite3"))
	if len(entries) != 1 {
		t.Fatalf("reading left files beside the source: %v", entries)
	}
}

func TestReadMarzbanRejectsOtherDatabases(t *testing.T) {
	dbPath, _ := marzbantest.Build(t, fixturePort)
	if _, err := ReadMarzban(dbPath, []byte(`{"inbounds":[{"tag":"api","protocol":"dokodemo-door","port":1}]}`)); err == nil ||
		!strings.Contains(err.Error(), "no vmess, vless, trojan or shadowsocks inbound") {
		t.Errorf("config without proxy inbounds: %v", err)
	}
	if _, err := ReadXUI(dbPath); err == nil || err.Error() != "this is a Marzban database: import it as marzban" {
		t.Errorf("a Marzban database read as x-ui: %v", err)
	}
}
