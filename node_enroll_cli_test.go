package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/nodeenroll"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
)

const cliInviteToken = "InviteTokenInviteTokenInviteTokenInviteToken1234"

// fakeEnrollMaster answers the invitation check and records the register
// request; registerStatus other than 200 refuses it.
type fakeEnrollMaster struct {
	srv            *httptest.Server
	mu             sync.Mutex
	requests       int
	registered     *nodeenroll.RegisterRequest
	registerStatus int
	// loseRegisterAnswer drops the connection after the register is recorded.
	loseRegisterAnswer bool
}

func newFakeEnrollMaster(t *testing.T, scope string) *fakeEnrollMaster {
	t.Helper()
	m := &fakeEnrollMaster{registerStatus: http.StatusOK}
	m.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requests++
		if r.Header.Get("Authorization") != "Bearer "+cliInviteToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(nodeenroll.CheckResponse{Name: "fra-1", Scope: scope, ExpiresAt: time.Now().Add(time.Hour).Unix()})
			return
		}
		req := &nodeenroll.RegisterRequest{}
		_ = json.NewDecoder(r.Body).Decode(req)
		m.registered = req
		if m.loseRegisterAnswer {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		if m.registerStatus != http.StatusOK {
			w.WriteHeader(m.registerStatus)
			_ = json.NewEncoder(w).Encode(nodeenroll.ErrorResponse{Error: "the master could not reach the node panel"})
			return
		}
		_ = json.NewEncoder(w).Encode(nodeenroll.RegisterResponse{NodeId: 7, Name: "fra-1", Address: "203.0.113.9", TlsVerifyMode: "pin"})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fakeEnrollMaster) invite() nodeenroll.Invite {
	return nodeenroll.Invite{Token: cliInviteToken, KeyPin: nodeenroll.KeyPin(m.srv.Certificate()), MasterURL: m.srv.URL + "/"}
}

// enrollPanelDeps stands in for the node's own panel: "restarting" it serves
// the panel's configured certificate and accepts the panel's API tokens.
func enrollPanelDeps(t *testing.T) (nodeEnrollDeps, *int) {
	t.Helper()
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	dbtest.InitDB(t, config.GetDBPath())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	settings := service.SettingService{}
	if err := settings.SetPort(port); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	var srv *http.Server
	t.Cleanup(func() {
		if srv != nil {
			_ = srv.Close()
		}
	})
	deps := nodeEnrollDeps{
		initDB:       func() error { return nil },
		localTimeout: 10 * time.Second,
		hostname:     func() (string, error) { return "host-name", nil },
		restartPanel: func(context.Context) error {
			restarts++
			if srv != nil {
				_ = srv.Close()
				srv = nil
			}
			certFile, _ := settings.GetCertFile()
			keyFile, _ := settings.GetKeyFile()
			if certFile == "" && keyFile == "" {
				return nil // plain HTTP again: nothing for the TLS checks to reach
			}
			pair, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return err
			}
			ln, err := tls.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{Certificates: []tls.Certificate{pair}})
			if err != nil {
				return err
			}
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			if domain, _ := settings.GetWebDomain(); domain != "" {
				engine.Use(middleware.DomainValidatorMiddleware(domain))
			}
			engine.Any("/*path", func(c *gin.Context) {
				token, _ := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
				if _, ok := (&panel.ApiTokenService{}).MatchToken(token); !ok || c.Request.URL.Path != "/panel/api/server/status" {
					c.Status(http.StatusUnauthorized)
					return
				}
				_, _ = c.Writer.Write([]byte(`{"success":true}`))
			})
			srv = &http.Server{ReadHeaderTimeout: time.Second, Handler: engine}
			go func(s *http.Server) { _ = s.Serve(ln) }(srv)
			return nil
		},
	}
	return deps, &restarts
}

