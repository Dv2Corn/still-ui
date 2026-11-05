package nodeenroll

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CheckResponse answers GET AgentPath: the invitation is valid and unused.
type CheckResponse struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"` // API token scope the node issues for the master
	ExpiresAt int64  `json:"expiresAt"`
}

// RegisterRequest is what a node tells the master about itself.
type RegisterRequest struct {
	Name         string `json:"name"`
	Address      string `json:"address,omitempty"` // empty: the master uses the request's source address
	Port         int    `json:"port"`
	BasePath     string `json:"basePath"`
	CertSha256   string `json:"certSha256"` // hex SHA-256 of the node panel's leaf certificate
	ApiToken     string `json:"apiToken"`
	PanelVersion string `json:"panelVersion,omitempty"`
}

// RegisterResponse confirms the node is on the master.
type RegisterResponse struct {
	NodeId        int    `json:"nodeId"`
	Name          string `json:"name"`
	Address       string `json:"address"`
	TlsVerifyMode string `json:"tlsVerifyMode"`
}

// ErrorResponse is the body of every refusal.
type ErrorResponse struct {
	Error string `json:"error"`
}

// ErrKeyPinMismatch means the server at the master URL is not the master
// that made the invitation (or its key changed since).
var ErrKeyPinMismatch = errors.New("the master's TLS key does not match the invitation")

// StatusError is a refusal from the master.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the master answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("the master refused (HTTP %d): %s", e.Status, e.Message)
}

// PinnedTLSConfig trusts exactly the server whose public key hashes to pin,
// whoever signed its certificate.
func PinnedTLSConfig(pin []byte) *tls.Config {
	want := append([]byte(nil), pin...)
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // lgtm[go/disabled-certificate-check] the key pin below replaces chain checks
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return ErrKeyPinMismatch
			}
			if subtle.ConstantTimeCompare(KeyPin(cs.PeerCertificates[0]), want) != 1 {
				return ErrKeyPinMismatch
			}
			return nil
		},
	}
}

// Client talks to the master named in an invitation.
type Client struct {
	inv  Invite
	http *http.Client
}

// NewClient returns a client that reaches only the pinned master and never
// follows redirects (the token must not travel anywhere else).
func NewClient(inv Invite) *Client {
	return &Client{inv: inv, http: &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{TLSClientConfig: PinnedTLSConfig(inv.KeyPin), Proxy: http.ProxyFromEnvironment},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Check asks whether the invitation is still valid, without using it.
func (c *Client) Check(ctx context.Context) (*CheckResponse, error) {
	out := &CheckResponse{}
	if err := c.do(ctx, http.MethodGet, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Register uses the invitation: the master adds this node and the token
// stops working.
func (c *Client) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	out := &RegisterResponse{}
	if err := c.do(ctx, http.MethodPost, body, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.inv.MasterURL+AgentPath, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.inv.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, ErrKeyPinMismatch) {
			return ErrKeyPinMismatch
		}
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e ErrorResponse
		_ = json.Unmarshal(raw, &e)
		return &StatusError{Status: resp.StatusCode, Message: e.Error}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer from the master: %w", err)
	}
	return nil
}
