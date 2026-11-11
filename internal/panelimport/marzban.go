package panelimport

import (
	"bytes"
	"crypto/ecdh"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	xrayjson "github.com/xtls/xray-core/infra/conf/json"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// marzbanProtocols are the inbound protocols Marzban manages users for (app/models/proxy.py).
var marzbanProtocols = map[string]bool{"vmess": true, "vless": true, "trojan": true, "shadowsocks": true}

// marzbanResets maps data_limit_reset_strategy onto the client traffic cycle; year has none.
var marzbanResets = map[string]string{"no_reset": "never", "day": "daily", "week": "weekly", "month": "monthly"}

type marzbanUser struct {
	id       int64
	username string
	client   model.Client
	used     int64
	// proxies holds the credentials per protocol: id, password, flow, method.
	proxies  map[string]map[string]any
	excluded map[string]map[string]bool
	// emails names the client that holds each protocol's credential.
	emails    map[string]string
	notBefore int64
	statDone  bool
}

// marzbanInbound is what Marzban derives from an Xray inbound to decide a client's flow.
type marzbanInbound struct {
	raw        map[string]any
	tag        string
	protocol   string
	network    string
	security   string
	headerType string
}

// ReadMarzban reads a Marzban SQLite database and its xray_config.json. Schema and rules
// follow Gozargah/Marzban v0.8.4 (7f396db3e703d71a28060bc9ce4a532ec64cb1f4).
func ReadMarzban(dbPath string, xrayConfig []byte) (*Plan, error) {
	inbounds, err := parseMarzbanConfig(xrayConfig)
	if err != nil {
		return nil, err
	}
	gdb, closeDB, err := openSource(dbPath)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	if !hasTable(gdb, "users") || !hasTable(gdb, "proxies") || !hasTable(gdb, "jwt") {
		return nil, errors.New("not a Marzban database: the users, proxies or jwt table is missing")
	}

	plan := &Plan{Source: SourceMarzban, Hosts: map[string][]model.Host{}}
	if err := gdb.Raw("SELECT secret_key FROM jwt ORDER BY id LIMIT 1").Scan(&plan.SubSecret).Error; err != nil {
		return nil, fmt.Errorf("read jwt: %w", err)
	}
	admins, err := rows(gdb, "admins")
	if err != nil {
		return nil, err
	}
	for _, a := range admins {
		plan.Admins = append(plan.Admins, asString(a["username"]))
	}
	users, err := readMarzbanUsers(gdb, plan, inbounds)
	if err != nil {
		return nil, err
	}
	hosts, err := rows(gdb, "hosts")
	if err != nil {
		return nil, err
	}

	attached := map[int64]bool{}
	for _, in := range inbounds {
		port, ok := in.raw["port"].(float64)
		if !ok || port != math.Trunc(port) || port < 1 || port > 65535 {
			plan.note("inbound %q: no single numeric port (a fallback or a port range), not imported", in.tag)
			continue
		}
		row, err := in.toInbound(int(port))
		if err != nil {
			plan.note("inbound %q: %v, not imported", in.tag, err)
			continue
		}
		var clients []model.Client
		methods := map[string]int{}
		for _, u := range users {
			c, ok := u.clientFor(in)
			if !ok {
				continue
			}
			methods[asString(u.proxies[in.protocol]["method"])]++
			clients = append(clients, c)
			attached[u.id] = true
			if c.Email == u.username && !u.statDone {
				u.statDone = true
				row.ClientStats = append(row.ClientStats, xray.ClientTraffic{
					Email: c.Email, Enable: c.Enable, Down: u.used, Total: c.TotalGB, ExpiryTime: c.ExpiryTime,
				})
			}
		}
		if err := setClients(plan, row, in, clients, methods); err != nil {
			return nil, err
		}
		plan.Inbounds = append(plan.Inbounds, row)
		for _, h := range hosts {
			if asString(h["inbound_tag"]) != in.tag {
				continue
			}
			if host, ok := marzbanHost(plan, in.tag, h, len(plan.Hosts[in.tag])); ok {
				plan.Hosts[in.tag] = append(plan.Hosts[in.tag], host)
			}
		}
	}
	for _, u := range users {
		if !attached[u.id] {
			plan.note("user %q: none of its inbounds is imported, skipped", u.username)
			continue
		}
		plan.SubUsers = append(plan.SubUsers, model.LegacySubUser{
			Username: strings.ToLower(u.username), SubID: u.client.SubID, NotBefore: u.notBefore,
		})
		// A user whose primary client sits on no inbound still has its usage to carry.
		if !u.statDone && u.used > 0 {
			plan.note("user %q: its %d used bytes are not carried over, only its VMess or Shadowsocks client is imported", u.username, u.used)
		}
	}
	return plan, nil
}

func parseMarzbanConfig(raw []byte) ([]*marzbanInbound, error) {
	var cfg struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	// Marzban reads the file with commentjson, so comments are legal in it.
	if err := json.NewDecoder(&xrayjson.Reader{Reader: bytes.NewReader(raw)}).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("xray_config.json: %w", err)
	}
	var out []*marzbanInbound
	for _, raw := range cfg.Inbounds {
		protocol, _ := raw["protocol"].(string)
		tag, _ := raw["tag"].(string)
		if !marzbanProtocols[protocol] || tag == "" {
			continue
		}
		in := &marzbanInbound{raw: raw, tag: tag, protocol: protocol, network: "tcp", security: "none"}
		if stream, ok := raw["streamSettings"].(map[string]any); ok {
			if n, _ := stream["network"].(string); n != "" {
				in.network = n
			}
			if s, _ := stream["security"].(string); s != "" {
				in.security = s
			}
			if net, ok := stream[in.network+"Settings"].(map[string]any); ok {
				if header, ok := net["header"].(map[string]any); ok {
					in.headerType, _ = header["type"].(string)
				}
			}
		}
		out = append(out, in)
	}
	if len(out) == 0 {
		return nil, errors.New("xray_config.json has no vmess, vless, trojan or shadowsocks inbound with a tag")
	}
	return out, nil
}

