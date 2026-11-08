package service

import (
	"encoding/json"
	"strconv"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/torrentblock"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const torrentBlockSettingKey = "torrentBlock"

// torrentBlockOutboundTag names the blackhole added when the template has none.
const torrentBlockOutboundTag = "torrent-block"

// TorrentBlockStatus is the panel toggle plus what the host filter is doing.
type TorrentBlockStatus struct {
	Enabled bool `json:"enabled"`
	torrentblock.Status
	Error string `json:"error,omitempty"`
}

// hostFilter is the nftables side; swapped out in tests.
type hostFilter interface {
	Apply(protected []uint16) error
	Ensure(protected []uint16) error
	Remove() error
	Status() torrentblock.Status
}

var (
	torrentBlockHost hostFilter = torrentblock.Default()
	torrentBlockMu   sync.Mutex
	torrentBlockErr  string
)

// TorrentBlockService turns "Block torrents for clients" on and off.
type TorrentBlockService struct {
	settingService SettingService
	inboundService InboundService
	nodeService    NodeService
}

// protectedPorts are this server's own service ports plus node panel ports: a
// BitTorrent-looking answer from one of them is never learned as a peer.
func (s *TorrentBlockService) protectedPorts() []uint16 {
	var ports []uint16
	if p, err := s.settingService.GetListenPort(); err == nil {
		ports = appendPort(ports, p)
	}
	if p, err := s.settingService.GetSubPort(); err == nil {
		ports = appendPort(ports, p)
	}
	if inbounds, err := s.inboundService.GetAllInbounds(); err == nil {
		for _, in := range inbounds {
			ports = appendPort(ports, in.Port)
		}
	}
	if nodes, err := s.nodeService.GetAll(); err == nil {
		for _, n := range nodes {
			ports = appendPort(ports, n.Port)
		}
	}
	return ports
}

// Enabled reports the stored toggle; a read error counts as off.
func (s *TorrentBlockService) Enabled() bool {
	on, err := s.settingService.getBool(torrentBlockSettingKey)
	return err == nil && on
}

// Status is safe to poll: it never changes the host.
func (s *TorrentBlockService) Status() TorrentBlockStatus {
	torrentBlockMu.Lock()
	defer torrentBlockMu.Unlock()
	return TorrentBlockStatus{Enabled: s.Enabled(), Status: torrentBlockHost.Status(), Error: torrentBlockErr}
}

// SetEnabled stores the toggle and converges the host filter. The Xray half follows
// from the next config build, so the caller restarts Xray.
func (s *TorrentBlockService) SetEnabled(on bool) (TorrentBlockStatus, error) {
	if err := s.settingService.setString(torrentBlockSettingKey, strconv.FormatBool(on)); err != nil {
		return TorrentBlockStatus{}, err
	}
	var protected []uint16
	if on {
		protected = s.protectedPorts()
	}
	torrentBlockMu.Lock()
	var err error
	if on {
		err = torrentBlockHost.Apply(protected)
	} else {
		err = torrentBlockHost.Remove()
	}
	recordHostErr(&torrentBlockErr, "torrent block", err)
	torrentBlockMu.Unlock()
	return s.Status(), nil
}

// EnsureHostFilter reloads the table after a reboot or a foreign ruleset flush, and keeps
// its protected ports in step with the inbounds.
func (s *TorrentBlockService) EnsureHostFilter() {
	if !s.Enabled() {
		return
	}
	protected := s.protectedPorts()
	torrentBlockMu.Lock()
	defer torrentBlockMu.Unlock()
	recordHostErr(&torrentBlockErr, "torrent block", torrentBlockHost.Ensure(protected))
}

// injectTorrentBlock routes sniffed BitTorrent to a blackhole ahead of every other rule
// and turns on content sniffing where it is off, since only sniffing tags the protocol.
func injectTorrentBlock(cfg *xray.Config, clientInbounds map[string]struct{}) {
	var routing map[string]any
	if len(cfg.RouterConfig) > 0 && json.Unmarshal(cfg.RouterConfig, &routing) != nil {
		logger.Warning("torrent block: routing section unreadable, skipping the Xray rule")
		return
	}
	if routing == nil {
		routing = map[string]any{}
	}
	var outbounds []map[string]any
	if len(cfg.OutboundConfigs) > 0 && json.Unmarshal(cfg.OutboundConfigs, &outbounds) != nil {
		logger.Warning("torrent block: outbounds unreadable, skipping the Xray rule")
		return
	}
	blackhole, withBlackhole := ensureBlackholeOutbound(outbounds, torrentBlockOutboundTag)
	if len(withBlackhole) != len(outbounds) {
		raw, err := json.Marshal(withBlackhole)
		if err != nil {
			return
		}
		cfg.OutboundConfigs = json_util.RawMessage(raw)
	}
	rules, _ := routing["rules"].([]any)
	rule := map[string]any{"type": "field", "protocol": []any{"bittorrent"}, "outboundTag": blackhole}
	routing["rules"] = append([]any{rule}, rules...)
	raw, err := json.Marshal(routing)
	if err != nil {
		return
	}
	cfg.RouterConfig = json_util.RawMessage(raw)

	for i := range cfg.InboundConfigs {
		if _, ok := clientInbounds[cfg.InboundConfigs[i].Tag]; ok {
			cfg.InboundConfigs[i].Sniffing = sniffingForTorrentBlock(cfg.InboundConfigs[i].Sniffing)
		}
	}
}

// sniffingForTorrentBlock enables content sniffing without overriding destinations
// when it is off; an inbound that already reads content keeps its own settings.
func sniffingForTorrentBlock(raw json_util.RawMessage) json_util.RawMessage {
	sniffing := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &sniffing) != nil {
		sniffing = map[string]any{}
	}
	enabled, _ := sniffing["enabled"].(bool)
	metadataOnly, _ := sniffing["metadataOnly"].(bool)
	if enabled && !metadataOnly {
		return raw
	}
	if !enabled {
		sniffing["destOverride"] = []any{}
		sniffing["routeOnly"] = true
	}
	sniffing["enabled"] = true
	sniffing["metadataOnly"] = false
	out, err := json.Marshal(sniffing)
	if err != nil {
		return raw
	}
	return json_util.RawMessage(out)
}
