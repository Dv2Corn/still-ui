package service

import (
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

func gateHwid(t *testing.T, subID, hwid string) HwidGateResult {
	t.Helper()
	res, err := (&ClientService{}).EnforceHwidForSubID(subID, HwidRequest{Hwid: hwid})
	if err != nil {
		t.Fatalf("gate %q for %q: %v", hwid, subID, err)
	}
	return res
}

func countHwidRows(t *testing.T, subID string) int64 {
	t.Helper()
	var rows int64
	if err := database.GetDB().Model(&model.ClientHwid{}).Where("sub_id = ?", subID).Count(&rows).Error; err != nil {
		t.Fatalf("count client_hwids of %q: %v", subID, err)
	}
	return rows
}

func ageHwidRows(t *testing.T, subID string, days int) {
	t.Helper()
	seen := time.Now().AddDate(0, 0, -days).UnixMilli()
	if err := database.GetDB().Model(&model.ClientHwid{}).Where("sub_id = ?", subID).UpdateColumn("last_seen", seen).Error; err != nil {
		t.Fatalf("age client_hwids of %q: %v", subID, err)
	}
}

// Under "allow" an app that sends no device id passes without taking a slot; "refuse" turns it away.
func TestHwidGateNoIdAllowAndRefuse(t *testing.T) {
	initClientHwidTestDB(t)
	rec := seedHwidClient(t, 1)
	setHwidSetting(t, "subHwidNoIdPolicy", HwidNoIdAllow)

	for range 2 {
		res := gateHwid(t, rec.SubID, "")
		if want := (HwidGateResult{Allowed: true, Active: true, Limit: 1}); res != want {
			t.Fatalf("no id under allow = %+v, want %+v", res, want)
		}
	}
	if rows := countHwidRows(t, rec.SubID); rows != 0 {
		t.Fatalf("an app without an id took %d slot(s) under allow, want 0", rows)
	}
	if res := gateHwid(t, rec.SubID, "device-one"); !res.Allowed || res.Registered != 1 {
		t.Fatalf("the only slot must still be free for a device with an id: %+v", res)
	}

	setHwidSetting(t, "subHwidNoIdPolicy", HwidNoIdRefuse)
	res := gateHwid(t, rec.SubID, "")
	if want := (HwidGateResult{Active: true, NotSupported: true, Limit: 1}); res != want {
		t.Fatalf("no id under refuse = %+v, want %+v", res, want)
	}
	// An id shorter than the minimum is no id.
	if res := gateHwid(t, rec.SubID, "abc"); res.Allowed || !res.NotSupported {
		t.Fatalf("a too-short id under refuse = %+v, want refused as not supported", res)
	}
}

// The default: every app without a device id shares one slot of the subscription, so leaving
// the header out does not get a device past the limit.
func TestHwidGateNoIdSharedSlot(t *testing.T) {
	initClientHwidTestDB(t)
	rec := seedHwidClient(t, 2)
	svc := &ClientService{}

	for range 3 {
		res := gateHwid(t, rec.SubID, "")
		if want := (HwidGateResult{Allowed: true, Active: true, Limit: 2, Registered: 1}); res != want {
			t.Fatalf("no id under the shared slot = %+v, want %+v", res, want)
		}
	}
	if rows := countHwidRows(t, rec.SubID); rows != 1 {
		t.Fatalf("apps without an id hold %d rows, want one shared slot", rows)
	}
	list, err := svc.ListClientHwids(rec.Email)
	if err != nil || len(list) != 1 || !list[0].NoID || list[0].Fingerprint != "" {
		t.Fatalf("device list = %+v err=%v, want one entry marked as the shared no-id slot", list, err)
	}

	if res := gateHwid(t, rec.SubID, "device-one"); !res.Allowed || res.Registered != 2 || !res.LimitReached {
		t.Fatalf("a device with an id takes the second slot: %+v", res)
	}
	if res := gateHwid(t, rec.SubID, "device-two"); res.Allowed || !res.MaxDevicesReached {
		t.Fatalf("a third device = %+v, want refused: the shared slot counts", res)
	}
	if res := gateHwid(t, rec.SubID, ""); !res.Allowed {
		t.Fatalf("the registered shared slot must keep passing at a full limit: %+v", res)
	}
}

// With every slot taken by real devices, an app without an id is refused as over the limit.
func TestHwidGateNoIdSharedSlotRefusedAtFullLimit(t *testing.T) {
	initClientHwidTestDB(t)
	rec := seedHwidClient(t, 1)
	if res := gateHwid(t, rec.SubID, "device-one"); !res.Allowed {
		t.Fatalf("register the first device: %+v", res)
	}
	res := gateHwid(t, rec.SubID, "")
	if want := (HwidGateResult{Active: true, MaxDevicesReached: true, LimitReached: true, Limit: 1, Registered: 1}); res != want {
		t.Fatalf("no id at a full limit = %+v, want %+v", res, want)
	}
	if rows := countHwidRows(t, rec.SubID); rows != 1 {
		t.Fatalf("a refused no-id fetch left %d rows, want only the registered device", rows)
	}
}

// Changing the policy must not leave a slot held by nobody, nor count one that is not there.
func TestHwidNoIdPolicySwitchKeepsRegistryConsistent(t *testing.T) {
	initClientHwidTestDB(t)
	rec := seedHwidClient(t, 2)
	svc := &ClientService{}
	registered := func() int {
		t.Helper()
		status, _, err := svc.HwidSlotStatusForSubID(rec.SubID)
		if err != nil {
			t.Fatalf("slot status: %v", err)
		}
		return status.Registered
	}

	gateHwid(t, rec.SubID, "")
	gateHwid(t, rec.SubID, "device-one")
	if got := registered(); got != 2 {
		t.Fatalf("registered under shared = %d, want the shared slot and one device", got)
	}

	for _, policy := range []string{HwidNoIdAllow, HwidNoIdRefuse} {
		setHwidSetting(t, "subHwidNoIdPolicy", policy)
		if got := registered(); got != 1 {
			t.Fatalf("registered under %s = %d, want 1: the shared slot no longer counts", policy, got)
		}
		if list, err := svc.ListClientHwids(rec.Email); err != nil || len(list) != 1 || list[0].NoID {
			t.Fatalf("device list under %s = %+v err=%v, want only the real device", policy, list, err)
		}
	}
	// The slot the shared row held is free for a real device, and the row goes with the fetch.
	if res := gateHwid(t, rec.SubID, "device-two"); !res.Allowed || res.Registered != 2 {
		t.Fatalf("second device after leaving shared = %+v, want admitted as the second of two", res)
	}
	if rows := countHwidRows(t, rec.SubID); rows != 2 {
		t.Fatalf("client_hwids rows = %d, want the two real devices and no leftover shared row", rows)
	}

	// Back to shared with both slots taken: the apps without an id have to wait for a free one.
	setHwidSetting(t, "subHwidNoIdPolicy", HwidNoIdShared)
	if res := gateHwid(t, rec.SubID, ""); res.Allowed || !res.MaxDevicesReached {
		t.Fatalf("no id back under shared at a full limit = %+v, want refused", res)
	}
	if err := svc.ClearClientHwids(rec.Email); err != nil {
		t.Fatalf("clear devices: %v", err)
	}
	if res := gateHwid(t, rec.SubID, ""); !res.Allowed || res.Registered != 1 {
		t.Fatalf("no id under shared with a free slot = %+v, want it registered", res)
	}

	// A stored value nobody can have saved reads as the default, never as "let everyone in".
	setHwidSetting(t, "subHwidNoIdPolicy", "bogus")
	if got := registered(); got != 1 {
		t.Fatalf("registered under an unknown policy = %d, want the shared slot counted", got)
	}
}

func TestHwidGatePanelDefaultLimit(t *testing.T) {
	initClientHwidTestDB(t)
	db := database.GetDB()
	rec := seedHwidClient(t, 0)
	own := &model.ClientRecord{Email: "own@example.com", SubID: "sub-own", UUID: "22222222-2222-4333-8444-555555555555", Enable: true, LimitHwid: 2}
	if err := db.Create(own).Error; err != nil {
		t.Fatalf("seed client with its own limit: %v", err)
	}
	setHwidSetting(t, "subHwidDefaultLimit", "1")

	if res := gateHwid(t, rec.SubID, "device-one"); !res.Allowed || !res.Active || res.Limit != 1 {
		t.Fatalf("first device under the panel default = %+v, want allowed with limit 1", res)
	}
	if res := gateHwid(t, rec.SubID, "device-two"); res.Allowed || !res.MaxDevicesReached || res.Limit != 1 {
		t.Fatalf("second device under the panel default = %+v, want refused with limit 1", res)
	}

	// A client's own limit wins over the panel default.
	for _, hwid := range []string{"device-one", "device-two"} {
		if res := gateHwid(t, own.SubID, hwid); !res.Allowed || res.Limit != 2 {
			t.Fatalf("%s under the client's own limit = %+v, want allowed with limit 2", hwid, res)
		}
	}
	if res := gateHwid(t, own.SubID, "device-three"); res.Allowed || res.Limit != 2 {
		t.Fatalf("third device under the client's own limit = %+v, want refused with limit 2", res)
	}

	// The default must not make a subId nobody owns a limited subscription with stored rows.
	if res := gateHwid(t, "never-issued", "device-ghost"); res != (HwidGateResult{Allowed: true}) {
		t.Fatalf("unknown subId under a panel default = %+v, want a plain pass", res)
	}
	if rows := countHwidRows(t, "never-issued"); rows != 0 {
		t.Fatalf("client_hwids rows for an unknown subId = %d, want 0", rows)
	}
	for _, policy := range []string{HwidNoIdShared, HwidNoIdRefuse} {
		setHwidSetting(t, "subHwidNoIdPolicy", policy)
		if res := gateHwid(t, "never-issued", ""); res != (HwidGateResult{Allowed: true}) {
			t.Fatalf("unknown subId without a device id under %s = %+v, want a plain pass", policy, res)
		}
	}
	if rows := countHwidRows(t, "never-issued"); rows != 0 {
		t.Fatalf("a shared slot was stored for an unknown subId: %d rows", rows)
	}

	status, found, err := (&ClientService{}).HwidSlotStatusForSubID(rec.SubID)
	if err != nil || !found {
		t.Fatalf("slot status: found=%v err=%v", found, err)
	}
	if want := (HwidSlotStatus{Active: true, Limit: 1, Registered: 1, Remaining: 0, Full: true}); status != want {
		t.Fatalf("slot status under the panel default = %+v, want %+v", status, want)
	}

	setHwidSetting(t, "subHwidDefaultLimit", "0")
	if res := gateHwid(t, rec.SubID, "device-two"); res != (HwidGateResult{Allowed: true}) {
		t.Fatalf("default switched off = %+v, want a plain pass", res)
	}
}

// A saved panel default trims a subscription over it to its most recently seen devices. Left
// alone, it enforced nothing there until an unrelated client edit deleted the extra devices.
func TestHwidPanelDefaultAppliesWhenSaved(t *testing.T) {
	initClientHwidTestDB(t)
	db := database.GetDB()
	svc := &ClientService{}
	settings := &SettingService{}
	saveDefault := func(limit int) {
		t.Helper()
		all, err := settings.GetAllSetting()
		if err != nil {
			t.Fatalf("GetAllSetting: %v", err)
		}
		all.SubHwidDefaultLimit = limit
		if err := settings.UpdateAllSetting(all, SecretClears{}); err != nil {
			t.Fatalf("save default limit %d: %v", limit, err)
		}
	}
	hashes := func(subID string) []string {
		t.Helper()
		var got []string
		if err := db.Model(&model.ClientHwid{}).Where("sub_id = ?", subID).Order("last_seen").Pluck("hwid_hash", &got).Error; err != nil {
			t.Fatalf("read client_hwids of %q: %v", subID, err)
		}
		return got
	}

	over := seedHwidClient(t, 0)
	own := &model.ClientRecord{Email: "own@example.com", SubID: "sub-own", UUID: "22222222-2222-4333-8444-555555555555", Enable: true, LimitHwid: 4}
	under := &model.ClientRecord{Email: "under@example.com", SubID: "sub-under", UUID: "33333333-2222-4333-8444-555555555555", Enable: true}
	for _, rec := range []*model.ClientRecord{own, under} {
		if err := db.Create(rec).Error; err != nil {
			t.Fatalf("seed client %s: %v", rec.Email, err)
		}
	}
	seedHwids(t, db, over.SubID, 5)
	seedHwids(t, db, own.SubID, 4)
	seedHwids(t, db, under.SubID, 2)

	saveDefault(0)
	if got := len(hashes(over.SubID)); got != 5 {
		t.Fatalf("devices after saving no default = %d, want all 5 kept", got)
	}

	saveDefault(3)
	want := []string{over.SubID + "-hash-2", over.SubID + "-hash-3", over.SubID + "-hash-4"}
	if got := hashes(over.SubID); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("devices under the saved default = %q, want the three seen last %q", got, want)
	}
	if got := len(hashes(own.SubID)); got != 4 {
		t.Fatalf("a client with its own limit of 4 was trimmed to %d by the panel default", got)
	}
	if got := len(hashes(under.SubID)); got != 2 {
		t.Fatalf("a subscription under the default lost devices: %d left of 2", got)
	}
	status, _, err := svc.HwidSlotStatusForSubID(over.SubID)
	if err != nil || status.Registered != 3 || status.Limit != 3 {
		t.Fatalf("slot status = %+v err=%v, want 3 of 3", status, err)
	}

	// Saving a client without touching its limit deletes nothing further.
	if err := svc.setClientLimitHwidByEmail(over.Email, 0); err != nil {
		t.Fatalf("client update: %v", err)
	}
	if got := len(hashes(over.SubID)); got != 3 {
		t.Fatalf("devices after an unrelated client update = %d, want 3", got)
	}
}

