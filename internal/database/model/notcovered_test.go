package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Tests for model helpers no test called before (gremlins: NOT COVERED).

func TestApiScopes(t *testing.T) {
	for _, s := range []string{ApiScopeAdmin, ApiScopeMonitor, ApiScopeNodeSync, ApiScopeNodeAdmin} {
		if !IsKnownApiScope(s) {
			t.Errorf("IsKnownApiScope(%q) = false", s)
		}
	}
	if IsKnownApiScope("root") || IsKnownApiScope("") {
		t.Error("unknown scopes accepted")
	}
	if !IsNodeSyncApiScope(ApiScopeNodeSync) || !IsNodeSyncApiScope(ApiScopeNodeAdmin) ||
		IsNodeSyncApiScope(ApiScopeAdmin) || IsNodeSyncApiScope(ApiScopeMonitor) {
		t.Error("IsNodeSyncApiScope is wrong")
	}
}

func TestInboundResetFlags(t *testing.T) {
	yes, no := true, false
	if (Inbound{}).PreservesClientTraffic() || (Inbound{PreserveClientTraffic: &no}).PreservesClientTraffic() ||
		!(Inbound{PreserveClientTraffic: &yes}).PreservesClientTraffic() {
		t.Error("PreservesClientTraffic is wrong")
	}
	for reset, want := range map[string]bool{"": false, "never": false, "daily": true} {
		if got := (&Inbound{TrafficReset: reset}).HasTrafficResetSchedule(); got != want {
			t.Errorf("HasTrafficResetSchedule(%q) = %v", reset, got)
		}
	}
}

func TestNormalizeXhttpExtraShapes(t *testing.T) {
	stream := func(extra any) map[string]any {
		return map[string]any{"xhttpSettings": map[string]any{"path": "/p", "extra": extra}}
	}
	if s := stream("text"); NormalizeXhttpExtra(s) {
		t.Error("a non-object extra must be left alone")
	}
	s := stream(nil)
	if !NormalizeXhttpExtra(s) {
		t.Fatal("a null extra is flattened")
	}
	if xh := s["xhttpSettings"].(map[string]any); xh["path"] != "/p" || len(xh) != 1 {
		t.Fatalf("null extra: %v", xh)
	}
	s = stream(map[string]any{})
	if !NormalizeXhttpExtra(s) || s["xhttpSettings"].(map[string]any)["path"] != "/p" {
		t.Fatalf("empty extra: %v", s)
	}
}

func TestStripVmessClientSecurity(t *testing.T) {
	for _, in := range []string{"", "{", `{"clients":"x"}`, `{"clients":[1,{"id":"a"}]}`} {
		if out, changed := StripVmessClientSecurity(in); changed || out != in {
			t.Errorf("%q: changed = %v", in, changed)
		}
	}
	out, changed := StripVmessClientSecurity(`{"clients":[1,{"id":"a","security":"auto"}]}`)
	if !changed || strings.Contains(out, "security") || !strings.Contains(out, `"id": "a"`) {
		t.Fatalf("strip: %v %s", changed, out)
	}
}

func TestMasqueClientsToCore(t *testing.T) {
	for _, in := range []string{"", "{", `{"clients":"x"}`, `{"clients":[1,{"email":"a"}]}`} {
		if out, changed := MasqueClientsToCore(in); changed || out != in {
			t.Errorf("%q: changed = %v", in, changed)
		}
	}
	out, changed := MasqueClientsToCore(`{"clients":[1,{"password":"p1"},{"password":"old","pass":"kept"}]}`)
	var got struct{ Clients []any }
	if err := json.Unmarshal([]byte(out), &got); err != nil || !changed {
		t.Fatalf("masque: %v %v", changed, err)
	}
	a, b := got.Clients[1].(map[string]any), got.Clients[2].(map[string]any)
	if a["pass"] != "p1" || b["pass"] != "kept" || a["password"] != nil || b["password"] != nil {
		t.Fatalf("masque clients = %v", got.Clients)
	}
}

func TestHealShadowsocksMethodWithoutClients(t *testing.T) {
	out, changed := HealShadowsocksClientMethods(`{"method":"none"}`)
	if !changed || !strings.Contains(out, "chacha20-ietf-poly1305") {
		t.Fatalf("heal = %v %s", changed, out)
	}
}

