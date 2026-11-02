package tgbot

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Bot text is escaped once, where values enter it (I18nBot params); the client
// card and the deplete report escaped the finished text again, so an inbound
// remark "R&D" showed as "R&amp;amp;D".
func TestClientCardEscapesRemarksOnce(t *testing.T) {
	tb, _ := newScreenTgbot(t, false)
	setTestAdmins(t, 1)
	initReportDB(t)
	seedClientForTgUser(t, 0, "esc@x")
	if err := database.GetDB().Model(&model.Inbound{}).Where("tag = ?", "tg-esc@x").Update("remark", "R&D <eu>").Error; err != nil {
		t.Fatal(err)
	}

	tb.renderPickerOnClient(1, "esc@x", nil)

	sc, ok := tb.screens().get(1)
	if !ok {
		t.Fatal("no screen was tracked")
	}
	if strings.Contains(sc.body, "&amp;amp;") || strings.Contains(sc.body, "&amp;lt;") {
		t.Fatalf("remark escaped twice:\n%s", sc.body)
	}
	if !strings.Contains(sc.body, "R&amp;D &lt;eu&gt;") {
		t.Fatalf("remark not shown escaped once:\n%s", sc.body)
	}
}