// flowApplies is Marzban's rule (app/xray/config.py include_db_users) for keeping a flow.
func (in *marzbanInbound) flowApplies() bool {
	rawTCP := in.network == "tcp" || in.network == "raw" || in.network == "kcp"
	return rawTCP && (in.security == "tls" || in.security == "reality") && in.headerType != "http"
}

func (in *marzbanInbound) toInbound(port int) (*model.Inbound, error) {
	row := &model.Inbound{Tag: in.tag, Remark: in.tag, Enable: true, Port: port, Protocol: model.Protocol(in.protocol)}
	row.Listen, _ = in.raw["listen"].(string)
	if stream, ok := in.raw["streamSettings"].(map[string]any); ok {
		if in.security == "reality" {
			if err := fillRealityLinkSettings(stream); err != nil {
				return nil, err
			}
		}
		if in.security == "tls" {
			fillTLSServerName(stream)
		}
		b, err := json.Marshal(stream)
		if err != nil {
			return nil, err
		}
		row.StreamSettings = string(b)
	}
	if sniffing, ok := in.raw["sniffing"]; ok {
		b, err := json.Marshal(sniffing)
		if err != nil {
			return nil, err
		}
		row.Sniffing = string(b)
	}
	return row, nil
}

// fillRealityLinkSettings adds what the panel reads share links from: the public key of
// the unchanged private key and Marzban's fixed chrome fingerprint.
func fillRealityLinkSettings(stream map[string]any) error {
	reality, _ := stream["realitySettings"].(map[string]any)
	if reality == nil {
		return errors.New("security is reality but realitySettings is missing")
	}
	if _, ok := reality["target"]; !ok {
		if dest, ok := reality["dest"]; ok {
			reality["target"] = dest
			delete(reality, "dest")
		}
	}
	link, _ := reality["settings"].(map[string]any)
	if link == nil {
		link = map[string]any{}
	}
	public, _ := reality["publicKey"].(string)
	if public == "" {
		private, _ := reality["privateKey"].(string)
		seed, err := base64.RawURLEncoding.DecodeString(private)
		if err != nil {
			return fmt.Errorf("reality privateKey: %w", err)
		}
		key, err := ecdh.X25519().NewPrivateKey(seed)
		if err != nil {
			return fmt.Errorf("reality privateKey: %w", err)
		}
		public = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	}
	delete(reality, "publicKey")
	link["publicKey"] = public
	if _, ok := link["fingerprint"]; !ok {
		link["fingerprint"] = "chrome"
	}
	if spx, _ := reality["SpiderX"].(string); spx != "" {
		link["spiderX"] = spx
	}
	reality["settings"] = link
	return nil
}

