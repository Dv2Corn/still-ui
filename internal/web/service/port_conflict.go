package service

import (
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/tuic"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/dokodemo"
	xhttpproxy "github.com/xtls/xray-core/proxy/http"
	"github.com/xtls/xray-core/proxy/hysteria"
	"github.com/xtls/xray-core/proxy/masque"
	"github.com/xtls/xray-core/proxy/shadowsocks"
	"github.com/xtls/xray-core/proxy/shadowsocks_2022"
	"github.com/xtls/xray-core/proxy/socks"
	"github.com/xtls/xray-core/proxy/trojan"
	vlessinbound "github.com/xtls/xray-core/proxy/vless/inbound"
	vmessinbound "github.com/xtls/xray-core/proxy/vmess/inbound"
	"github.com/xtls/xray-core/proxy/wireguard"
	"github.com/xtls/xray-core/transport/internet"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type transportBits uint8

const (
	transportTCP transportBits = 1 << iota
	transportUDP
)

// inboundTransports reports the sockets xray-core opens for an inbound, read from the core's
// own build of what GenXrayInboundConfig hands it; ok is false when the core cannot build it.
func inboundTransports(ib *model.Inbound) (transportBits, bool) {
	return configTransports(ib.GenXrayInboundConfig())
}

// conflictTransports is inboundTransports for the port checks: the core cannot run an
// inbound it cannot build, so keeping every socket of its port free for it costs nothing.
func conflictTransports(ib *model.Inbound) transportBits {
	if bits, ok := inboundTransports(ib); ok {
		return bits
	}
	return transportTCP | transportUDP
}

// configTransports builds a generated inbound with infra/conf, exactly as the core loads it.
func configTransports(ib *xray.InboundConfig) (transportBits, bool) {
	switch model.Protocol(ib.Protocol) {
	// Served by the panel's own cores, not by xray-core.
	case model.AmneziaWG, model.TUIC:
		return transportUDP, true
	case model.MTProto:
		return transportTCP, true
	}
	raw, err := json.Marshal(ib)
	if err != nil {
		return 0, false
	}
	// A node's certificate files are not on the master and choose no socket; raw keeps them for the checks.
	built, err := xray.BuildInbound(raw, func(detour *conf.InboundDetourConfig) {
		if detour.StreamSetting != nil && detour.StreamSetting.TLSSettings != nil {
			detour.StreamSetting.TLSSettings.Certs = nil
		}
	})
	if err != nil {
		return 0, false
	}
	receiver, err := built.ReceiverSettings.GetInstance()
	if err != nil {
		return 0, false
	}
	proxy, err := built.ProxySettings.GetInstance()
	if err != nil {
		return 0, false
	}
	receiverConfig, ok := receiver.(*proxyman.ReceiverConfig)
	if !ok {
		return 0, false
	}
	return coreSockets(receiverConfig.StreamSettings, proxy)
}

// coreSockets follows the core's AlwaysOnInboundHandler: each TCP network of the proxy opens
// the stream transport's listener on the port, each UDP network a plain UDP socket.
func coreSockets(stream *internet.StreamConfig, proxy proto.Message) (transportBits, bool) {
	orDefault := func(list []xnet.Network, def ...xnet.Network) []xnet.Network {
		if len(list) == 0 {
			return def
		}
		return list
	}
	var networks []xnet.Network
	switch p := proxy.(type) {
	case *dokodemo.Config:
		// DokodemoDoor.Init refuses an empty list, so the core would not start.
		if networks = p.AllowedNetworks; len(networks) == 0 {
			return 0, false
		}
	case *shadowsocks.ServerConfig:
		networks = orDefault(p.Network, xnet.Network_TCP)
	case *shadowsocks_2022.ServerConfig:
		networks = orDefault(p.Network, xnet.Network_TCP, xnet.Network_UDP)
	case *shadowsocks_2022.MultiUserServerConfig:
		networks = orDefault(p.Network, xnet.Network_TCP, xnet.Network_UDP)
	case *shadowsocks_2022.RelayServerConfig:
		networks = orDefault(p.Network, xnet.Network_TCP, xnet.Network_UDP)
	case *wireguard.DeviceConfig:
		// Network() is empty: the server's Start binds its own UDP socket on the port.
		return transportUDP, true
	case *vlessinbound.Config, *vmessinbound.Config, *trojan.ServerConfig, *xhttpproxy.ServerConfig,
		*socks.ServerConfig, *hysteria.ServerConfig, *masque.ServerConfig:
		// Network() is TCP (and UNIX); a socks UDP associate takes a fresh port per session.
		networks = []xnet.Network{xnet.Network_TCP}
	default:
		return 0, false
	}
	var bits transportBits
	for _, network := range networks {
		switch network {
		case xnet.Network_UDP:
			bits |= transportUDP
		case xnet.Network_TCP:
			listener, ok := streamListener(stream)
			if !ok {
				return 0, false
			}
			bits |= listener
		}
	}
	return bits, true
}

// streamListener is the socket each registered transport listener opens; xdrive polls
// remote storage and binds none.
func streamListener(stream *internet.StreamConfig) (transportBits, bool) {
	name := "tcp"
	var tlsConfig *xtls.Config
	if stream != nil {
		name = stream.ProtocolName
		if security, err := stream.GetEffectiveSecuritySettings(); err == nil {
			tlsConfig, _ = security.(*xtls.Config)
		}
	}
	switch name {
	case "tcp", "websocket", "grpc", "httpupgrade":
		return transportTCP, true
	case "mkcp", "hysteria":
		return transportUDP, true
	case "xdrive":
		return 0, true
	case "splithttp":
		// splithttp's ListenXH serves HTTP/3 alone when the TLS ALPN is exactly h3.
		if tlsConfig != nil && slices.Equal(tlsConfig.NextProtocol, []string{"h3"}) {
			return transportUDP, true
		}
		return transportTCP, true
	case "masque":
		// masque's serverVersions: HTTP/2 on TCP when h2 is offered, HTTP/3 on UDP when h3 is or h2 is not.
		if tlsConfig == nil {
			return 0, false
		}
		h2 := slices.Contains(tlsConfig.NextProtocol, "h2")
		var bits transportBits
		if h2 {
			bits |= transportTCP
		}
		if !h2 || slices.Contains(tlsConfig.NextProtocol, "h3") {
			bits |= transportUDP
		}
		return bits, true
	}
	return 0, false
}

// bindAddr is a listen address plus sockopt.v6only. xray listens on "tcp"/"udp",
// so Go opens every wildcard, 0.0.0.0 included, dual-stack unless v6only is set.
type bindAddr struct {
	listen string
	v6only bool
}

var loopbackBind = bindAddr{listen: "127.0.0.1"}

func inboundBindAddr(ib *model.Inbound) bindAddr {
	return bindAddr{listen: ib.Listen, v6only: streamV6Only(ib.StreamSettings)}
}

func amneziawgKernelMode(ib *model.Inbound) bool {
	if ib == nil || ib.Protocol != model.AmneziaWG {
		return false
	}
	return amneziawgKernelModeSettings(ib.Settings)
}

func amneziawgKernelModeSettings(raw string) bool {
	var settings amneziawg.InboundSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil || settings.Server == nil {
		return false
	}
	return settings.Server.KernelMode
}

func streamV6Only(streamSettings string) bool {
	if !strings.Contains(streamSettings, "v6only") {
		return false
	}
	var stream struct {
		Sockopt struct {
			V6Only bool `json:"v6only"`
		} `json:"sockopt"`
	}
	_ = json.Unmarshal([]byte(streamSettings), &stream)
	return stream.Sockopt.V6Only
}

func listenOverlaps(a, b bindAddr) bool {
	if a.listen == b.listen {
		return true
	}
	familiesA, wildcardA, okA := bindFamilies(a)
	familiesB, wildcardB, okB := bindFamilies(b)
	if !okA || !okB {
		return wildcardA || wildcardB
	}
	return (wildcardA || wildcardB) && familiesA&familiesB != 0
}

type addrFamily uint8

const (
	familyIPv4 addrFamily = 1 << iota
	familyIPv6
)

// bindFamilies reports the address families a listen claims; ok is false for a
// listen that is not an IP, such as a unix socket path.
func bindFamilies(a bindAddr) (families addrFamily, wildcard, ok bool) {
	if isAnyListen(a.listen) {
		if a.v6only {
			return familyIPv6, true, true
		}
		return familyIPv4 | familyIPv6, true, true
	}
	ip := net.ParseIP(a.listen)
	if ip == nil {
		return 0, false, false
	}
	if ip.To4() != nil {
		return familyIPv4, false, true
	}
	return familyIPv6, false, true
}

func isAnyListen(s string) bool {
	return s == "" || s == "0.0.0.0" || s == "::" || s == "::0"
}

type portConflictDetail struct {
	InboundID int
	Remark    string
	Tag       string
	Listen    string
	Port      int
	// Relay marks Port as an automatic loopback relay port, not a configured one.
	Relay bool
	// ForwardedBy is the peer whose port forward holds Port, when that is the
	// reason for the conflict.
	ForwardedBy string
	Transports  transportBits
}

// String renders the detail as a single-line, user-facing summary.
func (d *portConflictDetail) String() string {
	name := d.Remark
	if name == "" {
		name = d.Tag
	}
	if name == "" {
		name = fmt.Sprintf("#%d", d.InboundID)
	} else if d.InboundID > 0 {
		name = fmt.Sprintf("'%s' (#%d)", name, d.InboundID)
	} else {
		// reserved/system inbounds (e.g. the Xray API) have no DB id.
		name = fmt.Sprintf("'%s'", name)
	}
	listen := d.Listen
	if isAnyListen(listen) {
		listen = "*"
	}
	port := fmt.Sprintf("port %d", d.Port)
	if d.Relay {
		port = fmt.Sprintf("relay port %d", d.Port)
	}
	if d.ForwardedBy != "" {
		return fmt.Sprintf("%s (%s) already forwarded on inbound %s on %s by its client %s",
			port, transportTagSuffix(d.Transports), name, listen, d.ForwardedBy)
	}
	return fmt.Sprintf("%s (%s) already used by inbound %s on %s",
		port, transportTagSuffix(d.Transports), name, listen)
}

// defaultXrayAPIPort is the loopback port of the internal Xray API inbound
// (tag "api") seeded into the config template. Used as a fallback when the
// template can't be parsed.
const defaultXrayAPIPort = 62789

// reservedAPIPort returns the port of the internal Xray API inbound declared
// in the config template, falling back to defaultXrayAPIPort.
func reservedAPIPort() int {
	tmpl, err := (&SettingService{}).GetXrayConfigTemplate()
	if err != nil || tmpl == "" {
		return defaultXrayAPIPort
	}
	var parsed struct {
		Inbounds []struct {
			Port int    `json:"port"`
			Tag  string `json:"tag"`
		} `json:"inbounds"`
	}
	if json.Unmarshal([]byte(tmpl), &parsed) != nil {
		return defaultXrayAPIPort
	}
	for _, in := range parsed.Inbounds {
		if in.Tag == "api" && in.Port > 0 {
			return in.Port
		}
	}
	return defaultXrayAPIPort
}

// checkPortConflict reads outside any transaction; callers that must not race a
// concurrent create use checkPortConflictTx inside their own transaction.
func (s *InboundService) checkPortConflict(inbound *model.Inbound, ignoreId int) (*portConflictDetail, error) {
	return checkPortConflictTx(database.GetDB(), inbound, ignoreId)
}

func checkPortConflictTx(db *gorm.DB, inbound *model.Inbound, ignoreId int) (*portConflictDetail, error) {
	newBits := conflictTransports(inbound)
	if err := checkPortHopConflict(db, inbound, ignoreId); err != nil {
		return nil, err
	}

	// The internal Xray API inbound (tag "api", loopback TCP) isn't a DB row,
	// so a local user inbound reusing its port would leave Xray binding the
	// port twice (#5304). Nodes run their own Xray, so this only applies to
	// the local panel.
	if inbound.NodeID == nil && inbound.Port == reservedAPIPort() &&
		newBits&transportTCP != 0 && listenOverlaps(loopbackBind, inboundBindAddr(inbound)) {
		return &portConflictDetail{
			Tag:        "api",
			Listen:     "127.0.0.1",
			Port:       inbound.Port,
			Transports: transportTCP,
		}, nil
	}

	// Egress SOCKS server holds loopback EgressPort when AWG outbounds are
	// active; conflict check prevents inbounds from colliding with it.
	if inbound.NodeID == nil && inbound.Port == amneziawgnet.EgressPort() &&
		newBits&transportTCP != 0 && listenOverlaps(loopbackBind, inboundBindAddr(inbound)) {
		return &portConflictDetail{
			Tag:        "amneziawg-egress",
			Listen:     "127.0.0.1",
			Port:       inbound.Port,
			Transports: transportTCP,
		}, nil
	}

	// Local embedded inbounds reserve an automatically assigned Xray SOCKS relay port.
	if inbound.NodeID == nil && listenOverlaps(loopbackBind, inboundBindAddr(inbound)) {
		conflict, err := checkAmneziawgnetSocksConflict(db, inbound, ignoreId, newBits)
		if err != nil {
			return nil, err
		}
		if conflict != nil {
			return conflict, nil
		}
		conflict, err = checkTuicSocksConflict(db, inbound, ignoreId, newBits)
		if err != nil {
			return nil, err
		}
		if conflict != nil {
			return conflict, nil
		}
	}

	// The reverse direction, only meaningful once the id is known -- AddInbound
	// runs it after Save. Only a local row owns a relay slot (#6537 review).
	if inbound.NodeID == nil && inbound.Protocol == model.AmneziaWG && ignoreId > 0 && !amneziawgKernelMode(inbound) {
		if self := amneziawgnetSocksSelfConflict(inbound, ignoreId); self != "" {
			return nil, common.NewError(self)
		}
		conflict, err := checkAmneziawgnetSocksRelayCollision(db, ignoreId)
		if err != nil {
			return nil, err
		}
		if conflict != nil {
			return conflict, nil
		}
		conflict, err = checkAmneziawgnetSocksReverseConflict(db, ignoreId)
		if err != nil {
			return nil, err
		}
		if conflict != nil {
			return conflict, nil
		}
	}

	// A forwarded port is not a column and not a relay slot, so the query below
	// cannot see it either: the port is bound by the peer's forward listener.
	forwardedBy, err := amneziawgForwardedPortOwner(db, inbound, ignoreId)
	if err != nil {
		return nil, err
	}
	if forwardedBy != nil {
		forwardedBy.Transports = newBits
		return forwardedBy, nil
	}
	if inbound.NodeID == nil && inbound.Protocol == model.TUIC && ignoreId > 0 {
		if self := tuicSocksSelfConflict(inbound, ignoreId); self != "" {
			return nil, common.NewError(self)
		}
		conflict, err := checkTuicSocksRelayCollision(db, ignoreId)
		if err != nil {
			return nil, err
		}
		if conflict != nil {
			return conflict, nil
		}
		conflict, err = checkTuicSocksReverseConflict(db, ignoreId)
		if err != nil {
			return nil, err
		}
		if conflict != nil {
			return conflict, nil
		}
	}

	var candidates []*model.Inbound
	q := db.Model(model.Inbound{}).Where("port = ?", inbound.Port)
	if ignoreId > 0 {
		q = q.Where("id != ?", ignoreId)
	}
	if err := q.Find(&candidates).Error; err != nil {
		return nil, err
	}

	for _, c := range candidates {
		if !sameNode(c.NodeID, inbound.NodeID) {
			continue
		}
		if !listenOverlaps(inboundBindAddr(c), inboundBindAddr(inbound)) {
			continue
		}
		existingBits := conflictTransports(c)
		shared := existingBits & newBits
		if shared == 0 {
			continue
		}
		return &portConflictDetail{
			InboundID:  c.Id,
			Remark:     c.Remark,
			Tag:        c.Tag,
			Listen:     c.Listen,
			Port:       c.Port,
			Transports: shared,
		}, nil
	}
	return nil, nil
}

// amneziawgForwardedPortOwner names the AmneziaWG row on the same host whose peer
// forwards inbound's port -- a bind on every interface that only the AWG side checked.
func amneziawgForwardedPortOwner(db *gorm.DB, inbound *model.Inbound, ignoreId int) (*portConflictDetail, error) {
	var rows []*model.Inbound
	q := db.Model(model.Inbound{}).Where("protocol = ?", model.AmneziaWG)
	if ignoreId > 0 {
		q = q.Where("id != ?", ignoreId)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if !sameNode(row.NodeID, inbound.NodeID) {
			continue
		}
		if amneziawgKernelMode(row) {
			continue
		}
		instance, ok := amneziawg.InstanceFromInbound(row)
		if !ok {
			continue
		}
		email, forwards := amneziawgnet.ForwardedPortOwner(instance, inbound.Port)
		if !forwards {
			continue
		}
		return &portConflictDetail{
			InboundID: row.Id,
			Remark:    row.Remark,
			Tag:       row.Tag,
			// the forward binds :port on every interface, wherever the
			// candidate asked to listen.
			Listen:      "",
			Port:        inbound.Port,
			ForwardedBy: email,
		}, nil
	}
	return nil, nil
}

// checkAmneziawgnetSocksConflict checks the embedded relay port each local row reserves.
func checkAmneziawgnetSocksConflict(db *gorm.DB, inbound *model.Inbound, ignoreId int, newBits transportBits) (*portConflictDetail, error) {
	// A disabled row still owns the slot its id derives: SetInboundEnable flips
	// the column with no port check, so enabling it later must not collide.
	var candidates []*model.Inbound
	q := db.Model(model.Inbound{}).Where("protocol = ? AND node_id IS NULL", model.AmneziaWG)
	if ignoreId > 0 {
		q = q.Where("id != ?", ignoreId)
	}
	if err := q.Find(&candidates).Error; err != nil {
		return nil, err
	}
	// The row reserves its slot before its first peer is added.
	for _, c := range candidates {
		if amneziawgKernelMode(c) {
			continue
		}
		if amneziawgnet.SOCKSPortForInbound(c.Id) != inbound.Port {
			continue
		}
		return &portConflictDetail{
			InboundID:  c.Id,
			Remark:     c.Remark,
			Tag:        c.Tag,
			Listen:     "127.0.0.1",
			Port:       inbound.Port,
			Transports: newBits,
		}, nil
	}
	return nil, nil
}

// checkAmneziawgnetSocksRelayCollision reports whether id's embedded relay port
// is already claimed by another local AmneziaWG inbound, including disabled rows.
func checkAmneziawgnetSocksRelayCollision(db *gorm.DB, id int) (*portConflictDetail, error) {
	relayPort := amneziawgnet.SOCKSPortForInbound(id)
	var candidates []*model.Inbound
	if err := db.Model(model.Inbound{}).
		Where("protocol = ? AND node_id IS NULL AND id != ?", model.AmneziaWG, id).
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if amneziawgKernelMode(c) {
			continue
		}
		if amneziawgnet.SOCKSPortForInbound(c.Id) != relayPort {
			continue
		}
		return &portConflictDetail{
			InboundID:  c.Id,
			Remark:     c.Remark,
			Tag:        c.Tag,
			Listen:     "127.0.0.1",
			Port:       relayPort,
			Relay:      true,
			Transports: transportTCP,
		}, nil
	}
	return nil, nil
}

