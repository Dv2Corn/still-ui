package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// jsonLeaf is one scalar of a JSON document with the lowercased key path leading to it.
// Repeated keys each yield a leaf: xray-core binds keys case-folded, and the last wins.
type jsonLeaf struct {
	path  []string
	value string
}

func (l jsonLeaf) endsWith(suffix ...string) bool {
	return len(l.path) >= len(suffix) && slices.Equal(l.path[len(l.path)-len(suffix):], suffix)
}

// jsonLeaves walks raw token by token, so no repeated key hides behind another.
func jsonLeaves(raw string) []jsonLeaf {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var leaves []jsonLeaf
	var walk func(path []string) bool
	walk = func(path []string) bool {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch tok {
		case json.Delim('{'):
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return false
				}
				name, _ := key.(string)
				if !walk(append(slices.Clone(path), strings.ToLower(name))) {
					return false
				}
			}
			_, err = dec.Token()
			return err == nil
		case json.Delim('['):
			for dec.More() {
				if !walk(append(slices.Clone(path), "[]")) {
					return false
				}
			}
			_, err = dec.Token()
			return err == nil
		}
		leaves = append(leaves, jsonLeaf{path: path, value: fmt.Sprint(tok)})
		return true
	}
	walk(nil)
	return leaves
}

// isEnvRef matches what xray-core expands from the panel's environment: a whole value, or
// one entry of a comma list such as a port list.
func isEnvRef(value string) bool {
	for part := range strings.SplitSeq(value, ",") {
		if strings.HasPrefix(strings.TrimSpace(part), "env:") {
			return true
		}
	}
	return false
}

// validateInboundEnvRefs refuses "env:NAME" anywhere the core would expand it: the panel's
// environment (database DSN, token key) must not reach a config or a load error quoting it.
func validateInboundEnvRefs(inbound *model.Inbound) error {
	found := isEnvRef(inbound.Listen)
	for _, raw := range []string{inbound.Settings, inbound.StreamSettings, inbound.Sniffing} {
		for _, leaf := range jsonLeaves(raw) {
			found = found || isEnvRef(leaf.value)
		}
	}
	if found {
		return common.NewError(`an inbound must not refer to environment variables ("env:")`)
	}
	return nil
}

// pushReach is one field of a pushed inbound that names a resource on this host.
type pushReach struct {
	field, value string
}

// validatePushedInbound refuses, on a master's push, the fields that reach this host's own
// files, sockets and services; a value the stored row already carries was set here and stays.
func validatePushedInbound(inbound, stored *model.Inbound) error {
	if (stored == nil || stored.Protocol != inbound.Protocol) && !nodeEligibleProtocols[inbound.Protocol] {
		return common.NewErrorf("%s inbounds cannot be pushed to a node", inbound.Protocol)
	}
	// The stored row's relay targets were judged when set and are not resolved again: a
	// flapping resolver must not refuse an unchanged inbound.
	var allowed []pushReach
	var kept map[pushReach]bool
	if stored != nil {
		var err error
		if allowed, err = hostReaches(stored, nil, false); err != nil {
			return err
		}
		kept = relayTargets(stored)
	}
	reaches, err := hostReaches(inbound, kept, true)
	if err != nil {
		return err
	}
	for _, r := range reaches {
		if !slices.Contains(allowed, r) {
			return common.NewErrorf("a pushed inbound may not set %s to %q on this node", r.field, r.value)
		}
	}
	return nil
}

// relayTargets lists an inbound's REALITY targets and fallback dests as written.
func relayTargets(inbound *model.Inbound) map[pushReach]bool {
	targets := map[pushReach]bool{}
	for _, leaf := range jsonLeaves(inbound.StreamSettings) {
		if leaf.endsWith("realitysettings", "target") || leaf.endsWith("realitysettings", "dest") {
			targets[pushReach{field: "REALITY target", value: leaf.value}] = true
		}
	}
	for _, leaf := range jsonLeaves(inbound.Settings) {
		if leaf.endsWith("fallbacks", "[]", "dest") {
			targets[pushReach{field: "fallback dest", value: leaf.value}] = true
		}
	}
	return targets
}

