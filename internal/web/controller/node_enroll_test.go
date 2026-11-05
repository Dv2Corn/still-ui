package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/nodeenroll"
	"github.com/mhsanaei/3x-ui/v3/internal/web/locale"
)

const enrollNodeAPIToken = "NodeApiTokenNodeApiTokenNodeApiTokenNodeApiToken"

// newEnrollMaster serves the Nodes API and the enrollment agent route under
// /base/ over TLS with a self-signed certificate, as a fresh master does.
func newEnrollMaster(t *testing.T) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	dbtest.InitDB(t, filepath.Join(dbDir, "x-ui.db"))
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("I18n", func(_ locale.I18nType, key string, _ ...string) string { return key })
		c.Next()
	})
	base := engine.Group("/base")
	NewNodeController(base.Group("/panel/api/nodes"))
	NewNodeEnrollAgentController(base.Group("/enroll/v1"))
	srv := httptest.NewTLSServer(engine)
	t.Cleanup(srv.Close)
	return srv
}

func createEnrollInvite(t *testing.T, master *httptest.Server, body map[string]any) nodeenroll.Invite {
	t.Helper()
	body["masterUrl"] = master.URL + "/base/"
	raw, _ := json.Marshal(body)
	resp, err := master.Client().Post(master.URL+"/base/panel/api/nodes/invites/create", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var msg struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
		Obj     struct {
			Secret string `json:"secret"`
		} `json:"obj"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil || !msg.Success {
		t.Fatalf("create invite: %+v %v", msg, err)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("the answer holding the invitation secret may be cached")
	}
	inv, err := nodeenroll.Parse(msg.Obj.Secret)
	if err != nil {
		t.Fatalf("secret %q: %v", msg.Obj.Secret, err)
	}
	return inv
}

// newFakeNodePanel answers the master's status probe only with the API token
// the node hands over at enrollment.
func newFakeNodePanel(t *testing.T) (port int, certSha string) {
	t.Helper()
	node := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nodebase/panel/api/server/status" || r.Header.Get("Authorization") != "Bearer "+enrollNodeAPIToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"obj":{"cpu":1,"mem":{"current":1,"total":2},"xray":{"version":"1","state":"running"},"panelVersion":"v1.6.0","panelGuid":"guid-enroll","uptime":7,"netIO":{"up":3,"down":4}}}`))
	}))
	t.Cleanup(node.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(node.URL, "https://"))
	port, _ = strconv.Atoi(p)
	sum := sha256.Sum256(node.Certificate().Raw)
	return port, hex.EncodeToString(sum[:])
}

func TestNodeEnrollAgentRegistersOnceOverPinnedTLS(t *testing.T) {
	master := newEnrollMaster(t)
	inv := createEnrollInvite(t, master, map[string]any{"name": "fra-1", "allowPrivateAddress": true})
	port, certSha := newFakeNodePanel(t)
	req := nodeenroll.RegisterRequest{
		Name: "ignored-host-name", Address: "127.0.0.1", Port: port, BasePath: "/nodebase/",
		CertSha256: certSha, ApiToken: enrollNodeAPIToken, PanelVersion: "1.6.0",
	}

	wrongPin := inv
	wrongPin.KeyPin = bytes.Repeat([]byte{1}, 32)
	if _, err := nodeenroll.NewClient(wrongPin).Register(context.Background(), req); !errors.Is(err, nodeenroll.ErrKeyPinMismatch) {
		t.Fatalf("a master with another TLS key: %v, want ErrKeyPinMismatch", err)
	}

	client := nodeenroll.NewClient(inv)
	check, err := client.Check(context.Background())
	if err != nil || check.Scope != model.ApiScopeNodeSync || check.Name != "fra-1" {
		t.Fatalf("check %+v %v", check, err)
	}
	resp, err := client.Register(context.Background(), req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if resp.Name != "fra-1" || resp.Address != "127.0.0.1" || resp.TlsVerifyMode != "pin" {
		t.Fatalf("response %+v", resp)
	}
	var node model.Node
	if err := database.GetDB().First(&node, resp.NodeId).Error; err != nil {
		t.Fatal(err)
	}
	if node.PinnedCertSha256 != certSha || node.Port != port || node.BasePath != "/nodebase/" || !node.Enable {
		t.Fatalf("stored node %+v", node)
	}

	var se *nodeenroll.StatusError
	if _, err := client.Register(context.Background(), req); !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("replay of a used invitation: %v, want HTTP 401", err)
	}
	var count int64
	database.GetDB().Model(&model.Node{}).Count(&count)
	if count != 1 {
		t.Fatalf("%d nodes after the replay, want 1", count)
	}
}

func TestNodeEnrollAgentLimitsFailedTokensPerAddress(t *testing.T) {
	master := newEnrollMaster(t)
	inv := createEnrollInvite(t, master, map[string]any{})
	get := func(token string) int {
		t.Helper()
		r, _ := http.NewRequest(http.MethodGet, master.URL+"/base/"+nodeenroll.AgentPath, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := master.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("agent answers may be cached")
		}
		return resp.StatusCode
	}
	bad := strings.Repeat("x", nodeenroll.TokenLength)
	for i := range nodeEnrollFailuresPerIP {
		if code := get(bad); code != http.StatusUnauthorized {
			t.Fatalf("failure %d: HTTP %d, want 401", i+1, code)
		}
	}
	if code := get(bad); code != http.StatusTooManyRequests {
		t.Fatalf("guess after %d failures: HTTP %d, want 429", nodeEnrollFailuresPerIP, code)
	}
	if code := get(inv.Token); code != http.StatusOK {
		t.Fatalf("the valid invitation from the same address: HTTP %d, want 200", code)
	}
	for i := 1; i < nodeEnrollRequestsPerInvite; i++ {
		get(inv.Token)
	}
	if code := get(inv.Token); code != http.StatusTooManyRequests {
		t.Fatalf("request %d with one invitation: HTTP %d, want 429", nodeEnrollRequestsPerInvite+1, code)
	}
}

func TestNodeInviteRoutesNeverListTheToken(t *testing.T) {
	master := newEnrollMaster(t)
	inv := createEnrollInvite(t, master, map[string]any{"name": "n1"})
	resp, err := master.Client().Get(master.URL + "/base/panel/api/nodes/invites")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if strings.Contains(buf.String(), inv.Token) || !strings.Contains(buf.String(), `"status":"pending"`) {
		t.Fatalf("list %s", buf.String())
	}
	r, _ := http.NewRequest(http.MethodPost, master.URL+"/base/panel/api/nodes/invites/revoke/1", nil)
	rr, err := master.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	rr.Body.Close()
	if _, err := nodeenroll.NewClient(inv).Check(context.Background()); err == nil {
		t.Fatal("a revoked invitation still checks out")
	} else if se := (*nodeenroll.StatusError)(nil); !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("revoked: %v, want HTTP 401", err)
	}
}