func TestHwidExpiryFreesSlot(t *testing.T) {
	initClientHwidTestDB(t)
	rec := seedHwidClient(t, 1)
	svc := &ClientService{}

	if res := gateHwid(t, rec.SubID, "device-one"); !res.Allowed {
		t.Fatalf("register the first device: %+v", res)
	}
	ageHwidRows(t, rec.SubID, 29)
	if res := gateHwid(t, rec.SubID, "device-two"); res.Allowed {
		t.Fatalf("a device seen 29 days ago must still hold its slot: %+v", res)
	}

	ageHwidRows(t, rec.SubID, 31)
	status, _, err := svc.HwidSlotStatusForSubID(rec.SubID)
	if err != nil || status.Registered != 0 || status.Full {
		t.Fatalf("slot status must not count an expired device: %+v err=%v", status, err)
	}
	if list, err := svc.ListClientHwids(rec.Email); err != nil || len(list) != 0 {
		t.Fatalf("device list must not show an expired device: %+v err=%v", list, err)
	}
	if res := gateHwid(t, rec.SubID, "device-two"); !res.Allowed || res.Registered != 1 {
		t.Fatalf("a device unseen for 31 days must free its slot: %+v", res)
	}
	if rows := countHwidRows(t, rec.SubID); rows != 1 {
		t.Fatalf("client_hwids rows after expiry = %d, want only the new device", rows)
	}
	// The expired device is a new device now, and the slot is taken.
	if res := gateHwid(t, rec.SubID, "device-one"); res.Allowed || !res.MaxDevicesReached {
		t.Fatalf("the expired device coming back = %+v, want refused", res)
	}

	setHwidSetting(t, "subHwidExpireDays", "0")
	ageHwidRows(t, rec.SubID, 4000)
	if res := gateHwid(t, rec.SubID, "device-one"); res.Allowed {
		t.Fatalf("with expiry off an old device keeps its slot: %+v", res)
	}
}

