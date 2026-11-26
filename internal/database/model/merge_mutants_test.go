package model

import (
	"reflect"
	"testing"
)

// Boundary tests written against surviving mutants reported by gremlins (nightly mutation run).

func hasConflict(cs []ClientMergeConflict, field string) bool {
	for _, c := range cs {
		if c.Field == field {
			return true
		}
	}
	return false
}

func setField(r *ClientRecord, name string, v any) {
	f := reflect.ValueOf(r).Elem().FieldByName(name)
	f.Set(reflect.ValueOf(v).Convert(f.Type()))
}

func getField(r *ClientRecord, name string) any {
	return reflect.ValueOf(r).Elem().FieldByName(name).Interface()
}

// Fields where a non-empty incoming value wins when the incoming record is newer
// or the existing value is empty.
func TestMergeClientRecordNewerOrFillFields(t *testing.T) {
	fields := []struct {
		name, conflict string
		a, b           any
		alwaysReported bool
	}{
		{"UUID", "uuid", "u-a", "u-b", true},
		{"Password", "password", "p-a", "p-b", false},
		{"Auth", "auth", "h-a", "h-b", false},
		{"Flow", "flow", "f-a", "f-b", false},
		{"Security", "security", "s-a", "s-b", false},
		{"SubID", "subId", "sub-a", "sub-b", false},
		{"TgID", "tgId", int64(11), int64(22), false},
		{"Reset", "reset", 7, 30, false},
		{"ResetDay", "resetDay", 1, 15, false},
		{"ResetMax", "resetMax", 2, 5, false},
		{"TrafficReset", "trafficReset", "daily", "monthly", false},
		{"TrafficResetDay", "trafficResetDay", 3, 9, false},
		{"Reverse", "reverse", `{"tag":"a"}`, `{"tag":"b"}`, false},
		{"PrivateKey", "privateKey", "k-a", "k-b", false},
		{"PublicKey", "publicKey", "pk-a", "pk-b", false},
		{"PreSharedKey", "preSharedKey", "psk-a", "psk-b", false},
		{"Secret", "secret", "sec-a", "sec-b", false},
		{"AllowedIPs", "allowedIPs", "10.0.0.2/32", "10.0.0.3/32", false},
		{"KeepAlive", "keepAlive", "25", "30-60", false},
		{"ForwardedPorts", "forwardedPorts", "80", "443", false},
		{"Comment", "comment", "c-a", "c-b", false},
		{"Group", "group", "g-a", "g-b", false},
	}
	type scenario struct {
		name       string
		ex, in     bool // value set (a for existing, b for incoming)
		same       bool // incoming carries the existing value
		newer      bool
		wantB      bool
		reportedIf bool
	}
	scenarios := []scenario{
		{"newer replaces", true, true, false, true, true, true},
		{"older keeps", true, true, false, false, false, false},
		{"older fills empty", false, true, false, false, true, true},
		{"empty incoming never replaces", true, false, false, true, false, false},
		{"equal value is no conflict", true, true, true, true, false, false},
	}
	for _, f := range fields {
		for _, s := range scenarios {
			t.Run(f.name+"/"+s.name, func(t *testing.T) {
				ex := ClientRecord{UpdatedAt: 2}
				in := ClientRecord{UpdatedAt: 1}
				if s.newer {
					in.UpdatedAt = 3
				}
				if s.ex {
					setField(&ex, f.name, f.a)
				}
				if s.in {
					v := f.b
					if s.same {
						v = f.a
					}
					setField(&in, f.name, v)
				}
				before := getField(&ex, f.name)
				cs := MergeClientRecord(&ex, &in)
				got := getField(&ex, f.name)
				want := before
				if s.wantB {
					want = getField(&in, f.name)
				}
				if got != want {
					t.Fatalf("%s = %v, want %v", f.name, got, want)
				}
				wantConflict := s.reportedIf || (f.alwaysReported && s.ex && s.in && !s.same)
				if hasConflict(cs, f.conflict) != wantConflict {
					t.Fatalf("conflict %q reported = %v, want %v (%+v)", f.conflict, !wantConflict, wantConflict, cs)
				}
			})
		}
	}
}