// fillTLSServerName gives links the SNI Marzban takes from the certificate when neither
// the config nor a host names one; a wildcard name gets a fixed label in place of Marzban's random one.
func fillTLSServerName(stream map[string]any) {
	tls, _ := stream["tlsSettings"].(map[string]any)
	if tls == nil || asString(tls["serverName"]) != "" {
		return
	}
	certs, _ := tls["certificates"].([]any)
	for _, raw := range certs {
		entry, _ := raw.(map[string]any)
		var pemBytes []byte
		if file := asString(entry["certificateFile"]); file != "" {
			pemBytes, _ = os.ReadFile(file)
		} else if lines, ok := entry["certificate"].([]any); ok {
			for _, line := range lines {
				pemBytes = append(pemBytes, asString(line)+"\n"...)
			}
		}
		block, _ := pem.Decode(pemBytes)
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || len(cert.DNSNames) == 0 {
			continue
		}
		tls["serverName"] = strings.Replace(cert.DNSNames[0], "*", "www", 1)
		return
	}
}

func setClients(plan *Plan, row *model.Inbound, in *marzbanInbound, clients []model.Client, methods map[string]int) error {
	settings := map[string]any{}
	if s, ok := in.raw["settings"].(map[string]any); ok {
		for k, v := range s {
			settings[k] = v
		}
	}
	if static, _ := settings["clients"].([]any); len(static) > 0 {
		plan.note("inbound %q: %d client(s) written in xray_config.json itself are not imported", in.tag, len(static))
	}
	if _, ok := settings["method"]; !ok && in.protocol == "shadowsocks" {
		// One method per inbound here; Marzban keeps it per user, the same for all by default.
		names := make([]string, 0, len(methods))
		for name := range methods {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			return methods[names[i]] > methods[names[j]] || (methods[names[i]] == methods[names[j]] && names[i] < names[j])
		})
		method := "chacha20-ietf-poly1305"
		if len(names) > 0 && names[0] != "" {
			method = names[0]
		}
		if len(names) > 1 {
			plan.note("inbound %q: its users have different Shadowsocks methods; all get %s, links with another method stop working", in.tag, method)
		}
		settings["method"] = method
	}
	settings["clients"] = clients
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	row.Settings = string(b)
	return nil
}

func readMarzbanUsers(gdb *gorm.DB, plan *Plan, inbounds []*marzbanInbound) ([]*marzbanUser, error) {
	userRows, err := rows(gdb, "users")
	if err != nil {
		return nil, err
	}
	proxyRows, err := rows(gdb, "proxies")
	if err != nil {
		return nil, err
	}
	excludedRows, err := rows(gdb, "exclude_inbounds_association")
	if err != nil {
		return nil, err
	}
	excludedByProxy := map[int64]map[string]bool{}
	for _, e := range excludedRows {
		id := asInt(e["proxy_id"])
		if excludedByProxy[id] == nil {
			excludedByProxy[id] = map[string]bool{}
		}
		excludedByProxy[id][asString(e["inbound_tag"])] = true
	}

	byID := map[int64]*marzbanUser{}
	users := make([]*marzbanUser, 0, len(userRows))
	for _, r := range userRows {
		u := &marzbanUser{
			id: asInt(r["id"]), username: asString(r["username"]), used: asInt(r["used_traffic"]),
			proxies: map[string]map[string]any{}, excluded: map[string]map[string]bool{},
		}
		status := asString(r["status"])
		u.client = model.Client{
			// Random like any new client's: derived from Marzban's secret it could be
			// recomputed by whoever holds that secret, even after its links are turned off.
			SubID:        random.NumLower(16),
			Enable:       status != "disabled",
			TotalGB:      asInt(r["data_limit"]),
			ExpiryTime:   asInt(r["expire"]) * 1000,
			Comment:      asString(r["note"]),
			TrafficReset: marzbanResets[asString(r["data_limit_reset_strategy"])],
		}
		if created := asTime(r["created_at"]); !created.IsZero() {
			u.client.CreatedAt = created.UnixMilli()
			u.notBefore = ceilUnix(created)
		}
		if revoked := asTime(r["sub_revoked_at"]); !revoked.IsZero() {
			u.notBefore = max(u.notBefore, ceilUnix(revoked))
		}
		if u.client.TrafficReset == "" {
			u.client.TrafficReset = "never"
			plan.note("user %q: the yearly traffic reset has no equivalent, set to never", u.username)
		}
		if status == "on_hold" {
			// A negative expiry starts counting on the first connection, like on_hold.
			u.client.ExpiryTime = -asInt(r["on_hold_expire_duration"]) * 1000
			if !asTime(r["on_hold_timeout"]).IsZero() {
				plan.note("user %q: on hold with a start deadline, which is not carried over", u.username)
			}
		}
		byID[u.id] = u
		users = append(users, u)
	}
	for _, p := range proxyRows {
		u := byID[asInt(p["user_id"])]
		if u == nil {
			continue
		}
		protocol := strings.ToLower(asString(p["type"]))
		settings := map[string]any{}
		if err := json.Unmarshal([]byte(asString(p["settings"])), &settings); err != nil {
			plan.note("user %q: unreadable %s settings, that protocol is skipped", u.username, protocol)
			continue
		}
		u.proxies[protocol] = settings
		u.excluded[protocol] = excludedByProxy[asInt(p["id"])]
	}
	for _, u := range users {
		u.nameClients(plan, inbounds)
	}
	return users, nil
}

