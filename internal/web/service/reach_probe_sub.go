package service

import (
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// Values of the subProbeOrder setting: what subscriptions do with links the probes report down.
const (
	SubProbeOrderOff     = "off"
	SubProbeOrderReorder = "reorder"
	SubProbeOrderHide    = "hide"
)

const (
	SubProbeMinAgentsMax        = 10
	SubProbeStaleMinutesMin     = 5
	SubProbeStaleMinutesMax     = 1440
	SubProbeStaleMinutesDefault = 30
)

// States of a probe target in subscriptions, shown on the Probes page.
const (
	ReachSubDemoted     = "demoted"
	ReachSubHidden      = "hidden"
	ReachSubUnconfirmed = "unconfirmed"
)

// Why an agent's results are left out of the subscription verdict.
const (
	ReachSubIgnoredSilent     = "silent"
	ReachSubIgnoredAllFailing = "allFailing"
)

// ReachSubPolicy is the subscription side of the probe settings.
type ReachSubPolicy struct {
	Mode         string
	MinAgents    int
	StaleMinutes int
}

func (p ReachSubPolicy) active() bool {
	return p.Mode == SubProbeOrderReorder || p.Mode == SubProbeOrderHide
}

// ReachSubVerdict is the combined view of every agent on one endpoint of one inbound.
type ReachSubVerdict struct {
	Unreachable bool
	FailAgents  int
	NeedAgents  int
}

// ReachSubVerdicts holds the verdict of every endpoint at least one usable agent reports down.
// A nil value, or an endpoint it does not list, is reachable.
type ReachSubVerdicts struct {
	Mode    string
	byKey   map[string]ReachSubVerdict
	ignored map[int]string
}

// Unreachable reports whether subscriptions treat the inbound's endpoint host:port as down.
func (v *ReachSubVerdicts) Unreachable(inboundID int, host string, port int) bool {
	if v == nil || len(v.byKey) == 0 || inboundID <= 0 || port <= 0 {
		return false
	}
	return v.byKey[reachSubKey(inboundID, host, port)].Unreachable
}

// HasDown reports whether any endpoint of the inbound is down, so callers can skip parsing links.
func (v *ReachSubVerdicts) HasDown(inboundID int) bool {
	if v == nil {
		return false
	}
	prefix := strconv.Itoa(inboundID) + "@"
	for key, verdict := range v.byKey {
		if verdict.Unreachable && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (v *ReachSubVerdicts) forTargetKey(targetKey string) (ReachSubVerdict, bool) {
	if v == nil {
		return ReachSubVerdict{}, false
	}
	key, ok := normalizeReachTargetKey(targetKey)
	if !ok {
		return ReachSubVerdict{}, false
	}
	verdict, ok := v.byKey[key]
	return verdict, ok
}

func reachSubKey(inboundID int, host string, port int) string {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	return strconv.Itoa(inboundID) + "@" + net.JoinHostPort(host, strconv.Itoa(port))
}

// normalizeReachTargetKey turns a stored "inbound@host:port" key into the form links are matched by.
func normalizeReachTargetKey(targetKey string) (string, bool) {
	idText, endpoint, ok := strings.Cut(targetKey, "@")
	if !ok {
		return "", false
	}
	id, err := strconv.Atoi(idText)
	if err != nil || id <= 0 {
		return "", false
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return "", false
	}
	return reachSubKey(id, host, port), true
}

func normalizeSubProbeSettings(all *entity.AllSetting) error {
	switch all.SubProbeOrder {
	case "":
		all.SubProbeOrder = SubProbeOrderOff
	case SubProbeOrderOff, SubProbeOrderReorder, SubProbeOrderHide:
	default:
		return common.NewError("subscription probe order must be off, reorder or hide")
	}
	if all.SubProbeMinAgents < 0 || all.SubProbeMinAgents > SubProbeMinAgentsMax {
		return common.NewErrorf("probe agents to confirm must be from 0 (automatic) to %d", SubProbeMinAgentsMax)
	}
	if all.SubProbeStaleMinutes == 0 {
		all.SubProbeStaleMinutes = SubProbeStaleMinutesDefault
	}
	if all.SubProbeStaleMinutes < SubProbeStaleMinutesMin || all.SubProbeStaleMinutes > SubProbeStaleMinutesMax {
		return common.NewErrorf("probe results expire after %d to %d minutes", SubProbeStaleMinutesMin, SubProbeStaleMinutesMax)
	}
	return nil
}

// SubProbePolicy reads the subscription probe settings, with out-of-range values clamped.
func (s *SettingService) SubProbePolicy() ReachSubPolicy {
	mode, _ := s.getString("subProbeOrder")
	if mode != SubProbeOrderReorder && mode != SubProbeOrderHide {
		return ReachSubPolicy{Mode: SubProbeOrderOff}
	}
	minAgents, _ := s.getInt("subProbeMinAgents")
	stale, err := s.getInt("subProbeStaleMinutes")
	if err != nil {
		stale = SubProbeStaleMinutesDefault
	}
	return ReachSubPolicy{
		Mode:         mode,
		MinAgents:    clampInt(minAgents, 0, SubProbeMinAgentsMax),
		StaleMinutes: clampInt(stale, SubProbeStaleMinutesMin, SubProbeStaleMinutesMax),
	}
}

// SubscriptionVerdicts: an endpoint is down when enough fresh agents hold it in the alert state
// "fail" (ReachProbeFailThreshold checks in a row); silent and all-failing agents are left out.
func (s *ReachProbeService) SubscriptionVerdicts(policy ReachSubPolicy) (*ReachSubVerdicts, error) {
	out := &ReachSubVerdicts{Mode: policy.Mode, byKey: map[string]ReachSubVerdict{}, ignored: map[int]string{}}
	if !policy.active() {
		return out, nil
	}
	var probes []*model.ReachProbe
	db := database.GetDB()
	if err := db.Where("enabled = ?", true).Order("id asc").Find(&probes).Error; err != nil {
		return nil, err
	}
	now := s.clock()
	since := now.Add(-time.Duration(policy.StaleMinutes) * time.Minute).UnixMilli()
	coverage := map[int]int{}
	failAgents := map[string]int{}
	for _, probe := range probes {
		for _, id := range uniqueInts(probe.InboundIds) {
			coverage[id]++
		}
		if probe.LastSeenAt < since {
			out.ignored[probe.Id] = ReachSubIgnoredSilent
			continue
		}
		down, fresh, err := freshReachFailures(probe, since)
		if err != nil {
			return nil, err
		}
		// Every target failing at once is more likely the agent's own network or test URL.
		if fresh >= 2 && len(down) == fresh {
			out.ignored[probe.Id] = ReachSubIgnoredAllFailing
			continue
		}
		for _, key := range down {
			failAgents[key]++
		}
	}
	for key, fails := range failAgents {
		id, _ := reachTargetInbound(key)
		need := policy.MinAgents
		if need <= 0 {
			need = 2
			if coverage[id] <= 1 {
				need = 1
			}
		}
		out.byKey[key] = ReachSubVerdict{Unreachable: fails >= need, FailAgents: fails, NeedAgents: need}
	}
	return out, nil
}

// freshReachFailures returns the normalized keys of the probe's targets that are down and the
// number of its targets checked since the cut-off; the direct check and dropped inbounds are skipped.
func freshReachFailures(probe *model.ReachProbe, since int64) ([]string, int, error) {
	db := database.GetDB()
	var lastChecks []struct {
		TargetKey string
		Last      int64
	}
	if err := db.Model(&model.ReachProbeResult{}).Select("target_key, MAX(at) AS last").
		Where("probe_id = ? AND at >= ?", probe.Id, since).Group("target_key").Scan(&lastChecks).Error; err != nil {
		return nil, 0, err
	}
	fresh := map[string]bool{}
	for _, c := range lastChecks {
		if id, ok := reachTargetInbound(c.TargetKey); ok && c.TargetKey != ReachDirectKey && slices.Contains(probe.InboundIds, id) {
			fresh[c.TargetKey] = true
		}
	}
	var states []model.ReachProbeTarget
	if err := db.Where("probe_id = ?", probe.Id).Order("target_key asc").Find(&states).Error; err != nil {
		return nil, 0, err
	}
	var down []string
	for _, st := range states {
		if !fresh[st.TargetKey] || st.Status != ReachStatusFail || st.ConsecutiveFails < ReachProbeFailThreshold {
			continue
		}
		if key, ok := normalizeReachTargetKey(st.TargetKey); ok && !slices.Contains(down, key) {
			down = append(down, key)
		}
	}
	return down, len(fresh), nil
}

// annotateSubscriptionState marks each target with what subscriptions currently do with it.
func annotateSubscriptionState(views []ReachProbeView, verdicts *ReachSubVerdicts) {
	if verdicts == nil {
		return
	}
	for i := range views {
		views[i].SubIgnored = verdicts.ignored[views[i].Id]
		for j := range views[i].Targets {
			target := &views[i].Targets[j]
			verdict, ok := verdicts.forTargetKey(target.Key)
			if !ok {
				continue
			}
			target.SubFailAgents, target.SubNeedAgents = verdict.FailAgents, verdict.NeedAgents
			switch {
			case !verdict.Unreachable:
				target.SubState = ReachSubUnconfirmed
			case verdicts.Mode == SubProbeOrderHide:
				target.SubState = ReachSubHidden
			default:
				target.SubState = ReachSubDemoted
			}
		}
	}
}