func TestPurgeExpiredClientHwids(t *testing.T) {
	initClientHwidTestDB(t)
	db := database.GetDB()
	now := time.Now()
	rows := []model.ClientHwid{
		{SubID: "sub-a", HwidHash: "fresh", FirstSeen: 1, LastSeen: now.AddDate(0, 0, -29).UnixMilli()},
		{SubID: "sub-a", HwidHash: "stale", FirstSeen: 1, LastSeen: now.AddDate(0, 0, -31).UnixMilli()},
		{SubID: "sub-b", HwidHash: "stale", FirstSeen: 1, LastSeen: now.AddDate(0, 0, -400).UnixMilli()},
		{SubID: "sub-c", HwidHash: noIDHwidHash, FirstSeen: 1, LastSeen: now.UnixMilli()},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed client_hwids: %v", err)
	}
	svc := &ClientService{}

	setHwidSetting(t, "subHwidExpireDays", "0")
	if err := svc.PurgeExpiredClientHwids(); err != nil {
		t.Fatalf("purge with expiry off: %v", err)
	}
	if got := countHwidRows(t, "sub-a") + countHwidRows(t, "sub-b") + countHwidRows(t, "sub-c"); got != 4 {
		t.Fatalf("rows with expiry off = %d, want all 4 kept", got)
	}

	setHwidSetting(t, "subHwidExpireDays", "30")
	if err := svc.PurgeExpiredClientHwids(); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var left []string
	if err := db.Model(&model.ClientHwid{}).Order("id").Pluck("hwid_hash", &left).Error; err != nil {
		t.Fatalf("read client_hwids: %v", err)
	}
	if len(left) != 2 || left[0] != "fresh" || left[1] != noIDHwidHash {
		t.Fatalf("rows after purge = %q, want the fresh device and the shared slot", left)
	}

	// Once apps without an id are no longer counted, the slot they shared is nobody's.
	setHwidSetting(t, "subHwidNoIdPolicy", HwidNoIdAllow)
	if err := svc.PurgeExpiredClientHwids(); err != nil {
		t.Fatalf("purge under allow: %v", err)
	}
	if rows := countHwidRows(t, "sub-c"); rows != 0 {
		t.Fatalf("shared slot rows left under allow = %d, want 0", rows)
	}
}

