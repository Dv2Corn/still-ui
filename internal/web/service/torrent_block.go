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
	Apply() error
	Ensure() error
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
	torrentBlockMu.Lock()
	var err error
	if on {
		err = torrentBlockHost.Apply()
	} else {
		err = torrentBlockHost.Remove()
	}
	recordTorrentBlockErr(err)
	torrentBlockMu.Unlock()
	return s.Status(), nil
}

// EnsureHostFilter reloads the table after a reboot or a foreign ruleset flush.
func (s *TorrentBlockService) EnsureHostFilter() {
	if !s.Enabled() {
		return
	}
	torrentBlockMu.Lock()
	defer torrentBlockMu.Unlock()
	recordTorrentBlockErr(torrentBlockHost.Ensure())
}

// recordTorrentBlockErr keeps the last host error for the UI; caller holds torrentBlockMu.
func recordTorrentBlockErr(err error) {
	prev := torrentBlockErr
	torrentBlockErr = ""
	if err != nil {
		torrentBlockErr = err.Error()
		if torrentBlockErr != prev {
			logger.Warning("torrent block: nftables filter not applied: ", err)
		}
	}
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
	blackhole := ""
	tags := map[string]bool{}
	for _, o := range outbounds {
		tag, _ := o["tag"].(string)
		tags[tag] = true
		if p, _ := o["protocol"].(string); p == "blackhole" && blackhole == "" && tag != "" {
			blackhole = tag
		}
	}
	if blackhole == "" {
		blackhole = torrentBlockOutboundTag
		for i := 2; tags[blackhole]; i++ {
			blackhole = torrentBlockOutboundTag + "-" + strconv.Itoa(i)
		}
		outbounds = append(outbounds, map[string]any{"tag": blackhole, "protocol": "blackhole"})
		raw, err := json.Marshal(outbounds)
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
