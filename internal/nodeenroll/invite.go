// Package nodeenroll is the one-command enrollment contract: "still-node1.<token>.<SPKI SHA-256>.<URL>"
// (base64url fields, dot-separated for install.sh); the node pins the key on every connection.
package nodeenroll

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	// Prefix starts every invitation; the number is the format version.
	Prefix = "still-node1"
	// TokenLength is the length of the alphanumeric one-time token.
	TokenLength = 48
	// AgentPath is the enrollment endpoint relative to the master URL.
	AgentPath = "enroll/v1/node"
	// MaxMasterURL bounds the URL so an invitation stays one copyable line.
	MaxMasterURL = 512
)

var (
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9]{48}$`)
	// The same character set install.sh accepts, so both sides agree.
	masterURLRe = regexp.MustCompile(`^https://[]A-Za-z0-9.:[-]+(/[A-Za-z0-9._~/%+-]*)?$`)
)

// ErrInvalidInvite is returned for a string that is not an invitation.
var ErrInvalidInvite = errors.New("not a still-ui node invitation (it starts with " + Prefix + ".)")

// Invite is a decoded invitation.
type Invite struct {
	Token     string
	KeyPin    []byte // SHA-256 of the master's SubjectPublicKeyInfo
	MasterURL string // https://host:port/basepath/ with the trailing slash
}

// String encodes the invitation.
func (i Invite) String() string {
	enc := base64.RawURLEncoding
	return strings.Join([]string{Prefix, i.Token, enc.EncodeToString(i.KeyPin), enc.EncodeToString([]byte(i.MasterURL))}, ".")
}

// Parse decodes an invitation; surrounding white space is ignored.
func Parse(s string) (Invite, error) {
	parts := strings.Split(strings.Join(strings.Fields(s), ""), ".")
	if len(parts) != 4 || parts[0] != Prefix {
		return Invite{}, ErrInvalidInvite
	}
	enc := base64.RawURLEncoding
	if !tokenRe.MatchString(parts[1]) {
		return Invite{}, ErrInvalidInvite
	}
	pin, err := enc.DecodeString(parts[2])
	if err != nil || len(pin) != sha256.Size {
		return Invite{}, ErrInvalidInvite
	}
	rawURL, err := enc.DecodeString(parts[3])
	if err != nil {
		return Invite{}, ErrInvalidInvite
	}
	master, err := NormalizeMasterURL(string(rawURL))
	if err != nil || master != string(rawURL) {
		return Invite{}, ErrInvalidInvite
	}
	return Invite{Token: parts[1], KeyPin: pin, MasterURL: master}, nil
}

// NormalizeMasterURL accepts the panel URL as opened in a browser, https only
// (the token travels in it), and returns it with a trailing slash.
func NormalizeMasterURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", errors.New("the master URL must be https://host[:port]/base-path/")
	}
	if u.Hostname() == "" {
		return "", errors.New("the master URL has no host")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", errors.New("the master URL has an invalid port")
		}
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	out := "https://" + u.Host + path
	if len(out) > MaxMasterURL || !masterURLRe.MatchString(out) {
		return "", errors.New("the master URL has characters an invitation cannot carry")
	}
	return out, nil
}

// KeyPin is the SHA-256 of a certificate's SubjectPublicKeyInfo, the value
// curl's --pinnedpubkey sha256// checks.
func KeyPin(cert *x509.Certificate) []byte {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return sum[:]
}
