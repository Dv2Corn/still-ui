package xray

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// unitSocketProblem explains why the unit's sandbox cannot create ib's unix socket, or "".
func unitSocketProblem(ib InboundConfig) string {
	var listen string
	if len(ib.Listen) == 0 || json.Unmarshal(ib.Listen, &listen) != nil {
		return ""
	}
	path, _, _ := strings.Cut(listen, ",")
	if !strings.HasPrefix(path, "/") {
		return ""
	}
	clean := filepath.Clean(path)
	if slices.ContainsFunc(unitSocketFolders, func(dir string) bool { return strings.HasPrefix(clean, dir) }) {
		return ""
	}
	return fmt.Sprintf("inbound %q listens on the unix socket %s, which the still-xray service cannot create there: use a path in %s",
		ib.Tag, path, strings.Join(unitSocketFolders, " or "))
}

type sockopt struct {
	Tproxy string `json:"tproxy"`
	Mark   int64  `json:"mark"`
}

func streamSockopt(raw []byte) sockopt {
	var stream struct {
		Sockopt sockopt `json:"sockopt"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &stream)
	}
	return stream.Sockopt
}

// UnitCaps is what the still-xray service may do beyond binding low ports.
type UnitCaps struct {
	NetAdmin bool // CAP_NET_ADMIN: socket marks, TPROXY (the shipped unit grants it)
	Devices  bool // the host's /dev (PrivateDevices off): /dev/net/tun for TUN
}

// UnitBlockers lists what in cfg the still-xray service with caps cannot run: a unix socket
// outside the folders its sandbox shares, TPROXY and socket marks without CAP_NET_ADMIN, TUN
// without that and /dev/net/tun. install.sh and update.sh keep Xray in the panel when it is not empty.
func UnitBlockers(cfg *Config, caps UnitCaps) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	needs := func(what string) {
		out = append(out, what+" needs CAP_NET_ADMIN, which the still-xray service was not given (a drop-in in still-xray.service.d takes it away, or the unit is older than this panel: run still-ui update)")
	}
	for _, ib := range cfg.InboundConfigs {
		if p := unitSocketProblem(ib); p != "" {
			out = append(out, p)
		}
		if ib.Protocol == "tun" {
			if !caps.NetAdmin || !caps.Devices {
				out = append(out, fmt.Sprintf("inbound %q (TUN) needs /dev/net/tun and CAP_NET_ADMIN, which the still-xray service's sandbox does not give (PrivateDevices): keep Xray inside the panel with STILL_XRAY_UNIT=off", ib.Tag))
			}
			continue
		}
		if caps.NetAdmin {
			continue
		}
		switch so := streamSockopt(ib.StreamSettings); {
		case so.Tproxy == "tproxy":
			needs(fmt.Sprintf("inbound %q (TPROXY)", ib.Tag))
		case so.Mark != 0:
			needs(fmt.Sprintf("inbound %q (socket mark)", ib.Tag))
		}
	}
	if caps.NetAdmin || len(cfg.OutboundConfigs) == 0 {
		return out
	}
	var outbounds []struct {
		Tag            string          `json:"tag"`
		StreamSettings json.RawMessage `json:"streamSettings"`
	}
	if json.Unmarshal(cfg.OutboundConfigs, &outbounds) == nil {
		for _, ob := range outbounds {
			if streamSockopt(ob.StreamSettings).Mark != 0 {
				needs(fmt.Sprintf("outbound %q (socket mark)", ob.Tag))
			}
		}
	}
	return out
}