// hostReaches lists what an inbound reaches on this host. A relay target in kept is left
// out unjudged; lookup resolves the names of the others.
func hostReaches(inbound *model.Inbound, kept map[pushReach]bool, lookup bool) ([]pushReach, error) {
	// One answer per name and one time budget for the whole push, however many targets it lists.
	var look func(host string) ([]net.IPAddr, error)
	if lookup {
		ctx, cancel := context.WithTimeout(context.Background(), pushGuardLookupBudget)
		defer cancel()
		type answer struct {
			addrs []net.IPAddr
			err   error
		}
		answers := map[string]answer{}
		look = func(host string) ([]net.IPAddr, error) {
			key := strings.ToLower(strings.TrimSuffix(host, "."))
			a, asked := answers[key]
			if !asked {
				a.addrs, a.err = pushGuardLookup(ctx, host)
				answers[key] = a
			}
			return a.addrs, a.err
		}
	}
	local, err := localRelayTargets(inbound.Id)
	if err != nil {
		return nil, err
	}
	ownPorts := nodeOwnPorts()
	ownCerts := nodeOwnCertFiles()
	var reaches []pushReach
	add := func(field string, leaf jsonLeaf) {
		reaches = append(reaches, pushReach{field: field, value: leaf.value})
	}
	streamLeaves := jsonLeaves(inbound.StreamSettings)
	// A whole object is one reach: a push keeping the type but moving the url or folder
	// is a different reach than the one this node stores.
	object := func(field string, container ...string) {
		var parts []string
		for _, l := range streamLeaves {
			if i := slices.Index(l.path, container[len(container)-1]); i >= len(container)-1 && slices.Equal(l.path[i+1-len(container):i+1], container) {
				parts = append(parts, strings.Join(l.path[i+1:], ".")+"="+l.value)
			}
		}
		slices.Sort(parts)
		reaches = append(reaches, pushReach{field: field, value: strings.Join(parts, ";")})
	}
	for _, leaf := range streamLeaves {
		lower := strings.ToLower(strings.TrimSpace(leaf.value))
		switch {
		case leaf.endsWith("hysteriasettings", "masquerade", "type") && (lower == "file" || lower == "proxy"):
			object("hysteria masquerade", "hysteriasettings", "masquerade")
		case leaf.endsWith("masterkeylog") && lower != "" && lower != "none":
			add("masterKeyLog", leaf)
		case leaf.endsWith("xdrivesettings", "service") && (lower == "local" || lower == "template" || lower == "s3"):
			object("xdrive service", "xdrivesettings")
		case leaf.endsWith("echconfiglist") && strings.Contains(lower, "://"):
			add("echConfigList", leaf)
		case slices.Contains(leaf.path, "finalmask") && leaf.endsWith("type") && lower == "realm":
			add("finalmask", leaf)
		case (leaf.endsWith("certificatefile") || leaf.endsWith("keyfile")) && lower != "" && !isNodeOwnCertFile(leaf.value, ownCerts):
			add("certificate file", leaf)
		case leaf.endsWith("realitysettings", "target") || leaf.endsWith("realitysettings", "dest"):
			if kept[pushReach{field: "REALITY target", value: leaf.value}] {
				continue
			}
			switch public, resolved := isPublicRelayTarget(leaf.value, ownPorts, true, look); {
			case !resolved:
				add("REALITY target (its name does not resolve)", leaf)
			case !public:
				add("REALITY target", leaf)
			}
		}
	}
	// Anything but an IP is a unix socket to the core, which creates "<path>.lock" and binds there.
	if listen := xrayListenHost(inbound.Listen); listen != "" && net.ParseIP(listen) == nil {
		reaches = append(reaches, pushReach{field: "listen", value: strings.SplitN(listen, ",", 2)[0]})
	}
	for _, leaf := range jsonLeaves(inbound.Settings) {
		switch {
		case leaf.endsWith("fallbacks", "[]", "dest"):
			if kept[pushReach{field: "fallback dest", value: leaf.value}] || slices.Contains(local, normalizeRelayTarget(leaf.value)) {
				continue
			}
			switch public, resolved := isPublicRelayTarget(leaf.value, ownPorts, false, look); {
			case !resolved:
				add("fallback dest (its name does not resolve)", leaf)
			case !public:
				add("fallback dest", leaf)
			}
		case leaf.endsWith("domainfronting", "ip"):
			if ip, err := netip.ParseAddr(strings.TrimSpace(leaf.value)); err == nil && isBlockedIP(net.IP(ip.AsSlice())) {
				add("domainFronting ip", leaf)
			}
		}
	}
	return reaches, nil
}

// isPublicRelayTarget reports whether a relay target dials a public host, not this one; a
// name on an own port passes only as a REALITY target on 443. resolved is false for a dead name.
func isPublicRelayTarget(target string, ownPorts []string, reality bool, look func(host string) ([]net.IPAddr, error)) (public, resolved bool) {
	host, port, ok := splitRelayTarget(target)
	if !ok || strings.Contains(host, "%") {
		return false, true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return !isBlockedIP(ip) && !isLocalInterfaceIP(ip), true
	}
	if (slices.Contains(ownPorts, port) && (!reality || port != "443")) || namesInternalHost(host) {
		return false, true
	}
	if look == nil {
		return true, true
	}
	// shortcut: the name may resolve elsewhere when Xray dials it (DNS rebinding); closing that
	// needs Xray to pin the address it was checked against.
	addrs, err := look(host)
	if err != nil || len(addrs) == 0 {
		return false, false
	}
	for _, a := range addrs {
		if isBlockedIP(a.IP) || isLocalInterfaceIP(a.IP) {
			return false, true
		}
	}
	return true, true
}

// A push waits this long in all for the node's resolver, however many new names it carries.
const pushGuardLookupBudget = 3 * time.Second