// amneziawgnetSocksSelfConflict: a row's own WireGuard port vs the relay port its
// own id derives -- all three checks below exclude that id, so nothing else does.
func amneziawgnetSocksSelfConflict(inbound *model.Inbound, id int) string {
	if id <= 0 || inbound.NodeID != nil || amneziawgKernelMode(inbound) || !listenOverlaps(loopbackBind, inboundBindAddr(inbound)) {
		return ""
	}
	relayPort := amneziawgnet.SOCKSPortForInbound(id)
	if inbound.Port != relayPort {
		return ""
	}
	return fmt.Sprintf("WireGuard port %d is inbound #%d's own SOCKS5 relay port on 127.0.0.1; choose a different WireGuard port",
		relayPort, id)
}

// checkAmneziawgnetSocksReverseConflict mirrors checkAmneziawgnetSocksConflict:
// does id's own derived relay port collide with some other inbound's port.
func checkAmneziawgnetSocksReverseConflict(db *gorm.DB, id int) (*portConflictDetail, error) {
	relayPort := amneziawgnet.SOCKSPortForInbound(id)
	var candidates []*model.Inbound
	if err := db.Model(model.Inbound{}).
		Where("port = ? AND node_id IS NULL AND id != ?", relayPort, id).
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if amneziawgKernelMode(c) {
			continue
		}
		if !listenOverlaps(loopbackBind, inboundBindAddr(c)) {
			continue
		}
		return &portConflictDetail{
			InboundID:  c.Id,
			Remark:     c.Remark,
			Tag:        c.Tag,
			Listen:     c.Listen,
			Port:       relayPort,
			Relay:      true,
			Transports: transportTCP,
		}, nil
	}
	return relayPortForwardOwner(db, relayPort, id)
}

