package sub

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/util/link"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// Probes report every few minutes, so one verdict serves the subscription fetches in between.
const probeVerdictTTL = 30 * time.Second

var probeVerdictCache struct {
	sync.Mutex
	at       time.Time
	policy   service.ReachSubPolicy
	verdicts *service.ReachSubVerdicts
}

func resetProbeVerdictCache() {
	probeVerdictCache.Lock()
	defer probeVerdictCache.Unlock()
	probeVerdictCache.at, probeVerdictCache.verdicts = time.Time{}, nil
}

func loadProbeVerdicts(policy service.ReachSubPolicy) *service.ReachSubVerdicts {
	if policy.Mode != service.SubProbeOrderReorder && policy.Mode != service.SubProbeOrderHide {
		return nil
	}
	probeVerdictCache.Lock()
	defer probeVerdictCache.Unlock()
	if probeVerdictCache.policy == policy && time.Since(probeVerdictCache.at) < probeVerdictTTL {
		return probeVerdictCache.verdicts
	}
	verdicts, err := (&service.ReachProbeService{}).SubscriptionVerdicts(policy)
	if err != nil {
		// Without a verdict the subscription stays in its normal order.
		logger.Warning("subscription: reading probe results:", err)
		verdicts = nil
	}
	probeVerdictCache.at, probeVerdictCache.policy, probeVerdictCache.verdicts = time.Now(), policy, verdicts
	return verdicts
}

// probeVerdicts is read once per request and only for subscription bodies: the panel's own link
// and QR views and the probe jobs always see every link.
func (s *SubService) probeVerdicts() *service.ReachSubVerdicts {
	if !s.subscriptionBody {
		return nil
	}
	if !s.probeLoaded {
		s.probeLoaded = true
		s.probe = loadProbeVerdicts(s.settingService.SubProbePolicy())
	}
	return s.probe
}

// probeArrange keeps reachable panel entries in order and returns the unreachable ones as
// demoted (reorder) or hidden (hide); when every entry is down, nothing moves.
func probeArrange[T any](verdicts *service.ReachSubVerdicts, items []T, down func(T) bool) (kept, demoted, hidden []T) {
	if verdicts == nil || len(items) == 0 {
		return items, nil, nil
	}
	var up, dn []T
	for _, item := range items {
		if down(item) {
			dn = append(dn, item)
		} else {
			up = append(up, item)
		}
	}
	if len(dn) == 0 || len(up) == 0 {
		return items, nil, nil
	}
	if verdicts.Mode == service.SubProbeOrderHide {
		return up, nil, dn
	}
	return up, dn, nil
}

// subLink is one line of the raw subscription with the inbound it came from (0: external).
type subLink struct {
	link      string
	inboundID int
	remarks   remarkSpan
}

func probeLinkDown(verdicts *service.ReachSubVerdicts) func(subLink) bool {
	return func(l subLink) bool {
		if !verdicts.HasDown(l.inboundID) {
			return false
		}
		parsed, err := link.ParseLink(l.link)
		if err != nil {
			return false
		}
		host, port, ok := link.Endpoint(parsed.Outbound)
		return ok && verdicts.Unreachable(l.inboundID, host, port)
	}
}

// arrangeSubLinks splits multi-endpoint entries into lines so each endpoint is judged alone. It
// also returns the remarks of the kept lines in order, for the first-link remark (probe_remarks.go).
func arrangeSubLinks(verdicts *service.ReachSubVerdicts, entries []subLink) (kept, demoted []string, keptRemarks []remarkSpan) {
	if verdicts == nil {
		for _, e := range entries {
			kept = append(kept, e.link)
			keptRemarks = append(keptRemarks, e.remarks)
		}
		return kept, nil, keptRemarks
	}
	var lines []subLink
	for _, e := range entries {
		if !verdicts.HasDown(e.inboundID) {
			lines = append(lines, e)
			continue
		}
		split := splitLinkLines(e.link)
		remarks := splitRemarkSpan(e.remarks, len(split))
		for i, line := range split {
			lines = append(lines, subLink{line, e.inboundID, remarks[i]})
		}
	}
	keep, demote, _ := probeArrange(verdicts, lines, probeLinkDown(verdicts))
	for _, l := range keep {
		kept = append(kept, l.link)
		keptRemarks = append(keptRemarks, l.remarks)
	}
	for _, l := range demote {
		demoted = append(demoted, l.link)
	}
	return kept, demoted, keptRemarks
}

// clashProxyRender is a hosts-first Clash variant rendered in final order, with its inbound.
type clashProxyRender struct {
	inboundID int
	render    func() []map[string]any
}

// clashProxy is one Clash proxy with the inbound it came from (0: external).
type clashProxy struct {
	proxy     map[string]any
	inboundID int
	remarks   remarkSpan
}

func clashProxyDown(verdicts *service.ReachSubVerdicts) func(clashProxy) bool {
	return func(p clashProxy) bool {
		server, _ := p.proxy["server"].(string)
		port, _ := p.proxy["port"].(int)
		if f, ok := p.proxy["port"].(float64); ok {
			port = int(f)
		}
		return verdicts.Unreachable(p.inboundID, server, port)
	}
}

