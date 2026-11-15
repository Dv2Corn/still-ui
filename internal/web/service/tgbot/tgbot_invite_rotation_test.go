package tgbot

import (
	"encoding/base64"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// A binding claimed with a leaked subId must not survive the admin rotating that subId: the edit
// form sends the claimed tgId back, and keeping it would hand the leaker the new subscription.
func TestSubIDRotationEndsTheInviteBinding(t *testing.T) {
	const leaker = int64(7777)
	for _, tc := range []struct {
		name       string
		subID      string
		tgID       int64
		wantTgID   int64
		wantLeaker userLevel
	}{
		{"rotated, claimed tgId sent back", "sub-rotated", leaker, 0, levelStranger},
		{"rotated, admin names a new tgId", "sub-rotated", 9999, 9999, levelStranger},
		{"same subId", "sub-owned", leaker, leaker, levelClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tb, _ := newLinksCallbackTgbot(t, ownerMail)
			clients, inbounds := &service.ClientService{}, &service.InboundService{}
			recs, err := clients.GetRecordsBySubID("sub-owned")
			if err != nil || len(recs) != 1 {
				t.Fatalf("seeded records = %v, %v", recs, err)
			}
			id := recs[0].Id
			edit := func(subID string, tgID int64) {
				t.Helper()
				if _, err := clients.Update(inbounds, id, model.Client{Email: ownerMail, SubID: subID, TgID: tgID, Enable: true}, 0); err != nil {
					t.Fatalf("update client: %v", err)
				}
			}
			edit("sub-owned", 0)
			if out := tb.claimInvite(leaker, leaker, base64.RawURLEncoding.EncodeToString([]byte("sub-owned"))); out != inviteBindable {
				t.Fatalf("claim = %d, want bound", out)
			}

			edit(tc.subID, tc.tgID)

			rec, err := clients.GetByID(id)
			if err != nil {
				t.Fatalf("load record: %v", err)
			}
			if rec.TgID != tc.wantTgID {
				t.Fatalf("tg_id after the edit = %d, want %d", rec.TgID, tc.wantTgID)
			}
			if got := tb.levelOf(leaker); got != tc.wantLeaker {
				t.Fatalf("leaker level after the edit = %v, want %v", got, tc.wantLeaker)
			}
		})
	}
}