// relayPortForwardOwner asks the same of the forward listeners, which no port column
// shows: bound on every interface, one would be dialled in Xray's place by id's relay.
func relayPortForwardOwner(db *gorm.DB, relayPort, id int) (*portConflictDetail, error) {
	owner, err := amneziawgForwardedPortOwner(db, &model.Inbound{Port: relayPort}, id)
	if err != nil || owner == nil {
		return nil, err
	}
	owner.Relay = true
	owner.Transports = transportTCP
	return owner, nil
}

func checkTuicSocksConflict(db *gorm.DB, inbound *model.Inbound, ignoreId int, newBits transportBits) (*portConflictDetail, error) {
	var candidates []*model.Inbound
	q := db.Model(model.Inbound{}).Where("protocol = ? AND node_id IS NULL", model.TUIC)
	if ignoreId > 0 {
		q = q.Where("id != ?", ignoreId)
	}
	if err := q.Find(&candidates).Error; err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if _, ok := tuic.InstanceFromInbound(c); !ok {
			continue
		}
		if tuic.SOCKSPortForInbound(c.Id) != inbound.Port {
			continue
		}
		return &portConflictDetail{
			InboundID:  c.Id,
			Remark:     c.Remark,
			Tag:        c.Tag,
			Listen:     "127.0.0.1",
			Port:       inbound.Port,
			Transports: newBits,
		}, nil
	}
	return nil, nil
}

