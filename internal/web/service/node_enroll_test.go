package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/nodeenroll"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
)

type enrollFixture struct {
	svc    *NodeEnrollService
	now    time.Time
	mu     sync.Mutex
	probes []string // "mode@address" of every probe, in order
	// accept decides a probe; nil accepts only pin mode with goodCert.
	accept func(n *model.Node) error
}

var testMasterPin = bytes.Repeat([]byte{7}, 32)

const (
	goodCert  = "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11"
	nodeToken = "NodeTokenNodeTokenNodeTokenNodeTokenNodeToken123"
)

func newEnrollFixture(t *testing.T) *enrollFixture {
	t.Helper()
	setupBulkDB(t)
	f := &enrollFixture{now: time.Unix(1_800_000_000, 0)}
	f.svc = &NodeEnrollService{
		now:       func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		masterPin: func(context.Context, string) ([]byte, error) { return testMasterPin, nil },
		probe: func(_ context.Context, n *model.Node) error {
			f.mu.Lock()
			f.probes = append(f.probes, n.TlsVerifyMode+"@"+n.Address)
			accept := f.accept
			f.mu.Unlock()
			if accept != nil {
				return accept(n)
			}
			if n.TlsVerifyMode == "pin" && n.PinnedCertSha256 == goodCert && n.ApiToken == nodeToken {
				return nil
			}
			return errors.New("x509: certificate signed by unknown authority")
		},
	}
	return f
}

func (f *enrollFixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *enrollFixture) invite(t *testing.T, in NodeInviteInput) (string, nodeenroll.Invite) {
	t.Helper()
	if in.MasterUrl == "" {
		in.MasterUrl = "https://master.example.com:2053/secret"
	}
	created, err := f.svc.CreateInvite(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	inv, err := nodeenroll.Parse(created.Secret)
	if err != nil {
		t.Fatalf("the secret does not parse: %v (%s)", err, created.Secret)
	}
	return created.Secret, inv
}

func goodRequest() *nodeenroll.RegisterRequest {
	return &nodeenroll.RegisterRequest{Name: "fra-1", Port: 2053, BasePath: "/abc/", CertSha256: goodCert, ApiToken: nodeToken, PanelVersion: "1.6.0"}
}

func TestNodeInviteStoresOnlyTheHashAndPinsTheMaster(t *testing.T) {
	f := newEnrollFixture(t)
	_, inv := f.invite(t, NodeInviteInput{Name: "fra 1"})
	if inv.MasterURL != "https://master.example.com:2053/secret/" {
		t.Fatalf("master URL %q", inv.MasterURL)
	}
	if !bytes.Equal(inv.KeyPin, testMasterPin) {
		t.Fatal("the invitation does not carry the master's key pin")
	}
	var row model.NodeInvite
	if err := database.GetDB().First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.TokenHash != crypto.HashTokenSHA256(inv.Token) || strings.Contains(row.TokenHash, inv.Token) {
		t.Fatalf("token stored as %q, want only its SHA-256", row.TokenHash)
	}
	if row.Name != "fra-1" || row.ExpiresAt-row.CreatedAt != 30*60 {
		t.Fatalf("row %+v: want name fra-1 and 30 minutes", row)
	}
	views, err := f.svc.ListInvites()
	if err != nil || len(views) != 1 || views[0].Status != "pending" {
		t.Fatalf("list %+v %v", views, err)
	}
}

func TestNodeInviteRefusesPlainHTTPMaster(t *testing.T) {
	f := newEnrollFixture(t)
	if _, err := f.svc.CreateInvite(context.Background(), NodeInviteInput{MasterUrl: "http://master.example.com:2053/"}); err == nil {
		t.Fatal("an invitation for a plain-HTTP master would send its token in clear text")
	}
	if _, err := f.svc.CreateInvite(context.Background(), NodeInviteInput{MasterUrl: "https://m.example.com/", ValidMinutes: 2}); err == nil {
		t.Fatal("validity below 5 minutes accepted")
	}
}

func TestNodeEnrollAddsThePinnedNodeAndSpendsTheToken(t *testing.T) {
	f := newEnrollFixture(t)
	_, inv := f.invite(t, NodeInviteInput{NodeAdmin: true})

	check, _, err := f.svc.CheckInvite(inv.Token)
	if err != nil || check.Scope != model.ApiScopeNodeAdmin {
		t.Fatalf("check %+v %v", check, err)
	}
	resp, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if resp.Name != "fra-1" || resp.Address != "203.0.113.7" || resp.TlsVerifyMode != "pin" {
		t.Fatalf("response %+v", resp)
	}
	n, err := (&NodeService{}).GetById(resp.NodeId)
	if err != nil {
		t.Fatal(err)
	}
	if n.TlsVerifyMode != "pin" || n.PinnedCertSha256 != goodCert || n.ApiToken != nodeToken || n.Port != 2053 || n.BasePath != "/abc/" || n.Scheme != "https" {
		t.Fatalf("node %+v", n)
	}
	if got := strings.Join(f.probes, ","); got != "verify@203.0.113.7,pin@203.0.113.7" {
		t.Fatalf("probes %s: a trusted certificate is tried first, then the pin", got)
	}

	// Replay: the same token is spent.
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.8"); !errors.Is(err, ErrNodeInviteUnauthorized) {
		t.Fatalf("replay: %v, want unauthorized", err)
	}
	if _, _, err := f.svc.CheckInvite(inv.Token); !errors.Is(err, ErrNodeInviteUnauthorized) {
		t.Fatalf("check after use: %v", err)
	}
	views, _ := f.svc.ListInvites()
	if views[0].Status != "used" || views[0].UsedFromIp != "203.0.113.7" || views[0].NodeId != resp.NodeId {
		t.Fatalf("audit row %+v", views[0])
	}
}