// Limits keep the larger value; zero on the existing side means "not set" and is filled.
func TestMergeClientRecordLimitFields(t *testing.T) {
	for _, f := range []struct{ name, conflict string }{
		{"TotalGB", "totalGB"}, {"ExpiryTime", "expiryTime"}, {"LimitIP", "limitIp"}, {"LimitHwid", "limitHwid"},
	} {
		for _, c := range []struct{ ex, in, want int }{
			{0, 5, 5}, {5, 0, 5}, {3, 5, 5}, {5, 3, 5}, {5, 5, 5},
		} {
			for _, newer := range []bool{false, true} {
				ex := ClientRecord{UpdatedAt: 2}
				in := ClientRecord{UpdatedAt: 1}
				if newer {
					in.UpdatedAt = 3
				}
				setField(&ex, f.name, c.ex)
				setField(&in, f.name, c.in)
				cs := MergeClientRecord(&ex, &in)
				got := reflect.ValueOf(getField(&ex, f.name)).Int()
				if got != int64(c.want) {
					t.Fatalf("%s ex=%d in=%d newer=%v: got %d, want %d", f.name, c.ex, c.in, newer, got, c.want)
				}
				if hasConflict(cs, f.conflict) != (c.want != c.ex) {
					t.Fatalf("%s ex=%d in=%d: conflict reported = %v", f.name, c.ex, c.in, !(c.want != c.ex))
				}
			}
		}
	}
}

// Incoming is newer on a later UpdatedAt, or on equal UpdatedAt and a later CreatedAt.
func TestMergeClientRecordNewerTieBreak(t *testing.T) {
	for _, c := range []struct {
		exU, exC, inU, inC int64
		newer              bool
	}{
		{1, 0, 2, 0, true},
		{2, 0, 1, 0, false},
		{1, 1, 1, 2, true},
		{1, 2, 1, 1, false},
		{1, 1, 1, 1, false},
	} {
		ex := ClientRecord{Flow: "a", UpdatedAt: c.exU, CreatedAt: c.exC}
		in := ClientRecord{Flow: "b", UpdatedAt: c.inU, CreatedAt: c.inC}
		MergeClientRecord(&ex, &in)
		if (ex.Flow == "b") != c.newer {
			t.Fatalf("%+v: incoming treated as newer = %v, want %v", c, ex.Flow == "b", c.newer)
		}
	}
}

func TestMergeClientRecordTimestamps(t *testing.T) {
	for _, c := range []struct{ exC, inC, wantC, exU, inU, wantU int64 }{
		{0, 5, 5, 1, 2, 2},
		{3, 5, 3, 2, 1, 2},
		{5, 3, 3, 2, 2, 2},
		{5, 0, 5, 0, 4, 4},
		{5, 5, 5, 4, 0, 4},
	} {
		ex := ClientRecord{CreatedAt: c.exC, UpdatedAt: c.exU}
		in := ClientRecord{CreatedAt: c.inC, UpdatedAt: c.inU}
		MergeClientRecord(&ex, &in)
		if ex.CreatedAt != c.wantC || ex.UpdatedAt != c.wantU {
			t.Fatalf("%+v: got created=%d updated=%d", c, ex.CreatedAt, ex.UpdatedAt)
		}
	}
}

