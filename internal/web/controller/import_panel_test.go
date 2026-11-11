package controller

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport/marzbantest"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

func postImportPanel(t *testing.T, a *ServerController, fields map[string]string, files map[string][]byte) (bool, string, service.PanelImportReport) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := mw.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		part, err := mw.CreateFormFile(name, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { session.SetAPIAuthUser(c, &model.User{Id: 7}) })
	engine.POST("/panel/api/server/importPanel", a.importPanel)
	req := httptest.NewRequest(http.MethodPost, "/panel/api/server/importPanel", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	var env struct {
		Success bool                      `json:"success"`
		Msg     string                    `json:"msg"`
		Obj     service.PanelImportReport `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	return env.Success, env.Msg, env.Obj
}

func TestImportPanelUpload(t *testing.T) {
	newHostTestDB(t)
	t.Setenv("XUI_BACKUP_FOLDER", t.TempDir())
	dbPath, cfg := marzbantest.Build(t, 24000)
	source, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"db": source, "config": cfg}
	a := &ServerController{}
	inbounds := func() (n int64, owner int) {
		db := database.GetDB()
		db.Model(&model.Inbound{}).Count(&n)
		db.Model(&model.Inbound{}).Limit(1).Pluck("user_id", &owner)
		return n, owner
	}

	ok, msg, report := postImportPanel(t, a, map[string]string{"source": "marzban"}, files)
	if !ok || report.Applied || len(report.Inbounds) != 9 || report.Clients != 8 {
		t.Fatalf("check: ok %v (%s), report %+v", ok, msg, report)
	}
	if n, _ := inbounds(); n != 0 {
		t.Fatalf("an upload without apply=true created %d inbounds", n)
	}

	ok, msg, report = postImportPanel(t, a, map[string]string{"source": "marzban", "apply": "true"}, files)
	if !ok || !report.Applied || report.Backup == "" {
		t.Fatalf("import: ok %v (%s), report %+v", ok, msg, report)
	}
	if n, owner := inbounds(); n != 9 || owner != 7 {
		t.Fatalf("%d inbounds owned by user %d, want 9 owned by the caller (7)", n, owner)
	}
	if !a.xrayService.IsNeedRestartAndSetFalse() {
		t.Error("the import did not ask Xray to load the new inbounds")
	}

	// The panel now has inbounds: a second import must be refused unless merge is sent.
	ok, msg, _ = postImportPanel(t, a, map[string]string{"source": "marzban", "apply": "true"}, files)
	if ok || !strings.Contains(msg, "already has inbounds") {
		t.Errorf("second import without merge: ok %v, msg %q", ok, msg)
	}
	ok, msg, _ = postImportPanel(t, a, map[string]string{"source": "x-ui"}, map[string][]byte{"db": source})
	if ok || !strings.Contains(msg, "this is a Marzban database") {
		t.Errorf("a Marzban file sent as x-ui: ok %v, msg %q", ok, msg)
	}
}