func TestNodeEnrollPrefersATrustedCertificate(t *testing.T) {
	f := newEnrollFixture(t)
	f.accept = func(n *model.Node) error {
		if n.TlsVerifyMode == "verify" {
			return nil
		}
		return errors.New("not reached")
	}
	_, inv := f.invite(t, NodeInviteInput{})
	resp, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7")
	if err != nil || resp.TlsVerifyMode != "verify" {
		t.Fatalf("%+v %v: a system-trusted certificate must not be pinned (it would break at renewal)", resp, err)
	}
}

func TestNodeEnrollAddressOrder(t *testing.T) {
	f := newEnrollFixture(t)
	_, inv := f.invite(t, NodeInviteInput{Address: "node.example.com"})
	req := goodRequest()
	req.Address = "198.51.100.1"
	resp, err := f.svc.Enroll(context.Background(), inv.Token, req, "203.0.113.7")
	if err != nil || resp.Address != "node.example.com" {
		t.Fatalf("%+v %v: the admin's address wins", resp, err)
	}
	_, inv = f.invite(t, NodeInviteInput{})
	req = goodRequest()
	req.Address = "198.51.100.1"
	resp, err = f.svc.Enroll(context.Background(), inv.Token, req, "203.0.113.7")
	if err != nil || resp.Address != "198.51.100.1" || resp.Name != "fra-1-2" {
		t.Fatalf("%+v %v: the node's address beats the source address; the name gets a suffix", resp, err)
	}
}

func TestNodeEnrollRefusesRevokedAndExpired(t *testing.T) {
	f := newEnrollFixture(t)
	_, inv := f.invite(t, NodeInviteInput{})
	views, _ := f.svc.ListInvites()
	if err := f.svc.RevokeInvite(views[0].Id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7"); !errors.Is(err, ErrNodeInviteUnauthorized) {
		t.Fatalf("revoked: %v", err)
	}
	if err := f.svc.RevokeInvite(views[0].Id); err == nil {
		t.Fatal("revoking twice must say there is nothing to revoke")
	}

	_, inv = f.invite(t, NodeInviteInput{ValidMinutes: 10})
	f.advance(10*time.Minute + time.Second)
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7"); !errors.Is(err, ErrNodeInviteUnauthorized) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := f.svc.Enroll(context.Background(), "x"+inv.Token[1:], goodRequest(), "203.0.113.7"); !errors.Is(err, ErrNodeInviteUnauthorized) {
		t.Fatalf("unknown token: %v", err)
	}
}

