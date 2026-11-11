package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
)

// Who connected where is for the panel's admin only: a node's sync credential and a
// monitoring token must not read it.
func TestConnLogIsForAdminsOnly(t *testing.T) {
	engine, a := newAPIAuthTestEngine(t)
	api := engine.Group("/panel/api")
	api.Use(a.checkAPIAuth)
	api.Use(a.enforceTokenScope)
	NewConnLogController(api.Group("/connlog"))

	const denied = `{"msg":"this API token is not permitted to access this endpoint","success":false}`
	for _, tc := range []struct {
		scope    string
		wantCode int
	}{
		{model.ApiScopeAdmin, http.StatusOK},
		{model.ApiScopeNodeSync, http.StatusForbidden},
		{model.ApiScopeNodeAdmin, http.StatusForbidden},
		{model.ApiScopeMonitor, http.StatusForbidden},
	} {
		plaintext := "connlog-" + tc.scope
		if err := database.GetDB().Create(&model.ApiToken{Name: tc.scope, Token: crypto.HashTokenSHA256(plaintext), Enabled: true, Scope: tc.scope}).Error; err != nil {
			t.Fatalf("seed token: %v", err)
		}
		for _, path := range []string{"/panel/api/connlog/list", "/panel/api/connlog/export"} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+plaintext)
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if w.Code != tc.wantCode || (tc.wantCode == http.StatusForbidden && w.Body.String() != denied) {
				t.Errorf("%s with a %s token: %d %s, want %d", path, tc.scope, w.Code, w.Body.String(), tc.wantCode)
			}
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/panel/api/connlog/list", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("without a login: %d, want 404", w.Code)
	}

	// A token request runs as the first admin; the record of who read the log must say it was a token.
	logged := strings.Join(logger.GetLogs(50, "INFO"), "\n")
	for _, want := range []string{"connection log: query by an API token (admin) from ", "connection log: export by an API token (admin) from "} {
		if !strings.Contains(logged, want) {
			t.Errorf("the panel log has no %q:\n%s", want, logged)
		}
	}
}

func TestConnLogRejectsABadFilter(t *testing.T) {
	engine, a := newAPIAuthTestEngine(t)
	api := engine.Group("/panel/api")
	api.Use(a.checkAPIAuth)
	NewConnLogController(api.Group("/connlog"))
	if err := database.GetDB().Create(&model.ApiToken{Name: "admin", Token: crypto.HashTokenSHA256("admin-token"), Enabled: true, Scope: model.ApiScopeAdmin}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	for name, body := range map[string]string{
		"range backwards":   `{"from":2000,"to":1000}`,
		"negative time":     `{"from":-1}`,
		"limit too large":   `{"limit":501}`,
		"cursor not issued": `{"before":"yesterday"}`,
		"filter text long":  `{"host":"` + strings.Repeat("a", 256) + `"}`,
		"not json":          `client=alice`,
		"wrong type":        `{"limit":"many"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/panel/api/connlog/list", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer admin-token")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), `"success":false`) {
			t.Errorf("%s: accepted %s: %s", name, body, w.Body.String())
		}
	}
	ok := httptest.NewRequest(http.MethodPost, "/panel/api/connlog/list", strings.NewReader(`{"client":"alice","limit":10}`))
	ok.Header.Set("Content-Type", "application/json")
	ok.Header.Set("Authorization", "Bearer admin-token")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, ok)
	if got := w.Body.String(); !strings.Contains(got, `"success":true`) || !strings.Contains(got, `"rows":[]`) || !strings.Contains(got, `"enabled":false`) {
		t.Errorf("a valid query on an empty log: %s", got)
	}
}
