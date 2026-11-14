package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// The leak alert's threshold and window are saved with the other settings; a value the
// alert cannot use is refused and the stored one stays.
func TestUpdateSettingSavesAndValidatesSubLeakAlert(t *testing.T) {
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	settings := service.SettingService{}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewSettingController(router.Group("/panel/api"))

	post := func(change map[string]any) string {
		t.Helper()
		all, err := settings.GetAllSetting()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(all)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		for k, v := range change {
			body[k] = v
		}
		payload, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/panel/api/setting/update", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		return resp.Body.String()
	}
	stored := func() (service.SubLeakAlert, bool) {
		t.Helper()
		return settings.GetSubLeakAlert()
	}

	if got := post(map[string]any{"tgBotEnable": true, "subLeakNetworks": 10, "subLeakWindowHours": 48}); !strings.Contains(got, `"success":true`) {
		t.Fatalf("saving 10 networks in 48 h was refused: %s", got)
	}
	if cfg, on := stored(); !on || cfg != (service.SubLeakAlert{Networks: 10, Window: 48 * time.Hour}) {
		t.Fatalf("stored alert = %+v on=%v, want on with 10 networks in 48 h (new panels tick sub.leak)", cfg, on)
	}
	for _, bad := range []map[string]any{
		{"subLeakNetworks": 1},
		{"subLeakNetworks": 65},
		{"subLeakWindowHours": 0},
		{"subLeakWindowHours": 169},
	} {
		if got := post(bad); !strings.Contains(got, `"success":false`) {
			t.Errorf("%v was accepted: %s", bad, got)
		}
	}
	if cfg, _ := stored(); cfg != (service.SubLeakAlert{Networks: 10, Window: 48 * time.Hour}) {
		t.Fatalf("a refused save changed the alert to %+v", cfg)
	}
	if got := post(map[string]any{"tgEnabledEvents": "login.attempt"}); !strings.Contains(got, `"success":true`) {
		t.Fatalf("unticking the event was refused: %s", got)
	}
	if _, on := stored(); on {
		t.Fatal("the alert stays on with sub.leak unticked")
	}
}
