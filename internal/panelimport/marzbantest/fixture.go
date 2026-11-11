// Package marzbantest builds a Marzban database and xray_config.json for tests.
package marzbantest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Schema is what SQLAlchemy creates on SQLite from app/db/models.py of Gozargah/Marzban
// v0.8.4 (7f396db3e703d71a28060bc9ce4a532ec64cb1f4), limited to the tables an import reads.
const Schema = `
CREATE TABLE admins (id INTEGER NOT NULL PRIMARY KEY, username VARCHAR(34), hashed_password VARCHAR(128),
  created_at DATETIME, is_sudo BOOLEAN, password_reset_at DATETIME, telegram_id BIGINT,
  discord_webhook VARCHAR(1024), users_usage BIGINT NOT NULL DEFAULT 0);
CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY, username VARCHAR(34) COLLATE NOCASE,
  status VARCHAR(8) NOT NULL, used_traffic BIGINT, data_limit BIGINT,
  data_limit_reset_strategy VARCHAR(8) NOT NULL, expire INTEGER, admin_id INTEGER,
  sub_revoked_at DATETIME, sub_updated_at DATETIME, sub_last_user_agent VARCHAR(512), created_at DATETIME,
  note VARCHAR(500), online_at DATETIME, on_hold_expire_duration BIGINT, on_hold_timeout DATETIME,
  auto_delete_in_days INTEGER, edit_at DATETIME, last_status_change DATETIME);
CREATE TABLE proxies (id INTEGER NOT NULL PRIMARY KEY, user_id INTEGER, type VARCHAR(11) NOT NULL, settings JSON NOT NULL);
CREATE TABLE inbounds (id INTEGER NOT NULL PRIMARY KEY, tag VARCHAR(256) NOT NULL);
CREATE TABLE exclude_inbounds_association (proxy_id INTEGER, inbound_tag VARCHAR(256));
CREATE TABLE hosts (id INTEGER NOT NULL PRIMARY KEY, remark VARCHAR(256) NOT NULL, address VARCHAR(256) NOT NULL,
  port INTEGER, path VARCHAR(256), sni VARCHAR(1000), host VARCHAR(1000), security VARCHAR(15) NOT NULL,
  alpn VARCHAR(14) NOT NULL DEFAULT 'none', fingerprint VARCHAR(10) NOT NULL DEFAULT 'none',
  inbound_tag VARCHAR(256) NOT NULL, allowinsecure BOOLEAN, is_disabled BOOLEAN,
  mux_enable BOOLEAN NOT NULL DEFAULT '0', fragment_setting VARCHAR(100), noise_setting VARCHAR(2000),
  random_user_agent BOOLEAN NOT NULL DEFAULT '0', use_sni_as_host BOOLEAN NOT NULL DEFAULT '0');
CREATE TABLE jwt (id INTEGER NOT NULL PRIMARY KEY, secret_key VARCHAR(64) NOT NULL);
`

const (
	Secret            = "5f2b7c1d9e8a4f60b3c2d1e0f9a8b7c65f2b7c1d9e8a4f60b3c2d1e0f9a8b7c6"
	RealityPrivateKey = "6GzasD1uH4YejR6ie42SdFKc85B0smXEgpGdlI05XWc"
	RealityPublicKey  = "MAWxtqsFgiy0N3D93eOo2A9bLABwZX3rc40a2fWHS2Y"
	ShortID           = "0123abcd"

	AliceVless  = "a1111111-1111-4111-8111-111111111111"
	AliceVmess  = "a2222222-2222-4222-8222-222222222222"
	AliceTrojan = "alice-trojan-pw"
	AliceSS     = "alice-ss-pw"
	BobVless    = "b1111111-1111-4111-8111-111111111111"
	CarolVless  = "c1111111-1111-4111-8111-111111111111"
	DaveVless   = "d1111111-1111-4111-8111-111111111111"
	ErinVless   = "e1111111-1111-4111-8111-111111111111"
	FrankVmess  = "f2222222-2222-4222-8222-222222222222"

	// Tokens made with Marzban's create_subscription_token for the secret above.
	AliceToken        = "YWxpY2UsMTc2MDAwMDAwMA8DX5a3_ern" // alice, issued 1760000000
	AliceJWT          = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJhbGljZSIsImFjY2VzcyI6InN1YnNjcmlwdGlvbiIsImlhdCI6MTc2MDAwMDAwMH0.ns-EO47gF1ElMNKWoaqAWCvi0CZe0JOG20F7_F_Y9T0"
	FrankRevokedToken = "ZnJhbmssMTc0MDAwMDAwMA9rpnHJjHOx" // frank, issued 1740000000, before his revoke
	FrankToken        = "ZnJhbmssMTc2MDAwMDAwMARAkesBt5H7" // frank, issued 1760000000
	// What alice's subscription id would be if derived from the secret and her name
	// (sha256(secret + "\n" + name)[:16]); an import must never issue it.
	DerivableAliceSubID = "c9735a7b68cab525"

	// Users are created at 2025-01-01 10:00:00.5 UTC; frank revoked his link on 2025-06-01.
	CreatedCeil  = int64(1735725601)
	FrankRevoked = int64(1748736000)
	GiB          = int64(1) << 30
	AliceExpire  = int64(4102444800) // 2100-01-01
	DaveExpire   = int64(1600000000) // 2020-09-13, long past
	ErinHoldSecs = int64(30 * 24 * 3600)
)

