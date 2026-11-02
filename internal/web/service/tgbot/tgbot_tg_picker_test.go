package tgbot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// "Set Telegram User" on a client card must offer the user picker: every
// caller passed withPicker=false, so neither the select button nor the native
// picker keyboard was ever sent.
func TestSetTelegramUserOffersThePicker(t *testing.T) {
	tb, calls := newScreenTgbot(t, false)
	setTestAdmins(t, 1)
	initReportDB(t)
	seedClientForTgUser(t, 0, "pick@x")

	tb.answerCallback(&telego.CallbackQuery{
		ID: "q1", From: telego.User{ID: 1}, Data: "tg_user pick@x",
		Message: &telego.Message{Chat: telego.Chat{ID: 1}},
	}, true)

	sc, ok := tb.screens().get(1)
	if !ok {
		t.Fatal("no screen was tracked")
	}
	markup, _ := json.Marshal(sc.markup)
	if !strings.Contains(string(markup), "tgid_pick pick@x") {
		t.Errorf("the Telegram user screen has no select button: %s", markup)
	}
	picker := false
	for _, c := range calls() {
		raw, _ := json.Marshal(c.Payload)
		if c.Method == "sendMessage" && strings.Contains(string(raw), "request_users") {
			picker = true
		}
	}
	if !picker {
		t.Error("the native user picker keyboard was never sent")
	}
}

// The user picked after "Select" arrives while the chat still waits in
// awaiting_tg_pick; the state switch had no case for it, so the pick was lost.
func TestPickedTelegramUserIsSavedWhileAwaitingThePick(t *testing.T) {
	tb, _ := newScreenTgbot(t, false)
	setTestAdmins(t, 1)
	initReportDB(t)
	seedClientForTgUser(t, 0, "pick@x")
	// A VLESS client needs its id for the settings rewrite the save does.
	if err := database.GetDB().Model(&model.Inbound{}).Where("tag = ?", "tg-pick@x").
		Update("settings", `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"pick@x","subId":"sub-pick@x","enable":true}]}`).Error; err != nil {
		t.Fatal(err)
	}
	var traffic xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", "pick@x").First(&traffic).Error; err != nil {
		t.Fatal(err)
	}

	msg := telego.Message{
		Chat: telego.Chat{ID: 1}, From: &telego.User{ID: 1},
		UsersShared: &telego.UsersShared{RequestID: traffic.Id, Users: []telego.SharedUser{{UserID: 777}}},
	}
	userStateMgr.set(messageActor(msg), "awaiting_tg_pick")
	t.Cleanup(func() { userStateMgr.clear(messageActor(msg)) })

	tb.handleMessage(msg)

	_, client, err := tb.inboundService.GetClientByEmail("pick@x")
	if err != nil || client == nil {
		t.Fatalf("load client: %v", err)
	}
	if client.TgID != 777 {
		t.Fatalf("picked user not saved: tgId = %d, want 777", client.TgID)
	}
	if st, ok := userStateMgr.get(messageActor(msg)); ok {
		t.Errorf("chat still waits in %q after the pick", st)
	}
}
