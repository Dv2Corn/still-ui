package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/nodeenroll"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
)

const (
	nodeInviteDefaultMinutes = 30
	nodeInviteMaxMinutes     = 24 * 60
	nodeInviteMaxPending     = 20
	// A failed attempt (the master could not reach the node, say) gives the
	// token back, up to this many times; then the invitation is spent.
	nodeInviteMaxAttempts = 5
	// An enrollment claims its invitation while it probes the node; a claim
	// older than this belongs to a request that died and may be taken over.
	nodeInviteClaimTTL = 2 * 60
	// Spent invitations stay listed as the audit trail for a month.
	nodeInviteKeepSeconds  = 30 * 24 * 3600
	nodeEnrollProbeTimeout = 15 * time.Second
)

var (
	// ErrNodeInviteUnauthorized covers a token that is unknown, used, revoked
	// or expired: the agent learns nothing about which.
	ErrNodeInviteUnauthorized = errors.New("the invitation is not valid: it is unknown, already used, revoked or expired")
	// ErrNodeInviteBusy means another enrollment with the same token is running.
	ErrNodeInviteBusy = errors.New("this invitation is being used by another enrollment right now")

	nodeEnrollNameRe     = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	nodeEnrollBasePathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/%+-]*$`)
	nodeEnrollCertRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	nodeEnrollTokenRe    = regexp.MustCompile(`^[A-Za-z0-9]{48}$`)
)

// nodeEnrollRefusal is an enrollment the master turned down after reading it;
// Status is the HTTP status the agent gets.
type nodeEnrollRefusal struct {
	status int
	msg    string
}

func (e *nodeEnrollRefusal) Error() string { return e.msg }

// NodeEnrollHTTPStatus maps an Enroll or CheckInvite error to its HTTP status.
func NodeEnrollHTTPStatus(err error) int {
	var r *nodeEnrollRefusal
	switch {
	case errors.As(err, &r):
		return r.status
	case errors.Is(err, ErrNodeInviteUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNodeInviteBusy):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// NodeInviteInput is what the admin sets when making an invitation.
type NodeInviteInput struct {
	Name                string `json:"name" example:"de-fra-1"`
	Address             string `json:"address" example:""`
	AllowPrivateAddress bool   `json:"allowPrivateAddress" example:"false"`
	NodeAdmin           bool   `json:"nodeAdmin" example:"false"`
	ValidMinutes        int    `json:"validMinutes" example:"30"`
	// MasterUrl is the panel URL nodes reach this master at, as the admin
	// opens it in a browser.
	MasterUrl string `json:"masterUrl" example:"https://panel.example.com:2053/path/"`
}

// NodeInviteView is one invitation as the Nodes page lists it. The token is
// never part of it.
type NodeInviteView struct {
	Id                  int    `json:"id" example:"3"`
	Name                string `json:"name" example:"de-fra-1"`
	Address             string `json:"address" example:""`
	AllowPrivateAddress bool   `json:"allowPrivateAddress" example:"false"`
	NodeAdmin           bool   `json:"nodeAdmin" example:"false"`
	MasterUrl           string `json:"masterUrl" example:"https://panel.example.com:2053/path/"`
	Status              string `json:"status" example:"pending"` // pending|used|expired|revoked
	CreatedAt           int64  `json:"createdAt" example:"1736000000"`
	ExpiresAt           int64  `json:"expiresAt" example:"1736001800"`
	UsedAt              int64  `json:"usedAt" example:"0"`
	UsedFromIp          string `json:"usedFromIp" example:""`
	NodeId              int    `json:"nodeId" example:"0"`
	RevokedAt           int64  `json:"revokedAt" example:"0"`
	Attempts            int    `json:"attempts" example:"0"`
	LastError           string `json:"lastError" example:""`
}

// NodeInviteCreated carries the invitation string, shown once.
type NodeInviteCreated struct {
	Invite NodeInviteView `json:"invite"`
	Secret string         `json:"secret" example:"still-node1.<token>.<pin>.<url>"`
}

// NodeEnrollService makes, lists and revokes node invitations and turns a valid
// one into a node. The func fields are test seams; nil means the real network.
type NodeEnrollService struct {
	nodeService NodeService
	now         func() time.Time
	probe       func(ctx context.Context, n *model.Node) error
	masterPin   func(ctx context.Context, masterURL string) ([]byte, error)
}

func (s *NodeEnrollService) clock() int64 {
	if s.now != nil {
		return s.now().Unix()
	}
	return time.Now().Unix()
}

func inviteStatus(r *model.NodeInvite, now int64) string {
	switch {
	case r.UsedAt > 0:
		return "used"
	case r.RevokedAt > 0:
		return "revoked"
	case r.ExpiresAt <= now || r.Attempts >= nodeInviteMaxAttempts:
		return "expired"
	}
	return "pending"
}

func inviteView(r *model.NodeInvite, now int64) NodeInviteView {
	return NodeInviteView{
		Id: r.Id, Name: r.Name, Address: r.Address, AllowPrivateAddress: r.AllowPrivateAddress,
		NodeAdmin: r.NodeAdmin, MasterUrl: r.MasterUrl, Status: inviteStatus(r, now),
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, UsedAt: r.UsedAt, UsedFromIp: r.UsedFromIp,
		NodeId: r.NodeId, RevokedAt: r.RevokedAt, Attempts: r.Attempts, LastError: r.LastError,
	}
}

func cleanNodeName(name string) string {
	name = strings.Trim(nodeEnrollNameRe.ReplaceAllString(strings.TrimSpace(name), "-"), "-.")
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// CreateInvite stores a new invitation and returns it with its secret. The
// master's TLS key is read at MasterUrl now, so the node can pin it.
func (s *NodeEnrollService) CreateInvite(ctx context.Context, in NodeInviteInput) (*NodeInviteCreated, error) {
	masterURL, err := nodeenroll.NormalizeMasterURL(in.MasterUrl)
	if err != nil {
		return nil, err
	}
	minutes := in.ValidMinutes
	if minutes == 0 {
		minutes = nodeInviteDefaultMinutes
	}
	if minutes < 5 || minutes > nodeInviteMaxMinutes {
		return nil, common.NewError("an invitation is valid for 5 minutes to 24 hours")
	}
	name := cleanNodeName(in.Name)
	if strings.TrimSpace(in.Name) != "" && name == "" {
		return nil, common.NewError("the node name may use letters, digits, dot, dash and underscore")
	}
	address := ""
	if strings.TrimSpace(in.Address) != "" {
		if address, err = netsafe.NormalizeHost(in.Address); err != nil {
			return nil, err
		}
	}
	now := s.clock()
	db := database.GetDB()
	var pending int64
	if err := db.Model(&model.NodeInvite{}).
		Where("used_at = 0 AND revoked_at = 0 AND expires_at > ? AND attempts < ?", now, nodeInviteMaxAttempts).
		Count(&pending).Error; err != nil {
		return nil, err
	}
	if pending >= nodeInviteMaxPending {
		return nil, common.NewErrorf("there are already %d unused invitations; revoke some first", nodeInviteMaxPending)
	}
	pinFn := s.masterPin
	if pinFn == nil {
		pinFn = readMasterKeyPin
	}
	pin, err := pinFn(ctx, masterURL)
	if err != nil {
		return nil, common.NewErrorf("could not read this panel's TLS certificate at %s: %v", masterURL, err)
	}
	token := random.Seq(nodeenroll.TokenLength)
	row := &model.NodeInvite{
		TokenHash: crypto.HashTokenSHA256(token), Name: name, Address: address,
		AllowPrivateAddress: in.AllowPrivateAddress, NodeAdmin: in.NodeAdmin, MasterUrl: masterURL,
		CreatedAt: now, ExpiresAt: now + int64(minutes)*60,
	}
	if err := db.Create(row).Error; err != nil {
		return nil, err
	}
	logger.Infof("node enrollment: invitation #%d created for %q, valid %d min, scope %s", row.Id, name, minutes, inviteScope(row))
	secret := nodeenroll.Invite{Token: token, KeyPin: pin, MasterURL: masterURL}.String()
	return &NodeInviteCreated{Invite: inviteView(row, now), Secret: secret}, nil
}

func inviteScope(r *model.NodeInvite) string {
	if r.NodeAdmin {
		return model.ApiScopeNodeAdmin
	}
	return model.ApiScopeNodeSync
}

// ListInvites returns the invitations, newest first, and drops those spent
// more than a month ago.
func (s *NodeEnrollService) ListInvites() ([]NodeInviteView, error) {
	now := s.clock()
	db := database.GetDB()
	cutoff := now - nodeInviteKeepSeconds
	if err := db.Where("expires_at < ? AND (used_at = 0 OR used_at < ?)", cutoff, cutoff).Delete(&model.NodeInvite{}).Error; err != nil {
		return nil, err
	}
	var rows []*model.NodeInvite
	if err := db.Order("id desc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]NodeInviteView, 0, len(rows))
	for _, r := range rows {
		out = append(out, inviteView(r, now))
	}
	return out, nil
}

// RevokeInvite stops an unused invitation from working.
func (s *NodeEnrollService) RevokeInvite(id int) error {
	res := database.GetDB().Model(&model.NodeInvite{}).
		Where("id = ? AND used_at = 0 AND revoked_at = 0", id).
		Update("revoked_at", s.clock())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return common.NewError("no unused invitation with that id")
	}
	logger.Infof("node enrollment: invitation #%d revoked", id)
	return nil
}

// lookupInvite finds the usable invitation for a presented token by its hash.
func (s *NodeEnrollService) lookupInvite(token string, now int64) (*model.NodeInvite, error) {
	token = strings.TrimSpace(token)
	if !nodeEnrollTokenRe.MatchString(token) {
		return nil, ErrNodeInviteUnauthorized
	}
	row := &model.NodeInvite{}
	err := database.GetDB().Where("token_hash = ?", crypto.HashTokenSHA256(token)).First(row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNodeInviteUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if inviteStatus(row, now) != "pending" {
		return nil, ErrNodeInviteUnauthorized
	}
	return row, nil
}

// CheckInvite tells a node whether its invitation is usable, without using it.
func (s *NodeEnrollService) CheckInvite(token string) (*nodeenroll.CheckResponse, *model.NodeInvite, error) {
	row, err := s.lookupInvite(token, s.clock())
	if err != nil {
		return nil, nil, err
	}
	return &nodeenroll.CheckResponse{Name: row.Name, Scope: inviteScope(row), ExpiresAt: row.ExpiresAt}, row, nil
}

// Enroll adds the node once the master reaches it with the credentials it sent,
// and spends the token; remoteIP is its address unless the invite or node names one.
func (s *NodeEnrollService) Enroll(ctx context.Context, token string, req *nodeenroll.RegisterRequest, remoteIP string) (*nodeenroll.RegisterResponse, error) {
	now := s.clock()
	row, err := s.lookupInvite(token, now)
	if err != nil {
		return nil, err
	}
	target, err := validateEnrollRequest(row, req, remoteIP)
	if err != nil {
		return nil, err
	}
	db := database.GetDB()
	claim := db.Model(&model.NodeInvite{}).
		Where("id = ? AND used_at = 0 AND revoked_at = 0 AND expires_at > ? AND attempts < ? AND (claimed_at = 0 OR claimed_at < ?)",
			row.Id, now, nodeInviteMaxAttempts, now-nodeInviteClaimTTL).
		Updates(map[string]any{"claimed_at": now, "attempts": gorm.Expr("attempts + 1")})
	if claim.Error != nil {
		return nil, claim.Error
	}
	if claim.RowsAffected != 1 {
		return nil, ErrNodeInviteBusy
	}
	resp, err := s.enrollClaimed(ctx, row, target)
	if err != nil {
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if relErr := db.Model(&model.NodeInvite{}).Where("id = ? AND claimed_at = ?", row.Id, now).
			Updates(map[string]any{"claimed_at": 0, "last_error": msg}).Error; relErr != nil {
			logger.Warning("node enrollment: releasing invitation", row.Id, ":", relErr)
		}
		logger.Warningf("node enrollment: invitation #%d from %s refused: %s", row.Id, remoteIP, msg)
		return nil, err
	}
	if err := db.Model(&model.NodeInvite{}).Where("id = ? AND claimed_at = ?", row.Id, now).
		Updates(map[string]any{"claimed_at": 0, "used_at": s.clock(), "used_from_ip": remoteIP, "node_id": resp.NodeId, "last_error": ""}).Error; err != nil {
		logger.Warning("node enrollment: marking invitation", row.Id, "used:", err)
	}
	logger.Infof("node enrollment: invitation #%d used from %s; node %q (#%d) added at %s, TLS %s",
		row.Id, remoteIP, resp.Name, resp.NodeId, resp.Address, resp.TlsVerifyMode)
	return resp, nil
}

func refuse(status int, format string, a ...any) error {
	return &nodeEnrollRefusal{status: status, msg: fmt.Sprintf(format, a...)}
}

// enrollTarget is a register request checked and normalised before the
// invitation is claimed, so a malformed one costs no attempt.
type enrollTarget struct {
	address, basePath, certSha, apiToken string
	port                                 int
	name, panelVersion                   string
}

func validateEnrollRequest(row *model.NodeInvite, req *nodeenroll.RegisterRequest, remoteIP string) (*enrollTarget, error) {
	if req == nil {
		return nil, refuse(http.StatusBadRequest, "empty request")
	}
	if req.Port < 1 || req.Port > 65535 {
		return nil, refuse(http.StatusBadRequest, "port must be 1-65535")
	}
	basePath := normalizeBasePath(req.BasePath)
	if !nodeEnrollBasePathRe.MatchString(basePath) || len(basePath) > 256 {
		return nil, refuse(http.StatusBadRequest, "invalid base path")
	}
	certSha := strings.ToLower(strings.TrimSpace(req.CertSha256))
	if !nodeEnrollCertRe.MatchString(certSha) {
		return nil, refuse(http.StatusBadRequest, "certSha256 must be the hex SHA-256 of the node's certificate")
	}
	apiToken := strings.TrimSpace(req.ApiToken)
	if !nodeEnrollTokenRe.MatchString(apiToken) {
		return nil, refuse(http.StatusBadRequest, "apiToken must be a still-ui API token")
	}
	address := row.Address
	if address == "" {
		address = req.Address
	}
	if strings.TrimSpace(address) == "" {
		address = remoteIP
	}
	address, err := netsafe.NormalizeHost(address)
	if err != nil {
		return nil, refuse(http.StatusBadRequest, "node address: %v", err)
	}
	return &enrollTarget{
		address: address, basePath: basePath, certSha: certSha, apiToken: apiToken,
		port: req.Port, name: req.Name, panelVersion: req.PanelVersion,
	}, nil
}

func (s *NodeEnrollService) enrollClaimed(ctx context.Context, row *model.NodeInvite, t *enrollTarget) (*nodeenroll.RegisterResponse, error) {
	address := t.address
	db := database.GetDB()
	var dup int64
	if err := db.Model(&model.Node{}).Where("address = ? AND port = ?", address, t.port).Count(&dup).Error; err != nil {
		return nil, err
	}
	if dup > 0 {
		return nil, refuse(http.StatusConflict, "a node at %s is already on this master", net.JoinHostPort(address, strconv.Itoa(t.port)))
	}
	name, err := s.freeNodeName(row.Name, t.name)
	if err != nil {
		return nil, err
	}

	mreq := &NodeMutationRequest{
		Name: name, Scheme: "https", Address: address, Port: t.port, BasePath: t.basePath,
		ApiToken: &t.apiToken, Enable: true, AllowPrivateAddress: row.AllowPrivateAddress,
		TlsVerifyMode: "verify", InboundSyncMode: "all",
	}
	if t.panelVersion != "" {
		mreq.Remark = "enrolled " + time.Unix(s.clock(), 0).UTC().Format("2006-01-02") + ", still-ui " + truncate(t.panelVersion, 32)
	}
	// A certificate the system trusts survives its renewals; otherwise pin
	// the one the node reported over this authenticated request.
	verifyErr := s.probeNode(ctx, mreq)
	if verifyErr != nil {
		mreq.TlsVerifyMode = "pin"
		mreq.PinnedCertSha256 = t.certSha
		if pinErr := s.probeNode(ctx, mreq); pinErr != nil {
			hint := ""
			if !row.AllowPrivateAddress && errors.Is(pinErr, netsafe.ErrPrivateAddressBlocked) {
				hint = " (the node's address is private: make the invitation with \"allow private address\")"
			}
			return nil, refuse(http.StatusBadGateway, "the master could not reach the node panel at %s: %s%s",
				net.JoinHostPort(address, strconv.Itoa(t.port)), FriendlyProbeError(pinErr.Error()), hint)
		}
	}
	view, err := s.nodeService.CreateFromRequest(mreq)
	if err != nil {
		return nil, refuse(http.StatusBadRequest, "adding the node: %v", err)
	}
	return &nodeenroll.RegisterResponse{NodeId: view.Id, Name: view.Name, Address: view.Address, TlsVerifyMode: view.TlsVerifyMode}, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// freeNodeName picks the admin's name, else the node's, and adds -2, -3, …
// when a node of that name exists.
func (s *NodeEnrollService) freeNodeName(adminName, nodeName string) (string, error) {
	base := cleanNodeName(adminName)
	if base == "" {
		base = cleanNodeName(nodeName)
	}
	if base == "" {
		base = "node"
	}
	if len(base) > 58 {
		base = base[:58]
	}
	db := database.GetDB()
	for i := 1; i <= 100; i++ {
		name := base
		if i > 1 {
			name = base + "-" + strconv.Itoa(i)
		}
		var n int64
		if err := db.Model(&model.Node{}).Where("name = ?", name).Count(&n).Error; err != nil {
			return "", err
		}
		if n == 0 {
			return name, nil
		}
	}
	return "", refuse(http.StatusConflict, "no free node name for %s", base)
}

func (s *NodeEnrollService) probeNode(ctx context.Context, req *NodeMutationRequest) error {
	n, err := s.nodeService.RuntimeNodeFromRequest(0, req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, nodeEnrollProbeTimeout)
	defer cancel()
	if s.probe != nil {
		return s.probe(ctx, n)
	}
	_, err = s.nodeService.Probe(ctx, n)
	return err
}

// readMasterKeyPin reads the key the master presents at its own URL, as a node
// sees it (a reverse proxy included), else the key of its certificate file.
func readMasterKeyPin(ctx context.Context, masterURL string) ([]byte, error) {
	u, err := url.Parse(masterURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := &tls.Dialer{Config: &tls.Config{ServerName: u.Hostname(), InsecureSkipVerify: true}} // lgtm[go/disabled-certificate-check] only the key is read, to pin it
	conn, dialErr := d.DialContext(dialCtx, "tcp", host)
	if dialErr == nil {
		defer conn.Close()
		if certs := conn.(*tls.Conn).ConnectionState().PeerCertificates; len(certs) > 0 {
			return nodeenroll.KeyPin(certs[0]), nil
		}
	}
	certFile, err := (&SettingService{}).GetCertFile()
	if err != nil || certFile == "" {
		if dialErr != nil {
			return nil, dialErr
		}
		return nil, errors.New("no certificate presented")
	}
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("the panel certificate file holds no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return nodeenroll.KeyPin(cert), nil
}