func TestMergeClientRecordEnableOnlyTurnsOn(t *testing.T) {
	ex, in := ClientRecord{Enable: false}, ClientRecord{Enable: true}
	if cs := MergeClientRecord(&ex, &in); !ex.Enable || !hasConflict(cs, "enable") {
		t.Fatalf("disabled + enabled: enable=%v conflicts=%+v", ex.Enable, cs)
	}
	ex, in = ClientRecord{Enable: true, UpdatedAt: 1}, ClientRecord{Enable: false, UpdatedAt: 2}
	if cs := MergeClientRecord(&ex, &in); !ex.Enable || hasConflict(cs, "enable") {
		t.Fatalf("enabled + newer disabled: enable=%v conflicts=%+v", ex.Enable, cs)
	}
}

func TestMergeClientRecordWireguardDevicesAndLimit(t *testing.T) {
	dev := []WireguardDevice{{}}
	for _, c := range []struct {
		name             string
		exDev, inDev     []WireguardDevice
		exLimit, inLimit int
		newer            bool
		wantInDev        bool
		wantLimit        int
	}{
		{"older nil incoming keeps", dev, nil, 2, 0, false, false, 2},
		{"newer nil incoming keeps", dev, nil, 2, 0, true, false, 2},
		{"older fills empty", nil, dev, 0, 3, false, true, 3},
		{"older keeps set", dev, []WireguardDevice{{}, {}}, 2, 3, false, false, 2},
		{"newer replaces", dev, []WireguardDevice{{}, {}}, 2, 3, true, true, 3},
	} {
		ex := ClientRecord{WireguardDevices: c.exDev, WgDeviceLimit: c.exLimit, UpdatedAt: 2}
		in := ClientRecord{WireguardDevices: c.inDev, WgDeviceLimit: c.inLimit, UpdatedAt: 1}
		if c.newer {
			in.UpdatedAt = 3
		}
		MergeClientRecord(&ex, &in)
		wantLen := len(c.exDev)
		if c.wantInDev {
			wantLen = len(c.inDev)
		}
		if len(ex.WireguardDevices) != wantLen || ex.WgDeviceLimit != c.wantLimit {
			t.Fatalf("%s: devices=%d limit=%d, want devices=%d limit=%d", c.name, len(ex.WireguardDevices), ex.WgDeviceLimit, wantLen, c.wantLimit)
		}
	}
}

func TestMergeClientRecordHostRuleIds(t *testing.T) {
	for _, c := range []struct {
		name         string
		ex, in, want []int
		newer        bool
		conflict     bool
	}{
		{"nil incoming keeps", []int{1}, nil, []int{1}, true, false},
		{"same set other order is no conflict", []int{1, 2}, []int{2, 1}, []int{1, 2}, true, false},
		{"newer replaces", []int{1, 2}, []int{1, 3}, []int{1, 3}, true, true},
		{"older keeps", []int{1, 2}, []int{1, 3}, []int{1, 2}, false, false},
		{"older fills nil", nil, []int{3}, []int{3}, false, true},
	} {
		ex := ClientRecord{ClientHostRuleIds: c.ex, UpdatedAt: 2}
		in := ClientRecord{ClientHostRuleIds: c.in, UpdatedAt: 1}
		if c.newer {
			in.UpdatedAt = 3
		}
		cs := MergeClientRecord(&ex, &in)
		if !reflect.DeepEqual(ex.ClientHostRuleIds, c.want) || hasConflict(cs, "clientHostRuleIds") != c.conflict {
			t.Fatalf("%s: ids=%v conflict=%v, want %v conflict=%v", c.name, ex.ClientHostRuleIds, hasConflict(cs, "clientHostRuleIds"), c.want, c.conflict)
		}
	}
}

func TestEqualClientHostRuleIdsIsSetEquality(t *testing.T) {
	for _, c := range []struct {
		a, b []int
		want bool
	}{
		{[]int{1, 2}, []int{2, 1}, true},
		{[]int{1, 2}, []int{1, 3}, false},
		{[]int{1, 2}, []int{1}, false},
		{nil, nil, true},
	} {
		if got := equalClientHostRuleIds(c.a, c.b); got != c.want {
			t.Fatalf("equalClientHostRuleIds(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
