package service

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/health"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

const (
	NodeTrafficActionNotify  = "notify"
	NodeTrafficActionDisable = "disable"

	nodeTrafficMultiplierMin = 0.1
	nodeTrafficMultiplierMax = 100

	nodeTrafficLevelUnder = 0
	nodeTrafficLevelWarn  = 1
	nodeTrafficLevelLimit = 2
)

// NodeTrafficLimitRequest is the write contract of a node's traffic quota and accounting
// multiplier. A zero limit means no limit; the multiplier only affects client totals.
type NodeTrafficLimitRequest struct {
	TrafficLimitBytes  int64   `json:"trafficLimitBytes" form:"trafficLimitBytes" example:"1099511627776"`
	TrafficResetDay    int     `json:"trafficResetDay" form:"trafficResetDay" example:"1"`
	TrafficWarnPercent int     `json:"trafficWarnPercent" form:"trafficWarnPercent" example:"80"`
	TrafficLimitAction string  `json:"trafficLimitAction" form:"trafficLimitAction" example:"notify"`
	TrafficMultiplier  float64 `json:"trafficMultiplier" form:"trafficMultiplier" example:"1"`
}

func (r *NodeTrafficLimitRequest) validate() error {
	if r == nil {
		return common.NewError("node traffic limit request is required")
	}
	if r.TrafficLimitBytes < 0 || r.TrafficLimitBytes > database.TrafficMax {
		return common.NewError("traffic limit must be between 0 and " + strconv.FormatInt(database.TrafficMax, 10) + " bytes")
	}
	if r.TrafficResetDay < 1 || r.TrafficResetDay > 31 {
		return common.NewError("traffic reset day must be 1-31")
	}
	if r.TrafficWarnPercent < 0 || r.TrafficWarnPercent > 100 {
		return common.NewError("traffic warning threshold must be 0-100 percent")
	}
	if r.TrafficLimitAction != NodeTrafficActionNotify && r.TrafficLimitAction != NodeTrafficActionDisable {
		return common.NewError("traffic limit action must be notify or disable")
	}
	if math.IsNaN(r.TrafficMultiplier) || r.TrafficMultiplier < nodeTrafficMultiplierMin || r.TrafficMultiplier > nodeTrafficMultiplierMax {
		return common.NewError("traffic multiplier must be between 0.1 and 100")
	}
	return nil
}