func TestNodeEnrollFailureGivesTheTokenBackFiveTimes(t *testing.T) {
	f := newEnrollFixture(t)
	f.accept = func(*model.Node) error { return errors.New("dial tcp: i/o timeout") }
	_, inv := f.invite(t, NodeInviteInput{})
	for i := 1; i <= nodeInviteMaxAttempts; i++ {
		_, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7")
		if NodeEnrollHTTPStatus(err) != http.StatusBadGateway || !strings.Contains(err.Error(), "could not reach the node") {
			t.Fatalf("attempt %d: %v (%d)", i, err, NodeEnrollHTTPStatus(err))
		}
	}
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7"); !errors.Is(err, ErrNodeInviteUnauthorized) {
		t.Fatalf("after %d failures: %v", nodeInviteMaxAttempts, err)
	}
	views, _ := f.svc.ListInvites()
	if views[0].Status != "expired" || !strings.Contains(views[0].LastError, "i/o timeout") {
		t.Fatalf("audit row %+v", views[0])
	}
	var nodes int64
	database.GetDB().Model(&model.Node{}).Count(&nodes)
	if nodes != 0 {
		t.Fatal("a node the master cannot reach was added")
	}
}

func TestNodeEnrollOneAtATime(t *testing.T) {
	f := newEnrollFixture(t)
	_, inv := f.invite(t, NodeInviteInput{})
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	f.accept = func(n *model.Node) error {
		once.Do(func() { close(started) })
		<-release
		if n.TlsVerifyMode == "pin" {
			return nil
		}
		return errors.New("untrusted")
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7")
		done <- err
	}()
	<-started
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.9"); !errors.Is(err, ErrNodeInviteBusy) {
		t.Fatalf("a second enrollment with the token while the first runs: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first enrollment: %v", err)
	}
	var nodes int64
	database.GetDB().Model(&model.Node{}).Count(&nodes)
	if nodes != 1 {
		t.Fatalf("%d nodes from one invitation", nodes)
	}
}

func TestNodeEnrollValidatesTheRequest(t *testing.T) {
	f := newEnrollFixture(t)
	_, inv := f.invite(t, NodeInviteInput{})
	bad := []func(r *nodeenroll.RegisterRequest){
		func(r *nodeenroll.RegisterRequest) { r.Port = 0 },
		func(r *nodeenroll.RegisterRequest) { r.CertSha256 = "zz" },
		func(r *nodeenroll.RegisterRequest) { r.ApiToken = "short" },
		func(r *nodeenroll.RegisterRequest) { r.BasePath = "/a b/" },
	}
	for i, mutate := range bad {
		req := goodRequest()
		mutate(req)
		if _, err := f.svc.Enroll(context.Background(), inv.Token, req, "203.0.113.7"); NodeEnrollHTTPStatus(err) != http.StatusBadRequest {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	if len(f.probes) != 0 {
		t.Fatal("a malformed request reached the node")
	}
	if views, _ := f.svc.ListInvites(); views[0].Attempts != 0 {
		t.Fatalf("malformed requests used %d attempts", views[0].Attempts)
	}
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7"); err != nil {
		t.Fatalf("the token must survive malformed requests: %v", err)
	}
	// The same server a second time is refused, not duplicated.
	_, inv = f.invite(t, NodeInviteInput{})
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7"); NodeEnrollHTTPStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate node: %v", err)
	}
}