func ceilUnix(t time.Time) int64 {
	if t.Nanosecond() > 0 {
		return t.Unix() + 1
	}
	return t.Unix()
}

func (u *marzbanUser) uses(protocol string, inbounds []*marzbanInbound) bool {
	if u.proxies[protocol] == nil {
		return false
	}
	for _, in := range inbounds {
		if in.protocol == protocol && !u.excluded[protocol][in.tag] {
			return true
		}
	}
	return false
}

// nameClients gives the user one client, or a second one per credential the first cannot
// hold: a client has one UUID (VLESS, VMess) and one password (Trojan, Shadowsocks).
func (u *marzbanUser) nameClients(plan *Plan, inbounds []*marzbanInbound) {
	u.emails = map[string]string{"vless": u.username, "vmess": u.username, "trojan": u.username, "shadowsocks": u.username}
	split := func(first, second, key, suffix string) {
		if !u.uses(first, inbounds) || !u.uses(second, inbounds) {
			return
		}
		if asString(u.proxies[first][key]) == asString(u.proxies[second][key]) {
			return
		}
		u.emails[second] = u.username + suffix
		plan.note("user %q: its %s and %s credentials differ, so %s is the separate client %q with its own traffic counter",
			u.username, first, second, second, u.emails[second])
	}
	split("vless", "vmess", "id", ".vmess")
	split("trojan", "shadowsocks", "password", ".ss")
}

func (u *marzbanUser) clientFor(in *marzbanInbound) (model.Client, bool) {
	proxy := u.proxies[in.protocol]
	if proxy == nil || u.excluded[in.protocol][in.tag] {
		return model.Client{}, false
	}
	c := u.client
	c.Email = u.emails[in.protocol]
	switch in.protocol {
	case "vmess":
		c.ID, c.Security = asString(proxy["id"]), "auto"
	case "vless":
		c.ID = asString(proxy["id"])
		if in.flowApplies() {
			c.Flow = asString(proxy["flow"])
		}
	default:
		c.Password = asString(proxy["password"])
	}
	return c, c.ID != "" || c.Password != ""
}

// marzbanHost maps a hosts row; an address Marzban fills at render time ({SERVER_IP}) is
// left empty, which here means the address the subscription is fetched from.
func marzbanHost(plan *Plan, tag string, h map[string]any, index int) (model.Host, bool) {
	remark := asString(h["remark"])
	addresses := splitList(asString(h["address"]))
	host := model.Host{
		SortOrder: index, Remark: remark, Port: int(asInt(h["port"])), Security: "same",
		HostHeader: first(splitList(asString(h["host"]))), Path: asString(h["path"]),
		IsDisabled: asInt(h["is_disabled"]) != 0, AllowInsecure: asInt(h["allowinsecure"]) != 0,
		RandomCount: 1,
	}
	if len(addresses) > 0 && !strings.ContainsAny(addresses[0], "{*") {
		host.Address = addresses[0]
	}
	if len(addresses) > 1 {
		plan.note("host %q of inbound %q: only its first address %q is kept", remark, tag, addresses[0])
	}
	if s := asString(h["security"]); s == "tls" || s == "none" {
		host.Security = s
	}
	if snis := splitList(asString(h["sni"])); len(snis) > 0 {
		host.Sni, host.Snis = snis[0], snis
	}
	if alpn := asString(h["alpn"]); alpn != "" && alpn != "none" {
		host.Alpn = splitList(alpn)
	}
	if fp := asString(h["fingerprint"]); fp != "none" {
		host.Fingerprint = fp
	}
	for _, key := range []string{"fragment_setting", "noise_setting"} {
		if asString(h[key]) != "" {
			plan.note("host %q of inbound %q: %s is not carried over", remark, tag, key)
		}
	}
	return host, remark != ""
}

func splitList(csv string) []string {
	var out []string
	for _, part := range strings.Split(csv, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func first(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}
