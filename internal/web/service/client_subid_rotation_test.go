package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Rotating a subId ends the Telegram binding it could claim on every write path, not only
// the client form: the per-inbound client update and a whole-inbound client sync.
func TestSubIDRotationEndsTheTelegramBindingOnEveryPath(t *testing.T) {
	const email, tg = "owner@x", int64(7777)
	seed := func(t *testing.T) (*ClientService, *model.Inbound) {
		t.Helper()
		setupBulkDB(t)
		svc := &ClientService{}
		source := []model.Client{{Email: email, ID: "aaaaaaaa-0000-4000-8000-000000000001", SubID: "sub-old", TgID: tg, Enable: true}}
		ib := mkInbound(t, 22031, model.VLESS, clientsSettings(t, source))
		if err := svc.SyncInbound(nil, ib.Id, source); err != nil {
			t.Fatalf("seed linkage: %v", err)
		}
		if got := lookupClientRecord(t, email).TgID; got != tg {
			t.Fatalf("seeded tg_id = %d, want %d", got, tg)
		}
		return svc, ib
	}
	edited := func(subID string, tgID int64) []model.Client {
		return []model.Client{{Email: email, ID: "aaaaaaaa-0000-4000-8000-000000000001", SubID: subID, TgID: tgID, Enable: true}}
	}
	for _, tc := range []struct {
		name     string
		subID    string
		tgID     int64
		wantTgID int64
	}{
		{"rotated, bound tgId sent back", "sub-new", tg, 0},
		{"rotated, a new tgId named", "sub-new", 9999, 9999},
		{"same subId", "sub-old", tg, tg},
	} {
		t.Run("per-inbound client update: "+tc.name, func(t *testing.T) {
			svc, ib := seed(t)
			if _, err := svc.UpdateInboundClient(&InboundService{}, &model.Inbound{Id: ib.Id, Settings: clientsSettings(t, edited(tc.subID, tc.tgID))}, email); err != nil {
				t.Fatalf("UpdateInboundClient: %v", err)
			}
			if got := lookupClientRecord(t, email).TgID; got != tc.wantTgID {
				t.Fatalf("record tg_id = %d, want %d", got, tc.wantTgID)
			}
			// The stored entry too: the next sync of this inbound reads it back into the record.
			var stored model.Inbound
			if err := database.GetDB().First(&stored, ib.Id).Error; err != nil {
				t.Fatal(err)
			}
			clients, err := ParseInboundSettingsClients(stored.Settings)
			if err != nil || len(clients) != 1 || clients[0].TgID != tc.wantTgID {
				t.Fatalf("stored entry = %+v, %v; want tgId %d", clients, err, tc.wantTgID)
			}
		})
		t.Run("inbound client sync: "+tc.name, func(t *testing.T) {
			svc, ib := seed(t)
			if err := svc.SyncInbound(nil, ib.Id, edited(tc.subID, tc.tgID)); err != nil {
				t.Fatalf("SyncInbound: %v", err)
			}
			if got := lookupClientRecord(t, email).TgID; got != tc.wantTgID {
				t.Fatalf("record tg_id = %d, want %d", got, tc.wantTgID)
			}
		})
	}
}