// clashServedRemarks lists the remarks of the panel proxies a Clash profile serves first: the
// kept ones, or for a legacy profile the kept ones it can show, else the hidden ones it falls back on.
func clashServedRemarks(kept, hidden []clashProxy, legacy bool) []remarkSpan {
	pick := func(entries []clashProxy) []remarkSpan {
		var out []remarkSpan
		for _, e := range entries {
			if !legacy || legacyClashProxy(e.proxy) != nil {
				out = append(out, e.remarks)
			}
		}
		return out
	}
	if served := pick(kept); len(served) > 0 || !legacy {
		return served
	}
	return pick(hidden)
}

func clashProxyMaps(entries []clashProxy) []map[string]any {
	if len(entries) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.proxy)
	}
	return out
}

// keepReferencedProbeHidden puts back the hidden proxies that local custom routing names directly
// (a remote source cannot name proxies), so no group or rule points at a missing proxy.
func keepReferencedProbeHidden(config map[string]any, visible, hidden []map[string]any) {
	// Custom routing may replace the proxies list itself; then it is the admin's list.
	current, ok := config["proxies"].([]map[string]any)
	if len(hidden) == 0 || !ok || len(current) != len(visible) || (len(visible) > 0 && &current[0] != &visible[0]) {
		return
	}
	config["proxies"] = restoreReferencedHidden(config, visible, hidden)
}

func restoreReferencedHidden(config map[string]any, visible, hidden []map[string]any) []map[string]any {
	referenced := map[string]bool{}
	groups, _ := asAnySlice(config["proxy-groups"])
	for _, value := range groups {
		group, _ := value.(map[string]any)
		refs, _ := asAnySlice(group["proxies"])
		for _, ref := range refs {
			if name, ok := ref.(string); ok {
				referenced[name] = true
			}
		}
	}
	providers, _ := config["rule-providers"].(map[string]any)
	for _, value := range providers {
		if provider, ok := value.(map[string]any); ok {
			if via, ok := provider["proxy"].(string); ok {
				referenced[via] = true
			}
		}
	}
	rules, _ := asAnySlice(config["rules"])
	for _, value := range rules {
		rule, _ := value.(string)
		for _, part := range strings.Split(rule, ",") {
			referenced[strings.TrimSpace(part)] = true
		}
	}
	taken := map[string]bool{}
	// A reference to a group's name (PROXY, Auto, a template's own) means that group.
	for _, value := range groups {
		if name := clashProxyGroupName(value); name != "" {
			taken[name] = true
		}
	}
	for _, proxy := range visible {
		name, _ := proxy["name"].(string)
		taken[name] = true
	}
	out := slices.Clone(visible)
	for _, proxy := range hidden {
		// A hidden proxy sharing a visible one's name is not what the reference resolves to.
		if name, _ := proxy["name"].(string); referenced[name] && !taken[name] {
			taken[name] = true
			out = append(out, proxy)
		}
	}
	return out
}

// jsonConfigDown judges one JSON subscription document by the endpoint of its "proxy" outbound.
func jsonConfigDown(verdicts *service.ReachSubVerdicts, inboundID int, config json_util.RawMessage) bool {
	if !verdicts.HasDown(inboundID) {
		return false
	}
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if json.Unmarshal(config, &doc) != nil {
		return false
	}
	for _, outbound := range doc.Outbounds {
		if outbound["tag"] != "proxy" {
			continue
		}
		host, port, ok := link.Endpoint(outbound)
		return ok && verdicts.Unreachable(inboundID, host, port)
	}
	return false
}

// arrangeJSONEntries hides unreachable documents from the inbound entries (so balancers never
// name them) or marks them to be moved to the end of the subscription.
func arrangeJSONEntries(verdicts *service.ReachSubVerdicts, entries []subConfigEntry) []subConfigEntry {
	if verdicts == nil {
		return entries
	}
	type ref struct{ entry, config int }
	var refs []ref
	for i, entry := range entries {
		for j := range entry.configs {
			refs = append(refs, ref{i, j})
		}
	}
	_, demoted, hidden := probeArrange(verdicts, refs, func(r ref) bool {
		return jsonConfigDown(verdicts, entries[r.entry].id, entries[r.entry].configs[r.config])
	})
	for _, r := range demoted {
		entry := &entries[r.entry]
		if entry.probeDemoted == nil {
			entry.probeDemoted = make([]bool, len(entry.configs))
		}
		entry.probeDemoted[r.config] = true
	}
	if len(hidden) == 0 {
		return entries
	}
	drop := map[ref]bool{}
	for _, r := range hidden {
		drop[r] = true
	}
	out := entries[:0]
	for i, entry := range entries {
		configs := entry.configs[:0:0]
		var remarks []remarkSpan
		for j, config := range entry.configs {
			if !drop[ref{i, j}] {
				configs = append(configs, config)
				if j < len(entry.remarks) {
					remarks = append(remarks, entry.remarks[j])
				}
			}
		}
		if len(configs) > 0 {
			entry.configs, entry.remarks = configs, remarks
			out = append(out, entry)
		}
	}
	return out
}

// subLinkRender is a hosts-first raw link rendered in final order, with the inbound it came from.
type subLinkRender struct {
	inboundID int
	render    func() string
}