// SetTrafficLimit stores a node's quota settings. It never marks the node dirty: the
// quota is the master's policy and nothing about it is pushed to the node.
func (s *NodeService) SetTrafficLimit(id int, req *NodeTrafficLimitRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	res := database.GetDB().Model(model.Node{}).Where("id = ?", id).Updates(map[string]any{
		"traffic_limit_bytes":  req.TrafficLimitBytes,
		"traffic_reset_day":    req.TrafficResetDay,
		"traffic_warn_percent": req.TrafficWarnPercent,
		"traffic_limit_action": req.TrafficLimitAction,
		"traffic_multiplier":   req.TrafficMultiplier,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// effectiveTrafficMultiplier treats a missing or out-of-range stored value as 1, so a
// hand-edited row can never zero or explode client totals.
func effectiveTrafficMultiplier(m float64) float64 {
	if math.IsNaN(m) || m < nodeTrafficMultiplierMin || m > nodeTrafficMultiplierMax {
		return 1
	}
	return m
}

// scaleNodeTraffic is the billed share of v raw bytes; rounding is per merged delta.
func scaleNodeTraffic(v int64, m float64) int64 {
	if v <= 0 || m == 1 {
		return v
	}
	scaled := math.Round(float64(v) * m)
	if scaled >= float64(database.TrafficMax) {
		return database.TrafficMax
	}
	return int64(scaled)
}

// addTrafficClamped adds a non-negative delta without overflowing past TrafficMax.
func addTrafficClamped(total, delta int64) int64 {
	if delta <= 0 {
		return total
	}
	if delta >= database.TrafficMax-total {
		return database.TrafficMax
	}
	return total + delta
}

// nodeTrafficPeriodStart is the midnight the current period began: the reset day of this
// month, or of the last one, clamped to the month's last day (31 → 30 April, 28 February).
func nodeTrafficPeriodStart(now time.Time, day int, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	day = min(max(day, 1), 31)
	local := now.In(loc)
	start := calendarDay(local.Year(), local.Month(), day, loc)
	if start.After(local) {
		year, month := local.Year(), local.Month()-1
		if month < time.January {
			year, month = year-1, time.December
		}
		start = calendarDay(year, month, day, loc)
	}
	return start
}

// nodeTrafficNextReset is when the running period ends (unix ms); 0 for a transitive
// projection, which has no quota of its own.
func nodeTrafficNextReset(n *model.Node, loc *time.Location) int64 {
	if n.Transitive || n.Id == 0 {
		return 0
	}
	return nextCalendarRenewal(time.Now(), n.TrafficResetDay, loc).UnixMilli()
}

// nodeTrafficLevel is where a node's usage stands against its quota.
func nodeTrafficLevel(n *model.Node) int {
	if n.TrafficLimitBytes <= 0 {
		return nodeTrafficLevelUnder
	}
	if n.TrafficUsed >= n.TrafficLimitBytes {
		return nodeTrafficLevelLimit
	}
	if n.TrafficWarnPercent > 0 && float64(n.TrafficUsed) >= float64(n.TrafficLimitBytes)*float64(n.TrafficWarnPercent)/100 {
		return nodeTrafficLevelWarn
	}
	return nodeTrafficLevelUnder
}

func nodeTrafficPercent(n *model.Node) int {
	if n.TrafficLimitBytes <= 0 {
		return 0
	}
	return int(math.Floor(float64(n.TrafficUsed) * 100 / float64(n.TrafficLimitBytes)))
}

func (s *NodeService) trafficLocation() *time.Location {
	loc, err := (&SettingService{}).GetTimeLocation()
	if err != nil || loc == nil {
		return time.Local
	}
	return loc
}

// EvaluateTrafficLimits rolls finished periods over, applies the limit action and
// announces level changes. Alert level and disabled inbounds are stored, so a panel
// restart neither repeats an alert nor forgets what to turn back on.
func (s *NodeService) EvaluateTrafficLimits(now time.Time) {
	var nodes []model.Node
	if err := database.GetDB().Model(model.Node{}).Find(&nodes).Error; err != nil {
		logger.Warning("node traffic limit: load nodes failed:", err)
		return
	}
	loc := s.trafficLocation()
	for i := range nodes {
		if err := s.evaluateTrafficLimit(&nodes[i], now, loc); err != nil {
			logger.Warningf("node traffic limit: %s: %v", nodes[i].Name, err)
		}
	}
}

func (s *NodeService) evaluateTrafficLimit(n *model.Node, now time.Time, loc *time.Location) error {
	start := nodeTrafficPeriodStart(now, n.TrafficResetDay, loc).UnixMilli()
	if n.TrafficPeriodStart < start {
		if err := s.rollTrafficPeriod(n, start); err != nil {
			return err
		}
	}

	level := nodeTrafficLevel(n)
	enforce := level == nodeTrafficLevelLimit && n.TrafficLimitAction == NodeTrafficActionDisable
	switch {
	case enforce && !n.TrafficLimited:
		if err := s.disableNodeInboundsForLimit(n); err != nil {
			return err
		}
	case !enforce && n.TrafficLimited:
		if err := s.restoreNodeInboundsAfterLimit(n); err != nil {
			return err
		}
	}

	if level == n.TrafficAlertLevel {
		return nil
	}
	res := database.GetDB().Model(model.Node{}).
		Where("id = ? AND traffic_alert_level = ?", n.Id, n.TrafficAlertLevel).
		Update("traffic_alert_level", level)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	phase := health.Raised
	if level < n.TrafficAlertLevel {
		phase = health.Resolved
	}
	n.TrafficAlertLevel = level
	// A limit the admin removed needs no "back under" message.
	if phase == health.Raised || n.TrafficLimitBytes > 0 {
		publishNodeTrafficAlert(n, phase, level, now, loc)
	}
	return nil
}

// rollTrafficPeriod zeroes the usage of a finished period. The first evaluation of a
// node only stamps the period: what it counted so far belongs to the current one.
func (s *NodeService) rollTrafficPeriod(n *model.Node, start int64) error {
	first := n.TrafficPeriodStart == 0
	return submitTrafficWrite(func() error {
		updates := map[string]any{"traffic_period_start": start}
		if !first {
			updates["traffic_used"] = 0
		}
		res := database.GetDB().Model(model.Node{}).
			Where("id = ? AND traffic_period_start = ?", n.Id, n.TrafficPeriodStart).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			n.TrafficPeriodStart = start
			if !first {
				n.TrafficUsed = 0
			}
		}
		return nil
	})
}

// disableNodeInboundsForLimit records the node's enabled inbounds before turning them
// off, so a crash midway still lets the reset turn every one of them back on.
func (s *NodeService) disableNodeInboundsForLimit(n *model.Node) error {
	db := database.GetDB()
	var ids []int
	if err := db.Model(model.Inbound{}).
		Where("node_id = ? AND enable = ?", n.Id, true).
		Order("id").Pluck("id", &ids).Error; err != nil {
		return err
	}
	res := db.Model(&model.Node{}).
		Where("id = ? AND traffic_limited = ?", n.Id, false).
		Updates(map[string]any{"traffic_limited": true, "traffic_disabled_inbounds": inboundIdList(ids)})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	n.TrafficLimited = true
	n.TrafficDisabledInbounds = ids
	logger.Infof("node traffic limit: %s reached its limit, turning off %d inbound(s)", n.Name, len(ids))
	inboundService := &InboundService{}
	for _, id := range ids {
		if _, err := inboundService.SetInboundEnable(id, false); err != nil {
			logger.Warningf("node traffic limit: turn off inbound %d of %s failed: %v", id, n.Name, err)
		}
	}
	return nil
}

// restoreNodeInboundsAfterLimit turns back on what the limit turned off; an inbound
// deleted or moved to another node since then is left alone.
func (s *NodeService) restoreNodeInboundsAfterLimit(n *model.Node) error {
	db := database.GetDB()
	var ids []int
	if len(n.TrafficDisabledInbounds) > 0 {
		if err := db.Model(model.Inbound{}).
			Where("id IN ? AND node_id = ? AND enable = ?", n.TrafficDisabledInbounds, n.Id, false).
			Order("id").Pluck("id", &ids).Error; err != nil {
			return err
		}
	}
	inboundService := &InboundService{}
	for _, id := range ids {
		if _, err := inboundService.SetInboundEnable(id, true); err != nil {
			// Left limited: the next evaluation retries the ones still off.
			return err
		}
	}
	if err := db.Model(&model.Node{}).Where("id = ?", n.Id).
		Updates(map[string]any{"traffic_limited": false, "traffic_disabled_inbounds": inboundIdList(nil)}).Error; err != nil {
		return err
	}
	logger.Infof("node traffic limit: %s is under its limit again, turned %d inbound(s) back on", n.Name, len(ids))
	n.TrafficLimited = false
	n.TrafficDisabledInbounds = nil
	return nil
}

// inboundIdList is the stored form of the json-serialized id column for a map update.
func inboundIdList(ids []int) string {
	if ids == nil {
		ids = []int{}
	}
	out, _ := json.Marshal(ids)
	return string(out)
}

func publishNodeTrafficAlert(n *model.Node, phase health.Phase, level int, now time.Time, loc *time.Location) {
	levelName := "under"
	switch level {
	case nodeTrafficLevelWarn:
		levelName = "warn"
	case nodeTrafficLevelLimit:
		levelName = "limit"
	}
	next := nextCalendarRenewal(now, n.TrafficResetDay, loc)
	params := map[string]string{
		"Name":      n.Name,
		"Level":     levelName,
		"Used":      common.FormatTraffic(n.TrafficUsed),
		"Limit":     common.FormatTraffic(n.TrafficLimitBytes),
		"Percent":   strconv.Itoa(nodeTrafficPercent(n)),
		"Threshold": strconv.Itoa(n.TrafficWarnPercent),
		"Reset":     next.Format("2006-01-02"),
		"Action":    n.TrafficLimitAction,
	}
	logger.Infof("node traffic alert %s %s (%s)", levelName, phase, n.Name)
	if eventBus == nil {
		return
	}
	eventBus.Publish(eventbus.Event{
		Type:      eventbus.EventNodeTraffic,
		Source:    n.Name,
		Timestamp: now,
		Data:      &eventbus.HealthAlertData{Phase: string(phase), Since: now, Params: params},
	})
}