// pushGuardLookup resolves a pushed relay target name; tests replace it.
var pushGuardLookup = net.DefaultResolver.LookupIPAddr

// splitRelayTarget reads host:port as Go's dialer would, refusing a socket path and any port
// that is not a plain number ("062789" or a service name would slip past a string match).
func splitRelayTarget(target string) (host, port string, ok bool) {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "@") {
		return "", "", false
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", "", false
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 || strconv.FormatUint(n, 10) != port {
		return "", "", false
	}
	return host, port, true
}

// nodeOwnPorts lists the ports of this node's services a relay must not reach under a name:
// Xray's API and metrics listeners, the panel and the subscription server.
func nodeOwnPorts() []string {
	ports := xrayOwnPorts()
	settings := &SettingService{}
	for _, get := range []func() (int, error){settings.GetListenPort, settings.GetSubPort} {
		if port, err := get(); err == nil && port > 0 {
			ports = append(ports, strconv.Itoa(port))
		}
	}
	return ports
}

// nodeOwnCertFiles lists the certificate and key files of this node's panel and subscription
// server, the paths "Set Cert from Panel" sends to a node.
func nodeOwnCertFiles() []string {
	settings := &SettingService{}
	var files []string
	for _, get := range []func() (string, error){settings.GetCertFile, settings.GetKeyFile, settings.GetSubCertFile, settings.GetSubKeyFile} {
		if file, err := get(); err == nil && filepath.IsAbs(file) {
			files = append(files, filepath.Clean(file))
		}
	}
	return files
}

// nodeCertStores are where this node's certificates are installed: by x-ui.sh's acme flows
// and by certbot. ~/.acme.sh stays out: it also holds account keys and DNS API credentials.
var nodeCertStores = []string{"/root/cert", "/etc/letsencrypt/live"}

// isNodeOwnCertFile reports whether a pushed certificateFile or keyFile names one of this
// node's own certificates; any other path would have the panel read a file the master picks.
func isNodeOwnCertFile(path string, ownCerts []string) bool {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	return slices.Contains(ownCerts, path) || slices.ContainsFunc(nodeCertStores, func(store string) bool {
		rel, err := filepath.Rel(store, path)
		return err == nil && filepath.IsLocal(rel)
	})
}

// xrayListenHost reads a listen value as xray-core's net.ParseAddress does: one pair of
// surrounding brackets stripped, then surrounding spaces.
func xrayListenHost(listen string) string {
	if len(listen) > 1 && listen[0] == '[' && listen[len(listen)-1] == ']' {
		listen = listen[1 : len(listen)-1]
	}
	return strings.TrimSpace(listen)
}

// isLocalInterfaceIP reports whether ip is assigned to one of this host's interfaces.
func isLocalInterfaceIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// normalizeRelayTarget reduces a fallback dest to the form localRelayTargets lists: a bare
// port, a socket path, or host:port rejoined so every spelling of an address meets.
func normalizeRelayTarget(target string) string {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "@") {
		return target
	}
	if host, port, err := net.SplitHostPort(target); err == nil {
		return net.JoinHostPort(host, port)
	}
	return target
}

// localRelayTargets lists the addresses this node's other enabled inbounds listen on, the
// only local fallback dests a master generates; the core's own listeners are never among them.
func localRelayTargets(selfID int) ([]string, error) {
	var rows []model.Inbound
	if err := database.GetDB().Select("id", "listen", "port", "protocol", "stream_settings", "settings").Where("node_id IS NULL AND enable = ? AND id <> ?", true, selfID).Find(&rows).Error; err != nil {
		return nil, err
	}
	reserved := map[string]bool{}
	for _, port := range nodeOwnPorts() {
		reserved[port] = true
	}
	var targets []string
	for _, ib := range rows {
		// A fallback dials TCP: an inbound that only takes UDP leaves that TCP port to whoever binds it.
		if inboundTransports(ib.Protocol, ib.StreamSettings, ib.Settings)&transportTCP == 0 {
			continue
		}
		listen := xrayListenHost(ib.Listen)
		if strings.HasPrefix(listen, "/") || strings.HasPrefix(listen, "@") {
			targets = append(targets, strings.SplitN(listen, ",", 2)[0])
			continue
		}
		port := strconv.Itoa(ib.Port)
		if reserved[port] {
			continue
		}
		// The core dials a bare port on localhost; BuildFallbacksJSON derives 127.0.0.1 for a wildcard.
		ip := net.ParseIP(listen)
		// Another loopback address (127.0.0.2, ::1) leaves 127.0.0.1:port to whatever else binds it.
		if listen == "" || ip.IsUnspecified() || ip.Equal(net.IPv4(127, 0, 0, 1)) {
			targets = append(targets, port, net.JoinHostPort("127.0.0.1", port))
		}
		// A pushed row can carry any listen, so only an address of this host's own interfaces counts.
		if ip != nil && !ip.IsUnspecified() && isLocalInterfaceIP(ip) {
			targets = append(targets, net.JoinHostPort(listen, port))
		}
	}
	return targets, nil
}
