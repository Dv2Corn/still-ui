package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
)

type agentFixture struct {
	engine     http.Handler
	probeToken string
	adminToken string
	targetKey  string
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	engine := newTestEngine(t)
	service.RegisterSubLinkProvider(fakeSubLinkProvider{links: []string{"vless://00000000-0000-4000-8000-000000000001@vpn.example.com:443?type=tcp&security=none#Main"}})
	t.Cleanup(func() { service.RegisterSubLinkProvider(nil) })

	ib := &model.Inbound{Tag: "agent-vless", Enable: true, Port: 25443, Protocol: model.VLESS, Remark: "Main", Settings: `{"clients":[],"decryption":"none"}`}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	created, _, err := (&service.ReachProbeService{}).Create(service.ReachProbeInput{Name: "home-1", InboundIds: []int{ib.Id}})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := (&panel.ApiTokenService{}).Create("automation", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	return &agentFixture{engine: engine, probeToken: created.Token, adminToken: admin.Token, targetKey: strconv.Itoa(ib.Id) + "@vpn.example.com:443"}
}

func (f *agentFixture) do(method, path, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "198.51.100.20:40000"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestProbeAgentTokenOpensOnlyTheAgentRoutes(t *testing.T) {
	f := newAgentFixture(t)

	rec := f.do(http.MethodGet, "/probe/v1/job", f.probeToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("job with the probe token: %d %s", rec.Code, rec.Body)
	}
	var job service.ReachProbeJob
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || len(job.Targets) != 1 || job.Probe != "home-1" {
		t.Fatalf("job body %s (%v)", rec.Body, err)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("the job holds credentials and must not be cached")
	}

	for _, path := range []string{"/panel/api/inbounds/list", "/panel/api/probes/list", "/panel/api/server/status"} {
		if rec := f.do(http.MethodGet, path, f.probeToken, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("probe token on %s: %d, want 401", path, rec.Code)
		}
	}
	if rec := f.do(http.MethodGet, "/probe/v1/job", f.adminToken, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an admin API token on the agent route: %d, want 401", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/probe/v1/job", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", rec.Code)
	}
}

func TestProbeAgentReportIsStoredAndBounded(t *testing.T) {
	f := newAgentFixture(t)
	body, _ := json.Marshal(service.ReachReport{AgentVersion: "1.5.0", Results: []service.ReachReportResult{
		{Key: f.targetKey, Ok: true, TtfbMs: 100, TotalMs: 800, Bytes: 262144},
	}})
	rec := f.do(http.MethodPost, "/probe/v1/report", f.probeToken, body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stored":1`) {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	big := []byte(`{"results":[` + strings.Repeat(`{"key":"x","error":"`+strings.Repeat("a", 1000)+`"},`, 80) + `{}]}`)
	if rec := f.do(http.MethodPost, "/probe/v1/report", f.probeToken, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized report: %d, want 413", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/probe/v1/report", f.probeToken, []byte("{")); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed report: %d, want 400", rec.Code)
	}
	var n int64
	database.GetDB().Model(&model.ReachProbeResult{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d results stored, want 1", n)
	}
}

func TestProbeAgentRateLimitsBadTokensPerAddress(t *testing.T) {
	f := newAgentFixture(t)
	for i := range 20 {
		if rec := f.do(http.MethodGet, "/probe/v1/job", "wrong-token", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	if rec := f.do(http.MethodGet, "/probe/v1/job", f.probeToken, nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after 20 bad tokens even a good one must wait: %d", rec.Code)
	}
}
