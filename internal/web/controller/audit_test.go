package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// auditWebServer gives the server controller a cron that is never started.
type auditWebServer struct{ cron *cron.Cron }

func (s auditWebServer) GetCron() *cron.Cron     { return s.cron }
func (s auditWebServer) GetCtx() context.Context { return context.Background() }
func (s auditWebServer) GetWSHub() any           { return nil }

// newAuditTestEngine serves the production /panel/api router, so the requests below pass the
// same middleware chain and handlers as on a live panel.
func newAuditTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	dbtest.InitDB(t, filepath.Join(dbDir, "x-ui.db"))
	previous := global.GetWebServer()
	global.SetWebServer(auditWebServer{cron: cron.New()})
	t.Cleanup(func() { global.SetWebServer(previous) })
	engine := gin.New()
	engine.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("audit-test-secret"))))
	NewAPIController(engine.Group("/"))
	return engine
}

func seedAuditToken(t *testing.T, name, plaintext string) int {
	t.Helper()
	row := model.ApiToken{Name: name, Token: crypto.HashTokenSHA256(plaintext), Enabled: true, Scope: model.ApiScopeAdmin}
	if err := database.GetDB().Create(&row).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return row.Id
}

func auditRows(t *testing.T) []model.AuditLog {
	t.Helper()
	var rows []model.AuditLog
	if err := database.GetDB().Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return rows
}

func serveAudit(engine *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func TestAuditRecordsStateChangesButNotReads(t *testing.T) {
	engine := newAuditTestEngine(t)
	const token = "audit-test-token"
	seedAuditToken(t, "ops", token)
	victim := seedAuditToken(t, "old-script", "another-token")

	if w := serveAudit(engine, http.MethodGet, "/panel/api/setting/apiTokens", token, ""); w.Code != http.StatusOK {
		t.Fatalf("GET apiTokens status = %d", w.Code)
	}
	// Reading the settings is a POST on the exempt list.
	if w := serveAudit(engine, http.MethodPost, "/panel/api/setting/all", token, ""); w.Code != http.StatusOK {
		t.Fatalf("POST setting/all status = %d", w.Code)
	}
	if rows := auditRows(t); len(rows) != 0 {
		t.Fatalf("a read was recorded: %+v", rows)
	}

	path := "/panel/api/setting/apiTokens/delete/" + strconv.Itoa(victim)
	if w := serveAudit(engine, http.MethodPost, path, token, `{"expectedScope":"admin"}`); w.Code != http.StatusOK {
		t.Fatalf("POST delete status = %d", w.Code)
	}
	rows := auditRows(t)
	if len(rows) != 1 {
		t.Fatalf("journal = %+v; want the one POST", rows)
	}
	got := rows[0]
	want := model.AuditLog{
		Actor: "token:ops", IP: "192.0.2.1", Action: "POST /panel/api/setting/apiTokens/delete/:id",
		Target: "id=" + strconv.Itoa(victim), Outcome: "success", Detail: "HTTP 200",
	}
	got.Id, got.Time, got.PrevHash, got.Hash = 0, 0, "", ""
	if got != want {
		t.Fatalf("entry = %+v\nwant    %+v", got, want)
	}
}

func TestAuditNeverStoresThePassword(t *testing.T) {
	engine := newAuditTestEngine(t)
	const token = "audit-test-token"
	seedAuditToken(t, "ops", token)
	const password = "Hunter2-old-pass"
	body := `{"oldUsername":"nobody","oldPassword":"` + password + `","newUsername":"x","newPassword":"` + password + `"}`

	w := serveAudit(engine, http.MethodPost, "/panel/api/setting/updateUser", token, body)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatalf("updateUser with a wrong password: %d %s", w.Code, w.Body.String())
	}
	rows := auditRows(t)
	if len(rows) != 1 || rows[0].Action != "POST /panel/api/setting/updateUser" || rows[0].Outcome != "failure" {
		t.Fatalf("journal = %+v; want one failed updateUser", rows)
	}
	stored, _ := json.Marshal(rows)
	if strings.Contains(string(stored), password) {
		t.Fatalf("the password reached the journal: %s", stored)
	}
}