func setPanelSetting(t *testing.T, key, value string) {
	t.Helper()
	row := model.Setting{Key: key}
	if err := database.GetDB().Where("key = ?", key).FirstOrCreate(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&row).Update("value", value).Error; err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func apiTokens(t *testing.T) []*panel.ApiTokenView {
	t.Helper()
	rows, err := (&panel.ApiTokenService{}).List()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestNodeEnrollRefusesTheInvitationOnTheCommandLine(t *testing.T) {
	var stderr bytes.Buffer
	code := runNodeEnroll([]string{"still-node1.abc.def.ghi"}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), nodeEnrollInviteEnv) {
		t.Fatalf("exit %d, stderr %q: the invitation on argv must be refused", code, stderr.String())
	}
}

func TestNodeEnrollReadsTheInvitationFromTheEnvironmentAndClearsIt(t *testing.T) {
	t.Setenv(nodeEnrollInviteEnv, "not-an-invitation")
	var stderr bytes.Buffer
	if code := runNodeEnroll(nil, strings.NewReader(""), &bytes.Buffer{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "not a still-ui node invitation") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if _, set := os.LookupEnv(nodeEnrollInviteEnv); set {
		t.Fatal("the invitation stays in the environment of the panel restarted next")
	}
}

func TestNodeEnrollStopsAtAWrongMasterKeyBeforeTouchingThePanel(t *testing.T) {
	m := newFakeEnrollMaster(t, model.ApiScopeNodeSync)
	inv := m.invite()
	inv.KeyPin = bytes.Repeat([]byte{3}, 32)
	opened := false
	deps := nodeEnrollDeps{initDB: func() error { opened = true; return nil }}
	err := enrollNode(context.Background(), inv, nodeEnrollOptions{}, deps, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not the master that made this invitation") {
		t.Fatalf("err %v", err)
	}
	if opened || m.requests != 0 {
		t.Fatalf("panel database opened=%v, requests that reached the server %d: both must be zero", opened, m.requests)
	}
}

func TestNodeEnrollGivesThePanelACertificateAndHandsOverAToken(t *testing.T) {
	m := newFakeEnrollMaster(t, model.ApiScopeNodeAdmin)
	deps, restarts := enrollPanelDeps(t)
	var out bytes.Buffer
	if err := enrollNode(context.Background(), m.invite(), nodeEnrollOptions{address: "203.0.113.9"}, deps, &out); err != nil {
		t.Fatalf("enroll: %v\n%s", err, out.String())
	}
	if *restarts != 1 {
		t.Fatalf("panel restarted %d times, want once for its new certificate", *restarts)
	}
	settings := service.SettingService{}
	certFile, _ := settings.GetCertFile()
	keyFile, _ := settings.GetKeyFile()
	if filepath.Dir(certFile) != filepath.Join(config.GetDBFolderPath(), "node-tls") {
		t.Fatalf("certificate at %q", certFile)
	}
	if st, err := os.Stat(keyFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v %v: want mode 0600", st, err)
	}
	pair, _ := tls.LoadX509KeyPair(certFile, keyFile)
	reg := m.registered
	if reg == nil || reg.Address != "203.0.113.9" || reg.Name != "host-name" || reg.CertSha256 != sha256Hex(pair.Certificate[0]) {
		t.Fatalf("register request %+v", reg)
	}
	tokens := apiTokens(t)
	if len(tokens) != 1 || tokens[0].Scope != model.ApiScopeNodeAdmin {
		t.Fatalf("tokens %+v: want one node-admin token, the scope the invitation asked for", tokens)
	}
	if row, ok := (&panel.ApiTokenService{}).MatchToken(reg.ApiToken); !ok || row.Id != tokens[0].Id {
		t.Fatal("the master got a token this panel does not accept")
	}

	// Run again: the panel has a certificate now and keeps it.
	before, _ := os.ReadFile(certFile)
	m.registerStatus = http.StatusBadGateway
	err := enrollNode(context.Background(), m.invite(), nodeEnrollOptions{}, deps, &out)
	if err == nil || !strings.Contains(err.Error(), "could not reach the node panel") {
		t.Fatalf("refused register: %v", err)
	}
	after, _ := os.ReadFile(certFile)
	if *restarts != 1 || !bytes.Equal(before, after) {
		t.Fatal("a panel that has a certificate got a new one")
	}
	if got := apiTokens(t); len(got) != 1 {
		t.Fatalf("%d API tokens after a refused enrollment, want the token it issued removed", len(got))
	}
	// Revoked while the master was adding the node: 401, and the new token goes too.
	m.registerStatus = http.StatusUnauthorized
	if err := enrollNode(context.Background(), m.invite(), nodeEnrollOptions{}, deps, &out); err == nil ||
		!strings.Contains(err.Error(), "does not accept this invitation") {
		t.Fatalf("register refused with 401: %v", err)
	}
	if got := apiTokens(t); len(got) != 1 {
		t.Fatalf("%d API tokens after a 401 on register, want the token it issued removed", len(got))
	}
}

func TestNodeEnrollNeverReplacesABrokenConfiguredCertificate(t *testing.T) {
	m := newFakeEnrollMaster(t, model.ApiScopeNodeSync)
	deps, restarts := enrollPanelDeps(t)
	settings := service.SettingService{}
	broken := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(broken, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = settings.SetCertFile(broken)
	_ = settings.SetKeyFile(broken)
	err := enrollNode(context.Background(), m.invite(), nodeEnrollOptions{}, deps, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not load") {
		t.Fatalf("err %v", err)
	}
	if got, _ := settings.GetCertFile(); got != broken || *restarts != 0 || m.registered != nil || len(apiTokens(t)) != 0 {
		t.Fatalf("cert %q restarts %d registered %v: the configured certificate must stay and nothing be sent", got, *restarts, m.registered)
	}
}

// With a Panel Domain set the panel answers 403 to any other Host, and the local check dials
// an IP: it must name the domain, or enrollment can never finish on such a panel.
func TestNodeEnrollWaitsThroughThePanelDomainCheck(t *testing.T) {
	m := newFakeEnrollMaster(t, model.ApiScopeNodeSync)
	deps, _ := enrollPanelDeps(t)
	setPanelSetting(t, "webDomain", "panel.example.com")
	var out bytes.Buffer
	if err := enrollNode(context.Background(), m.invite(), nodeEnrollOptions{address: "panel.example.com"}, deps, &out); err != nil {
		t.Fatalf("enroll on a panel with a Panel Domain: %v\n%s", err, out.String())
	}
	if m.registered == nil {
		t.Fatal("the master was never asked to add the node")
	}
}

// The panel listens on XUI_PORT when it is set: the local check and the master both need that port.
func TestNodeEnrollUsesThePortThePanelListensOn(t *testing.T) {
	m := newFakeEnrollMaster(t, model.ApiScopeNodeSync)
	deps, _ := enrollPanelDeps(t)
	settings := service.SettingService{}
	listening, _ := settings.GetPort()
	t.Setenv("XUI_PORT", strconv.Itoa(listening))
	if err := settings.SetPort(listening + 1); err != nil { // a stored port nothing listens on
		t.Fatal(err)
	}
	if err := enrollNode(context.Background(), m.invite(), nodeEnrollOptions{address: "203.0.113.9"}, deps, &bytes.Buffer{}); err != nil {
		t.Fatalf("enroll with XUI_PORT: %v", err)
	}
	if m.registered == nil || m.registered.Port != listening {
		t.Fatalf("register request %+v: want port %d, the one XUI_PORT makes the panel listen on", m.registered, listening)
	}
}

// A failed enrollment leaves an existing panel as it found it: the certificate it gave a panel
// that had none is taken back and the panel restarted onto it, with a line saying so.
func TestNodeEnrollFailureTakesBackTheCertificateItGave(t *testing.T) {
	m := newFakeEnrollMaster(t, model.ApiScopeNodeSync)
	m.registerStatus = http.StatusBadGateway
	deps, restarts := enrollPanelDeps(t)
	var out bytes.Buffer
	err := enrollNode(context.Background(), m.invite(), nodeEnrollOptions{address: "203.0.113.9"}, deps, &out)
	if err == nil || !strings.Contains(err.Error(), "could not reach the node panel") {
		t.Fatalf("refused register: %v", err)
	}
	settings := service.SettingService{}
	certFile, _ := settings.GetCertFile()
	keyFile, _ := settings.GetKeyFile()
	if certFile != "" || keyFile != "" || *restarts != 2 {
		t.Fatalf("cert %q key %q after %d restarts: want both cleared and the panel restarted back", certFile, keyFile, *restarts)
	}
	if !strings.Contains(out.String(), "certificate it was given is removed again") {
		t.Fatalf("output %q does not say the certificate was taken back", out.String())
	}
	if len(apiTokens(t)) != 0 {
		t.Fatal("the token of a refused enrollment stayed")
	}
}

// A register whose answer never arrives may have added the node on the master: the token and
// certificate the master may now hold stay, and the operator is told how to check.
func TestNodeEnrollKeepsTheTokenWhenTheMastersAnswerIsLost(t *testing.T) {
	m := newFakeEnrollMaster(t, model.ApiScopeNodeSync)
	m.loseRegisterAnswer = true
	deps, _ := enrollPanelDeps(t)
	var out bytes.Buffer
	err := enrollNode(context.Background(), m.invite(), nodeEnrollOptions{address: "203.0.113.9"}, deps, &out)
	if err == nil || !strings.Contains(err.Error(), "may have added this node") {
		t.Fatalf("lost answer: %v", err)
	}
	tokens := apiTokens(t)
	if len(tokens) != 1 || !strings.Contains(err.Error(), tokens[0].Name) {
		t.Fatalf("tokens %+v, error %v: want the token kept and named", tokens, err)
	}
	if certFile, _ := (&service.SettingService{}).GetCertFile(); certFile == "" {
		t.Fatal("the certificate the master may have pinned was taken back")
	}
}