func checkTuicSocksRelayCollision(db *gorm.DB, id int) (*portConflictDetail, error) {
	relayPort := tuic.SOCKSPortForInbound(id)
	var candidates []*model.Inbound
	if err := db.Model(model.Inbound{}).
		Where("protocol = ? AND node_id IS NULL AND id != ?", model.TUIC, id).
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if tuic.SOCKSPortForInbound(c.Id) != relayPort {
			continue
		}
		return &portConflictDetail{
			InboundID:  c.Id,
			Remark:     c.Remark,
			Tag:        c.Tag,
			Listen:     "127.0.0.1",
			Port:       relayPort,
			Relay:      true,
			Transports: transportTCP,
		}, nil
	}
	return nil, nil
}

func tuicSocksSelfConflict(inbound *model.Inbound, id int) string {
	if id <= 0 || inbound.NodeID != nil || !listenOverlaps(loopbackBind, inboundBindAddr(inbound)) {
		return ""
	}
	relayPort := tuic.SOCKSPortForInbound(id)
	if inbound.Port != relayPort {
		return ""
	}
	return fmt.Sprintf("TUIC port %d is inbound #%d's own SOCKS5 relay port on 127.0.0.1; choose a different TUIC port",
		relayPort, id)
}

func checkTuicSocksReverseConflict(db *gorm.DB, id int) (*portConflictDetail, error) {
	relayPort := tuic.SOCKSPortForInbound(id)
	var candidates []*model.Inbound
	if err := db.Model(model.Inbound{}).
		Where("port = ? AND node_id IS NULL AND id != ?", relayPort, id).
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if !listenOverlaps(loopbackBind, inboundBindAddr(c)) {
			continue
		}
		return &portConflictDetail{
			InboundID:  c.Id,
			Remark:     c.Remark,
			Tag:        c.Tag,
			Listen:     c.Listen,
			Port:       relayPort,
			Relay:      true,
			Transports: transportTCP,
		}, nil
	}
	return relayPortForwardOwner(db, relayPort, id)
}