func TestNodeInvitePendingCap(t *testing.T) {
	f := newEnrollFixture(t)
	for range nodeInviteMaxPending {
		f.invite(t, NodeInviteInput{})
	}
	if _, err := f.svc.CreateInvite(context.Background(), NodeInviteInput{MasterUrl: "https://m.example.com/"}); err == nil {
		t.Fatal("unbounded unused invitations")
	}
}

func TestNodeEnrollRevokedMidwayAddsNoNode(t *testing.T) {
	f := newEnrollFixture(t)
	_, inv := f.invite(t, NodeInviteInput{})
	views, _ := f.svc.ListInvites()
	id := views[0].Id
	// The admin revokes after the claim, while the master probes the node.
	f.accept = func(n *model.Node) error {
		if n.TlsVerifyMode != "pin" {
			return errors.New("x509: certificate signed by unknown authority")
		}
		if err := f.svc.RevokeInvite(id); err != nil {
			t.Errorf("revoke during enrollment: %v", err)
		}
		return nil
	}
	resp, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7")
	if !errors.Is(err, ErrNodeInviteRevoked) || resp != nil || NodeEnrollHTTPStatus(err) != http.StatusUnauthorized {
		t.Fatalf("Enroll = %+v, %v: a revoked invitation must hand out nothing", resp, err)
	}
	var nodes int64
	database.GetDB().Model(&model.Node{}).Count(&nodes)
	if nodes != 0 {
		t.Fatalf("%d node(s) left from a revoked invitation", nodes)
	}
	views, _ = f.svc.ListInvites()
	if views[0].Status != "revoked" || views[0].NodeId != 0 || !strings.Contains(views[0].LastError, "revoked while the node was being added") {
		t.Fatalf("audit row %+v", views[0])
	}
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7"); !errors.Is(err, ErrNodeInviteUnauthorized) {
		t.Fatalf("retry after revoke: %v", err)
	}
}

// When the node added under a revoked invitation cannot be removed, it is left disabled and
// holding no credentials for the agent's panel.
func TestNodeEnrollRevokedMidwayUnremovableNodeKeepsNoCredentials(t *testing.T) {
	f := newEnrollFixture(t)
	_, inv := f.invite(t, NodeInviteInput{})
	views, _ := f.svc.ListInvites()
	id := views[0].Id
	nodeID := 1 // the first node row; an inbound on it makes the removal refuse
	f.accept = func(n *model.Node) error {
		if n.TlsVerifyMode != "pin" {
			return errors.New("x509: certificate signed by unknown authority")
		}
		if err := f.svc.RevokeInvite(id); err != nil {
			t.Errorf("revoke during enrollment: %v", err)
		}
		ib := &model.Inbound{UserId: 1, Tag: "on-the-new-node", Port: 42999, Protocol: model.VLESS, NodeID: &nodeID}
		if err := database.GetDB().Create(ib).Error; err != nil {
			t.Errorf("attach an inbound: %v", err)
		}
		return nil
	}
	if _, err := f.svc.Enroll(context.Background(), inv.Token, goodRequest(), "203.0.113.7"); !errors.Is(err, ErrNodeInviteRevoked) {
		t.Fatalf("Enroll: %v", err)
	}
	var nodes []model.Node
	database.GetDB().Find(&nodes)
	if len(nodes) != 1 || nodes[0].Id != nodeID {
		t.Fatalf("nodes %+v: want the one node that could not be removed", nodes)
	}
	if n := nodes[0]; n.Enable || n.ApiToken != "" || n.PinnedCertSha256 != "" || !strings.Contains(n.LastError, "could not be removed") {
		t.Fatalf("the unremovable node kept access: enable=%v token=%q pin=%q lastError=%q", n.Enable, n.ApiToken, n.PinnedCertSha256, n.LastError)
	}
	views, _ = f.svc.ListInvites()
	if !strings.Contains(views[0].LastError, "revoked while the node was being added; the node could not be removed, disabled") {
		t.Fatalf("audit row %+v", views[0])
	}
}