func TestValidMtprotoAdTag(t *testing.T) {
	good := strings.Repeat("ab", 16)
	if !ValidMtprotoAdTag(good) || ValidMtprotoAdTag(good[:30]) || ValidMtprotoAdTag(good+"ab") || ValidMtprotoAdTag(strings.Repeat("zz", 16)) {
		t.Fatal("ValidMtprotoAdTag is wrong")
	}
}

func TestClientRecordKeepAliveStorage(t *testing.T) {
	cases := []struct {
		in     KeepAliveValue
		legacy int
		ranged KeepAliveValue
	}{{"", 0, ""}, {"25", 25, ""}, {"0", 0, "0"}, {"10-20", 0, "10-20"}}
	for _, c := range cases {
		r := ClientRecord{KeepAlive: c.in}
		if err := r.BeforeSave(nil); err != nil || r.KeepAliveLegacy != c.legacy || r.KeepAliveRange != c.ranged {
			t.Errorf("BeforeSave(%q) = %d %q %v", c.in, r.KeepAliveLegacy, r.KeepAliveRange, err)
		}
	}
	for _, bad := range []KeepAliveValue{"abc", "025", "5s"} {
		r := ClientRecord{KeepAlive: bad}
		if err := r.BeforeSave(nil); err == nil {
			t.Errorf("BeforeSave(%q) accepted", bad)
		}
	}
	for _, c := range []struct {
		legacy int
		ranged KeepAliveValue
		want   KeepAliveValue
	}{{25, "", "25"}, {0, "10-20", "10-20"}, {0, "", ""}, {7, "10-20", "7"}} {
		r := ClientRecord{KeepAliveLegacy: c.legacy, KeepAliveRange: c.ranged, KeepAlive: "stale"}
		if err := r.AfterFind(nil); err != nil || r.KeepAlive != c.want {
			t.Errorf("AfterFind(%d, %q) = %q", c.legacy, c.ranged, r.KeepAlive)
		}
	}
}

func TestClientKeepAliveTextAndRecordReverse(t *testing.T) {
	if (Client{}).KeepAliveText() != "" {
		t.Error("no keepAlive gives empty text")
	}
	v := KeepAliveValue("25")
	if (Client{KeepAlive: &v}).KeepAliveText() != v.String() {
		t.Error("keepAlive text lost")
	}
	if rec := (&Client{Reverse: &ClientReverse{Tag: "r"}}).ToRecord(); rec.Reverse != `{"tag":"r"}` {
		t.Errorf("reverse = %q", rec.Reverse)
	}
	if EncodeClientHostRuleIds([]int{2, 1}) != "[2,1]" || EncodeClientHostRuleIds(nil) != "null" {
		t.Error("EncodeClientHostRuleIds is wrong")
	}
}

func TestEffectiveWireguardDeviceLimit(t *testing.T) {
	if EffectiveWireguardDeviceLimit(0) != DefaultWireguardDeviceLimit || EffectiveWireguardDeviceLimit(-1) != DefaultWireguardDeviceLimit ||
		EffectiveWireguardDeviceLimit(1) != 1 {
		t.Fatal("EffectiveWireguardDeviceLimit is wrong")
	}
}

func TestNormalizeLegacyClientFieldsParsesOrDrops(t *testing.T) {
	key := legacyClientIntFields[0]
	obj := map[string]any{key: " 1 2 "}
	if !NormalizeLegacyClientFields(obj) || obj[key] != int64(12) {
		t.Fatalf("number: %v", obj)
	}
	obj = map[string]any{key: "soon"}
	changed := NormalizeLegacyClientFields(obj)
	if _, has := obj[key]; !changed || has {
		t.Fatalf("junk must be dropped: %v", obj)
	}
}

func TestKeepAliveScanInt(t *testing.T) {
	var v KeepAliveValue = "x"
	if err := v.Scan(int64(0)); err != nil || v != "" {
		t.Fatalf("Scan(0) = %q %v", v, err)
	}
	if err := v.Scan(int64(25)); err != nil || v != "25" {
		t.Fatalf("Scan(25) = %q %v", v, err)
	}
}

func TestClientIpEntriesOrder(t *testing.T) {
	entries := []ClientIpEntry{{IP: "b", Timestamp: 1}, {IP: "a", Timestamp: 1}, {IP: "c", Timestamp: 0}}
	SortClientIpEntries(entries)
	if entries[0].IP != "c" || entries[1].IP != "a" || entries[2].IP != "b" {
		t.Fatalf("order = %v", entries)
	}
	if s, err := MarshalClientIpEntries(nil); err != nil || s != "[]" {
		t.Fatalf("empty = %q %v", s, err)
	}
}