func sameNode(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func baseInboundTag(port int) string {
	return fmt.Sprintf("in-%v", port)
}

func transportTagSuffix(b transportBits) string {
	switch b {
	case transportTCP:
		return "tcp"
	case transportUDP:
		return "udp"
	case transportTCP | transportUDP:
		return "tcpudp"
	}
	return "any"
}

// nodeTagPrefix scopes a tag to one remote node so the same listen+port
// can live on the central panel and on a node without bumping the global
// UNIQUE(inbounds.tag) constraint. nil → "" (local panel).
func nodeTagPrefix(nodeID *int) string {
	if nodeID == nil {
		return ""
	}
	return fmt.Sprintf("n%d-", *nodeID)
}

func composeInboundTag(port int, nodeID *int, bits transportBits) string {
	return nodeTagPrefix(nodeID) + baseInboundTag(port) + "-" + transportTagSuffix(bits)
}

func isAutoGeneratedTag(tag string, port int, nodeID *int, bits transportBits) bool {
	base := composeInboundTag(port, nodeID, bits)
	if tag == base {
		return true
	}
	suffix, ok := strings.CutPrefix(tag, base+"-")
	if !ok || suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *InboundService) generateInboundTag(inbound *model.Inbound, ignoreId int) (string, error) {
	bits := conflictTransports(inbound)
	candidate := composeInboundTag(inbound.Port, inbound.NodeID, bits)
	exists, err := s.tagExists(candidate, ignoreId)
	if err != nil {
		return "", err
	}
	if !exists {
		return candidate, nil
	}

	for i := 2; i < 100; i++ {
		c := fmt.Sprintf("%s-%d", candidate, i)
		exists, err = s.tagExists(c, ignoreId)
		if err != nil {
			return "", err
		}
		if !exists {
			return c, nil
		}
	}
	return "", common.NewError("could not pick a unique inbound tag for port:", inbound.Port)
}

func (s *InboundService) resolveInboundTag(inbound *model.Inbound, ignoreId int) (string, error) {
	if inbound.Tag != "" {
		taken, err := s.tagExists(inbound.Tag, ignoreId)
		if err != nil {
			return "", err
		}
		if !taken {
			return inbound.Tag, nil
		}
	}
	return s.generateInboundTag(inbound, ignoreId)
}

func (s *InboundService) tagExists(tag string, ignoreId int) (bool, error) {
	reserved, err := templateInboundTags()
	if err != nil {
		return false, err
	}
	if _, exists := reserved[tag]; exists {
		return true, nil
	}
	db := database.GetDB()
	q := db.Model(model.Inbound{}).Where("tag = ?", tag)
	if ignoreId > 0 {
		q = q.Where("id != ?", ignoreId)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func templateInboundTags() (map[string]struct{}, error) {
	raw, err := (&SettingService{}).GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
		API struct {
			Tag string `json:"tag"`
		} `json:"api"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}
	tags := map[string]struct{}{"api": {}}
	if cfg.API.Tag != "" {
		tags[cfg.API.Tag] = struct{}{}
	}
	for _, inbound := range cfg.Inbounds {
		if inbound.Tag != "" {
			tags[inbound.Tag] = struct{}{}
		}
	}
	return tags, nil
}
