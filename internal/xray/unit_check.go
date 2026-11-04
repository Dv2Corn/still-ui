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

// UnitBlockers lists what in cfg the still-xray service cannot run: a unix socket outside the
// folders its sandbox shares, and, unless the unit was given CAP_NET_ADMIN by a local drop-in
// (netAdmin), TPROXY, TUN and socket marks, which need that capability. install.sh and
// update.sh keep Xray inside the panel when this is not empty, instead of breaking the inbound.
func UnitBlockers(cfg *Config, netAdmin bool) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	needs := func(what string) {
		out = append(out, what+" needs CAP_NET_ADMIN, which the still-xray service does not have (grant it in a drop-in in still-xray.service.d, or keep Xray inside the panel)")
	}
	for _, ib := range cfg.InboundConfigs {
		if p := unitSocketProblem(ib); p != "" {
			out = append(out, p)
		}
		if netAdmin {
			continue
		}
		so := streamSockopt(ib.StreamSettings)
		switch {
		case ib.Protocol == "tun":
			needs(fmt.Sprintf("inbound %q (TUN)", ib.Tag))
		case so.Tproxy == "tproxy":
			needs(fmt.Sprintf("inbound %q (TPROXY)", ib.Tag))
		case so.Mark != 0:
			needs(fmt.Sprintf("inbound %q (socket mark)", ib.Tag))
		}
	}
	if netAdmin || len(cfg.OutboundConfigs) == 0 {
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
