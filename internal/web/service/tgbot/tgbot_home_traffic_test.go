package tgbot

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// The admin home screen put all inbound traffic under upload and 0 under
// download; it must show the real up and down split.
func TestHomeSummarySplitsUploadAndDownload(t *testing.T) {
	tb := initReportDB(t)
	localizeWithRealBundle(t)
	ib := &model.Inbound{UserId: 1, Tag: "home-t", Port: 9443, Protocol: model.VLESS, Enable: true,
		Settings: `{"clients":[]}`, Up: 3 << 30, Down: 5 << 30}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	// Set the counters explicitly in case creation ignores them.
	if err := database.GetDB().Model(ib).Updates(map[string]any{"up": int64(3 << 30), "down": int64(5 << 30)}).Error; err != nil {
		t.Fatal(err)
	}
	got := tb.homeSummary()
	line := tb.I18nBot("tgbot.messages.traffic", "Total=="+common.FormatTraffic(8<<30),
		"Upload=="+common.FormatTraffic(3<<30), "Download=="+common.FormatTraffic(5<<30))
	if !strings.Contains(got, line) {
		t.Fatalf("home traffic line = %q, want %q", got, line)
	}
}