// Inbound tags of the fixture's xray_config.json, in file order.
const (
	TagReality     = "VLESS TCP REALITY"
	TagVlessWS     = "VLESS WS TLS"
	TagVlessTLS    = "VLESS TCP TLS"
	TagVmessWS     = "VMESS WS"
	TagVmessHTTP   = "VMESS TCP HTTP"
	TagTrojanGRPC  = "TROJAN GRPC TLS"
	TagHTTPUpgrade = "VLESS HTTPUPGRADE TLS"
	TagXHTTP       = "VLESS XHTTP REALITY"
	TagSS          = "Shadowsocks TCP"
	TagFallback    = "VLESS FALLBACK WS"
)

// Build writes the fixture database into a temporary folder and returns its path with
// the matching xray_config.json; basePort shifts every inbound port.
func Build(t testing.TB, basePort int) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.sqlite3")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(Schema)
	exec(`INSERT INTO jwt (id, secret_key) VALUES (1, ?)`, Secret)
	exec(`INSERT INTO admins (id, username, hashed_password, is_sudo) VALUES (1, 'root', 'x', 1), (2, 'reseller', 'x', 0)`)

	const created = "2025-01-01 10:00:00.500000"
	user := func(id int, name, status string, used int64, limit any, reset string, expire any, note any, revoked any, holdSecs any) {
		exec(`INSERT INTO users (id, username, status, used_traffic, data_limit, data_limit_reset_strategy, expire,
			admin_id, sub_revoked_at, created_at, note, on_hold_expire_duration) VALUES (?,?,?,?,?,?,?,1,?,?,?,?)`,
			id, name, status, used, limit, reset, expire, revoked, created, note, holdSecs)
	}
	user(1, "alice", "active", GiB, 10*GiB, "month", AliceExpire, "paid until 2100", nil, nil)
	user(2, "bob", "disabled", 0, nil, "no_reset", nil, nil, nil, nil)
	user(3, "carol", "limited", 5*GiB, 5*GiB, "week", nil, nil, nil, nil)
	user(4, "dave", "expired", 100, nil, "day", DaveExpire, nil, nil, nil)
	user(5, "erin", "on_hold", 0, 3*GiB, "year", nil, nil, nil, ErinHoldSecs)
	user(6, "frank", "active", 42, nil, "no_reset", nil, nil, "2025-06-01 00:00:00.000000", nil)

	proxy := func(id, userID int, kind string, settings map[string]any) {
		raw, _ := json.Marshal(settings)
		exec(`INSERT INTO proxies (id, user_id, type, settings) VALUES (?,?,?,?)`, id, userID, kind, string(raw))
	}
	// SQLAlchemy stores the enum member names: VMess, VLESS, Trojan, Shadowsocks.
	proxy(1, 1, "VLESS", map[string]any{"id": AliceVless, "flow": "xtls-rprx-vision"})
	proxy(2, 1, "VMess", map[string]any{"id": AliceVmess})
	proxy(3, 1, "Trojan", map[string]any{"password": AliceTrojan, "flow": ""})
	proxy(4, 1, "Shadowsocks", map[string]any{"password": AliceSS, "method": "chacha20-ietf-poly1305"})
	proxy(5, 2, "VLESS", map[string]any{"id": BobVless, "flow": ""})
	proxy(6, 3, "VLESS", map[string]any{"id": CarolVless, "flow": "xtls-rprx-vision"})
	proxy(7, 4, "VLESS", map[string]any{"id": DaveVless, "flow": ""})
	proxy(8, 5, "VLESS", map[string]any{"id": ErinVless, "flow": ""})
	proxy(9, 6, "VMess", map[string]any{"id": FrankVmess})
	exec(`INSERT INTO exclude_inbounds_association (proxy_id, inbound_tag) VALUES (5, ?)`, TagVlessWS)

	host := func(tag, remark, address string, port any, path, sni, hostHeader any, security, alpn, fp string, disabled bool) {
		exec(`INSERT INTO inbounds (tag) SELECT ? WHERE NOT EXISTS (SELECT 1 FROM inbounds WHERE tag = ?)`, tag, tag)
		exec(`INSERT INTO hosts (remark, address, port, path, sni, host, security, alpn, fingerprint, inbound_tag, is_disabled)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, remark, address, port, path, sni, hostHeader, security, alpn, fp, tag, disabled)
	}
	for _, tag := range []string{TagReality, TagVlessTLS, TagVmessWS, TagVmessHTTP, TagTrojanGRPC, TagHTTPUpgrade, TagXHTTP, TagSS} {
		host(tag, "{USERNAME} "+tag, "vpn.example.com", nil, nil, nil, nil, "inbound_default", "none", "none", false)
	}
	host(TagVlessWS, "{USERNAME} cdn", "cdn.example.com,cdn2.example.com", 443, "/cdn-path", "sni.example.com", "host.example.com", "tls", "h2,http/1.1", "firefox", false)
	host(TagVlessWS, "{USERNAME} direct", "{SERVER_IP}", nil, nil, "vpn.example.com", nil, "inbound_default", "none", "none", false)
	host(TagVlessWS, "off", "off.example.com", nil, nil, nil, nil, "inbound_default", "none", "none", true)

	return path, xrayConfig(t, basePort)
}

func xrayConfig(t testing.TB, basePort int) []byte {
	t.Helper()
	certLines, keyLines := selfSigned(t)
	tls := map[string]any{"certificates": []any{map[string]any{"certificate": certLines, "key": keyLines}}}
	reality := func() map[string]any {
		return map[string]any{
			"dest": "www.example.org:443", "serverNames": []string{"www.example.org"},
			"privateKey": RealityPrivateKey, "shortIds": []string{ShortID},
		}
	}
	inbound := func(tag string, port int, protocol string, settings, stream map[string]any) map[string]any {
		in := map[string]any{"tag": tag, "listen": "0.0.0.0", "protocol": protocol, "settings": settings}
		if port > 0 {
			in["port"] = basePort + port
		}
		if stream != nil {
			in["streamSettings"] = stream
		}
		return in
	}
	vless := func() map[string]any { return map[string]any{"clients": []any{}, "decryption": "none"} }
	clients := func() map[string]any { return map[string]any{"clients": []any{}} }
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			inbound(TagReality, 1, "vless", vless(), map[string]any{"network": "tcp", "security": "reality", "realitySettings": reality()}),
			inbound(TagVlessWS, 2, "vless", vless(), map[string]any{"network": "ws", "security": "tls", "tlsSettings": tls, "wsSettings": map[string]any{"path": "/vl"}}),
			inbound(TagVlessTLS, 3, "vless", vless(), map[string]any{"network": "tcp", "security": "tls", "tlsSettings": tls}),
			inbound(TagVmessWS, 4, "vmess", clients(), map[string]any{"network": "ws", "security": "none", "wsSettings": map[string]any{"path": "/vm", "host": "ws.example.com"}}),
			inbound(TagVmessHTTP, 5, "vmess", clients(), map[string]any{"network": "tcp", "security": "none", "tcpSettings": map[string]any{
				"header": map[string]any{"type": "http", "request": map[string]any{"path": []string{"/"}, "headers": map[string]any{"Host": []string{"http.example.com"}}}},
			}}),
			inbound(TagTrojanGRPC, 6, "trojan", clients(), map[string]any{"network": "grpc", "security": "tls", "tlsSettings": tls, "grpcSettings": map[string]any{"serviceName": "trgrpc"}}),
			inbound(TagHTTPUpgrade, 7, "vless", vless(), map[string]any{"network": "httpupgrade", "security": "tls", "tlsSettings": tls, "httpupgradeSettings": map[string]any{"path": "/hu", "host": "hu.example.com"}}),
			inbound(TagXHTTP, 8, "vless", vless(), map[string]any{"network": "xhttp", "security": "reality", "realitySettings": reality(), "xhttpSettings": map[string]any{"path": "/xh", "mode": "auto"}}),
			inbound(TagSS, 9, "shadowsocks", map[string]any{"clients": []any{}, "network": "tcp,udp"}, nil),
			inbound(TagFallback, 0, "vless", vless(), map[string]any{"network": "ws", "security": "none", "wsSettings": map[string]any{"path": "/fb"}}),
		},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "DIRECT"}},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// Marzban reads the file with commentjson; operators do leave comments in it.
	return []byte("// edited by hand\n" + string(raw))
}

func selfSigned(t testing.TB) (cert, key []string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "vpn.example.com"},
		DNSNames:  []string{"vpn.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	lines := func(kind string, der []byte) []string {
		return strings.Split(strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))), "\n")
	}
	return lines("CERTIFICATE", der), lines("PRIVATE KEY", keyDER)
}

// WriteConfig stores the Xray config beside the database, where the CLI looks for it.
func WriteConfig(t testing.TB, dbPath string, xrayConfig []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(dbPath), "xray_config.json"), xrayConfig, 0o600); err != nil {
		t.Fatal(err)
	}
}