func TestValidateSubHwidSettings(t *testing.T) {
	long := strings.Repeat("я", SubHwidNoticeMaxLength+1)
	cases := []struct {
		name       string
		in         entity.AllSetting
		wantErr    string
		wantPolicy string
	}{
		{name: "blank policy becomes the shared slot", in: entity.AllSetting{}, wantPolicy: HwidNoIdShared},
		{name: "allow kept", in: entity.AllSetting{SubHwidNoIdPolicy: HwidNoIdAllow}, wantPolicy: HwidNoIdAllow},
		{name: "refuse kept", in: entity.AllSetting{SubHwidNoIdPolicy: HwidNoIdRefuse, SubHwidDefaultLimit: 5, SubHwidExpireDays: 30}, wantPolicy: HwidNoIdRefuse},
		{name: "unknown policy", in: entity.AllSetting{SubHwidNoIdPolicy: "block"}, wantErr: "device limit: apps without a device id must be set to shared, allow or refuse"},
		{name: "negative default limit", in: entity.AllSetting{SubHwidDefaultLimit: -1}, wantErr: "default device limit must be 0 to 1000"},
		{name: "default limit too large", in: entity.AllSetting{SubHwidDefaultLimit: 1001}, wantErr: "default device limit must be 0 to 1000"},
		{name: "negative expiry", in: entity.AllSetting{SubHwidExpireDays: -1}, wantErr: "devices expire after 0 to 3650 days"},
		{name: "expiry too large", in: entity.AllSetting{SubHwidExpireDays: 3651}, wantErr: "devices expire after 0 to 3650 days"},
		{name: "limit notice too long", in: entity.AllSetting{SubHwidLimitNotice: long}, wantErr: "device limit notice must not exceed 256 characters"},
		{name: "no-id notice too long", in: entity.AllSetting{SubHwidNoIdNotice: long}, wantErr: "device limit notice must not exceed 256 characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSubHwidSettings(&tc.in)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.in.SubHwidNoIdPolicy != tc.wantPolicy {
				t.Fatalf("policy = %q, want %q", tc.in.SubHwidNoIdPolicy, tc.wantPolicy)
			}
		})
	}
}
