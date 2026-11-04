package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/util/link"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
)

const (
	ReachProbeDefaultTestURL  = "https://speed.cloudflare.com/__down?bytes=262144"
	ReachProbeDefaultInterval = 600
	reachProbeMinInterval     = 60
	reachProbeMaxInterval     = 86400
	reachProbeTokenLength     = 48
	reachProbeClientPrefix    = "still-probe-"
	reachProbeMaxNameLen      = 64
	reachProbeMaxURLLen       = 512
	reachProbeMaxInbounds     = 32
	reachProbeMaxLinks        = 4
	reachProbeMaxTargets      = reachProbeMaxInbounds*reachProbeMaxLinks + 1
	reachProbeMaxErrorLen     = 200
	reachProbeMaxVersionLen   = 32
	// A target is reported down only after this many failed checks in a row:
	// a single lost check on a mobile network is noise, two are an outage.
	ReachProbeFailThreshold = 2
	// A probe is stale after missing this many of its own intervals.
	ReachProbeStaleFactor = 3
	reachProbeHistory     = 7 * 24 * time.Hour
	// ReachDirectKey is the target key of the agent's check without the VPN.
	ReachDirectKey = "direct"
)

const (
	ReachStatusUnknown = "unknown"
	ReachStatusOk      = "ok"
	ReachStatusFail    = "fail"
)

var reachProbeProtocols = map[model.Protocol]bool{
	model.VLESS: true, model.VMESS: true, model.Trojan: true, model.Shadowsocks: true, model.Hysteria: true,
}

// ReachProbeService manages external reachability probes: their tokens, the
// panel-managed client they connect as, the jobs they fetch and the results
// they post back.
type ReachProbeService struct {
	inboundService InboundService
	clientService  ClientService
	// Seams for tests; nil means the real clock and the shared event bus.
	now     func() time.Time
	publish func(eventbus.Event)
}

type ReachProbeInput struct {
	Name string `json:"name" example:"home-mobile"`
	// Enabled is read by Update only; omitted keeps the probe's current state.
	Enabled     *bool  `json:"enabled,omitempty" example:"true"`
	InboundIds  []int  `json:"inboundIds" example:"[1,2]"`
	TestUrl     string `json:"testUrl" example:"https://speed.cloudflare.com/__down?bytes=262144"`
	DirectUrl   string `json:"directUrl" example:"https://example.com/"`
	IntervalSec int    `json:"intervalSec" example:"600"`
}

type ReachTargetView struct {
	Key              string  `json:"key" example:"1@vpn.example.com:443"`
	Label            string  `json:"label" example:"VLESS-443"`
	InboundId        int     `json:"inboundId" example:"1"`
	Status           string  `json:"status" example:"ok"`
	ConsecutiveFails int     `json:"consecutiveFails" example:"0"`
	ChangedAt        int64   `json:"changedAt" example:"1736000000000"`
	LastAt           int64   `json:"lastAt" example:"1736000000000"`
	LastOk           bool    `json:"lastOk" example:"true"`
	LastError        string  `json:"lastError" example:""`
	LastTtfbMs       int     `json:"lastTtfbMs" example:"180"`
	LastTotalMs      int     `json:"lastTotalMs" example:"950"`
	LastBytes        int64   `json:"lastBytes" example:"262144"`
	Checks7d         int     `json:"checks7d" example:"1008"`
	Uptime7d         float64 `json:"uptime7d" example:"99.4"`
}

type ReachProbeView struct {
	Id           int               `json:"id" example:"1"`
	Name         string            `json:"name" example:"home-mobile"`
	Enabled      bool              `json:"enabled" example:"true"`
	ClientEmail  string            `json:"clientEmail" example:"still-probe-k3m9x2qa"`
	InboundIds   []int             `json:"inboundIds" example:"[1,2]"`
	TestUrl      string            `json:"testUrl" example:"https://speed.cloudflare.com/__down?bytes=262144"`
	DirectUrl    string            `json:"directUrl" example:""`
	IntervalSec  int               `json:"intervalSec" example:"600"`
	LastSeenAt   int64             `json:"lastSeenAt" example:"1736000000000"`
	AgentVersion string            `json:"agentVersion" example:"1.4.0"`
	Stale        bool              `json:"stale" example:"false"`
	CreatedAt    int64             `json:"createdAt" example:"1736000000000"`
	Targets      []ReachTargetView `json:"targets"`
}

// ReachProbeCreated carries the plaintext token, which is never shown again.
type ReachProbeCreated struct {
	Probe ReachProbeView `json:"probe"`
	Token string         `json:"token" example:"8fJ2kQ..."`
}

type ReachHistoryPoint struct {
	Key     string `json:"key" example:"1@vpn.example.com:443"`
	At      int64  `json:"at" example:"1736000000000"`
	Ok      bool   `json:"ok" example:"true"`
	TtfbMs  int    `json:"ttfbMs" example:"180"`
	TotalMs int    `json:"totalMs" example:"950"`
}

// ReachProbeJob is what an agent fetches: one Xray outbound per endpoint it
// should test, holding only the probe's own client credentials.
type ReachProbeJob struct {
	Probe       string           `json:"probe"`
	IntervalSec int              `json:"intervalSec"`
	TestUrl     string           `json:"testUrl"`
	DirectUrl   string           `json:"directUrl,omitempty"`
	Targets     []ReachJobTarget `json:"targets"`
}

type ReachJobTarget struct {
	Key       string          `json:"key"`
	Label     string          `json:"label"`
	InboundId int             `json:"inboundId"`
	Protocol  string          `json:"protocol"`
	Outbound  json.RawMessage `json:"outbound"`
}

type ReachReport struct {
	AgentVersion string              `json:"agentVersion"`
	Results      []ReachReportResult `json:"results"`
}

type ReachReportResult struct {
	Key     string `json:"key"`
	Ok      bool   `json:"ok"`
	Error   string `json:"error"`
	TtfbMs  int    `json:"ttfbMs"`
	TotalMs int    `json:"totalMs"`
	Bytes   int64  `json:"bytes"`
}

var (
	ErrReachProbeUnauthorized = errors.New("probe token is not valid")
	ErrReachReportTooLarge    = fmt.Errorf("a report may hold at most %d results", reachProbeMaxTargets)
	errReachProbeNotFound     = errors.New("probe not found")
)

func (s *ReachProbeService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *ReachProbeService) emit(e eventbus.Event) {
	if s.publish != nil {
		s.publish(e)
		return
	}
	if eventBus != nil {
		eventBus.Publish(e)
	}
}

// validateReachInput normalizes the input in place and rejects what the agent
// could not act on; inbound support is checked against the stored inbounds.
func (s *ReachProbeService) validateReachInput(in *ReachProbeInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return common.NewError("probe name is required")
	}
	if utf8.RuneCountInString(in.Name) > reachProbeMaxNameLen {
		return common.NewErrorf("probe name must be %d characters or fewer", reachProbeMaxNameLen)
	}
	for _, r := range in.Name {
		if unicode.IsControl(r) || r == '<' || r == '>' || r == '&' {
			return common.NewError("probe name contains an invalid character")
		}
	}
	if in.IntervalSec == 0 {
		in.IntervalSec = ReachProbeDefaultInterval
	}
	if in.IntervalSec < reachProbeMinInterval || in.IntervalSec > reachProbeMaxInterval {
		return common.NewErrorf("interval must be between %d and %d seconds", reachProbeMinInterval, reachProbeMaxInterval)
	}
	if strings.TrimSpace(in.TestUrl) == "" {
		in.TestUrl = ReachProbeDefaultTestURL
	}
	var err error
	if in.TestUrl, err = sanitizeReachURL(in.TestUrl); err != nil {
		return common.NewError("test URL:", err)
	}
	if in.DirectUrl, err = sanitizeReachURL(in.DirectUrl); err != nil {
		return common.NewError("direct URL:", err)
	}
	in.InboundIds = uniqueInts(in.InboundIds)
	if len(in.InboundIds) == 0 {
		return common.NewError("choose at least one inbound to probe")
	}
	if len(in.InboundIds) > reachProbeMaxInbounds {
		return common.NewErrorf("a probe can test at most %d inbounds", reachProbeMaxInbounds)
	}
	for _, id := range in.InboundIds {
		ib, err := s.inboundService.GetInbound(id)
		if err != nil {
			return common.NewErrorf("inbound %d not found", id)
		}
		if !reachProbeSupports(ib) {
			return common.NewErrorf("inbound %q (%s) cannot be probed: supported are VLESS, VMess, Trojan, Shadowsocks and Hysteria", ib.Remark, ib.Protocol)
		}
	}
	return nil
}

func sanitizeReachURL(raw string) (string, error) {
	clean, err := SanitizeHTTPURL(raw)
	if err != nil {
		return "", err
	}
	if len(clean) > reachProbeMaxURLLen {
		return "", fmt.Errorf("URL must be %d characters or fewer", reachProbeMaxURLLen)
	}
	return clean, nil
}

func reachProbeSupports(ib *model.Inbound) bool {
	if !reachProbeProtocols[ib.Protocol] {
		return false
	}
	network, _ := unmarshalReachStream(ib.StreamSettings)["network"].(string)
	return network != "xdrive"
}

func unmarshalReachStream(raw string) map[string]any {
	stream := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &stream)
	return stream
}

// Create stores a new probe and the client it connects as. The returned bool
// asks the caller to schedule an Xray reconcile, as client creation does.
func (s *ReachProbeService) Create(in ReachProbeInput) (*ReachProbeCreated, bool, error) {
	if err := s.validateReachInput(&in); err != nil {
		return nil, false, err
	}
	db := database.GetDB()
	var taken int64
	if err := db.Model(&model.ReachProbe{}).Where("name = ?", in.Name).Count(&taken).Error; err != nil {
		return nil, false, err
	}
	if taken > 0 {
		return nil, false, common.NewError("a probe with that name already exists")
	}
	email := reachProbeClientPrefix + random.NumLower(10)
	payload := &ClientCreatePayload{
		Client: model.Client{
			Email:   email,
			Enable:  true,
			Flow:    "xtls-rprx-vision",
			Comment: "Reachability probe «" + in.Name + "», managed on the Probes page",
		},
		InboundIds: in.InboundIds,
	}
	needRestart, err := s.clientService.Create(&s.inboundService, payload)
	if err != nil {
		return nil, needRestart, err
	}
	token := random.Seq(reachProbeTokenLength)
	row := &model.ReachProbe{
		Name:        in.Name,
		TokenHash:   crypto.HashTokenSHA256(token),
		Enabled:     true,
		ClientEmail: email,
		InboundIds:  in.InboundIds,
		TestURL:     in.TestUrl,
		DirectURL:   in.DirectUrl,
		IntervalSec: in.IntervalSec,
	}
	if err := db.Create(row).Error; err != nil {
		if again, delErr := s.clientService.DeleteByEmail(&s.inboundService, email, false); delErr != nil {
			logger.Warning("reach probe: removing the client of a probe that failed to save:", delErr)
		} else {
			needRestart = needRestart || again
		}
		return nil, needRestart, err
	}
	view, err := s.view(row, s.clock())
	if err != nil {
		return nil, needRestart, err
	}
	return &ReachProbeCreated{Probe: *view, Token: token}, needRestart, nil
}

// Update changes a probe's settings and moves its client to the chosen inbounds.
func (s *ReachProbeService) Update(id int, in ReachProbeInput) (bool, error) {
	if err := s.validateReachInput(&in); err != nil {
		return false, err
	}
	row, err := s.get(id)
	if err != nil {
		return false, err
	}
	db := database.GetDB()
	var taken int64
	if err := db.Model(&model.ReachProbe{}).Where("name = ? AND id <> ?", in.Name, id).Count(&taken).Error; err != nil {
		return false, err
	}
	if taken > 0 {
		return false, common.NewError("a probe with that name already exists")
	}
	enabled := row.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	// A disabled probe keeps no client at all, so its old link stops working;
	// enabling it again issues a client with fresh credentials.
	var needRestart bool
	if enabled {
		row.Name = in.Name
		needRestart, err = s.syncClient(row, in.InboundIds)
	} else {
		needRestart, err = s.dropClient(row)
	}
	if err != nil {
		return needRestart, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ReachProbe{Id: id}).
			Select("name", "enabled", "inbound_ids", "test_url", "direct_url", "interval_sec", "stale_alerted").
			Updates(&model.ReachProbe{
				Name: in.Name, Enabled: enabled, InboundIds: in.InboundIds, TestURL: in.TestUrl,
				DirectURL: in.DirectUrl, IntervalSec: in.IntervalSec, StaleAlerted: false,
			}).Error; err != nil {
			return err
		}
		return pruneReachTargetsTx(tx, id, in.InboundIds)
	})
	return needRestart, err
}

// syncClient attaches the probe client to wanted inbounds and detaches it from
// the rest; a client the operator deleted by hand is created again.
func (s *ReachProbeService) syncClient(row *model.ReachProbe, wanted []int) (bool, error) {
	rec, err := s.clientService.GetRecordByEmail(nil, row.ClientEmail)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.clientService.Create(&s.inboundService, &ClientCreatePayload{
			Client: model.Client{
				Email:   row.ClientEmail,
				Enable:  true,
				Flow:    "xtls-rprx-vision",
				Comment: "Reachability probe «" + row.Name + "», managed on the Probes page",
			},
			InboundIds: wanted,
		})
	}
	if err != nil {
		return false, err
	}
	current, err := s.clientService.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		return false, err
	}
	var add, drop []int
	for _, id := range wanted {
		if !slices.Contains(current, id) {
			add = append(add, id)
		}
	}
	for _, id := range current {
		if !slices.Contains(wanted, id) {
			drop = append(drop, id)
		}
	}
	needRestart := false
	if len(add) > 0 {
		r, err := s.clientService.Attach(&s.inboundService, rec.Id, add)
		needRestart = needRestart || r
		if err != nil {
			return needRestart, err
		}
	}
	if len(drop) > 0 {
		r, err := s.clientService.Detach(&s.inboundService, rec.Id, drop)
		needRestart = needRestart || r
		if err != nil {
			return needRestart, err
		}
	}
	return needRestart, nil
}

// pruneReachTargetsTx forgets the alert state of inbounds the probe no longer tests.
func pruneReachTargetsTx(tx *gorm.DB, probeID int, inboundIds []int) error {
	var targets []model.ReachProbeTarget
	if err := tx.Where("probe_id = ?", probeID).Find(&targets).Error; err != nil {
		return err
	}
	for _, t := range targets {
		if t.TargetKey == ReachDirectKey {
			continue
		}
		if id, ok := reachTargetInbound(t.TargetKey); ok && slices.Contains(inboundIds, id) {
			continue
		}
		if err := tx.Delete(&model.ReachProbeTarget{}, t.Id).Error; err != nil {
			return err
		}
	}
	return nil
}

func reachTargetInbound(key string) (int, bool) {
	idText, _, ok := strings.Cut(key, "@")
	if !ok {
		return 0, false
	}
	id, err := strconv.Atoi(idText)
	return id, err == nil
}

// Delete revokes a probe: its token stops working and its client is removed.
func (s *ReachProbeService) Delete(id int) (bool, error) {
	row, err := s.get(id)
	if err != nil {
		return false, err
	}
	needRestart, err := s.dropClient(row)
	if err != nil {
		return needRestart, err
	}
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("probe_id = ?", id).Delete(&model.ReachProbeResult{}).Error; err != nil {
			return err
		}
		if err := tx.Where("probe_id = ?", id).Delete(&model.ReachProbeTarget{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ReachProbe{}, id).Error
	})
	return needRestart, err
}

// dropClient removes the probe's client from every inbound; a client that is
// already gone is not an error.
func (s *ReachProbeService) dropClient(row *model.ReachProbe) (bool, error) {
	rec, err := s.clientService.GetRecordByEmail(nil, row.ClientEmail)
	if err == nil {
		return s.clientService.Delete(&s.inboundService, rec.Id, false)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	// No record; delete stray copies left in inbound settings, if any.
	ids, err := s.clientService.findInboundIdsByClientEmail(row.ClientEmail)
	if err != nil || len(ids) == 0 {
		return false, err
	}
	return s.clientService.DeleteByEmail(&s.inboundService, row.ClientEmail, false)
}

// RotateToken revokes everything an old probe config could use: it issues a
// new token and replaces the probe client, so the previous UUID, password and
// subscription stop working too. The agent picks the new client up from its
// next job. The returned bool asks the caller to schedule an Xray reconcile.
func (s *ReachProbeService) RotateToken(id int) (string, bool, error) {
	row, err := s.get(id)
	if err != nil {
		return "", false, err
	}
	needRestart, err := s.dropClient(row)
	if err != nil {
		return "", needRestart, err
	}
	if row.Enabled {
		again, err := s.syncClient(row, row.InboundIds)
		needRestart = needRestart || again
		if err != nil {
			return "", needRestart, err
		}
	}
	token := random.Seq(reachProbeTokenLength)
	err = database.GetDB().Model(&model.ReachProbe{}).Where("id = ?", id).
		Update("token_hash", crypto.HashTokenSHA256(token)).Error
	if err != nil {
		return "", needRestart, err
	}
	return token, needRestart, nil
}

func (s *ReachProbeService) get(id int) (*model.ReachProbe, error) {
	row := &model.ReachProbe{}
	if err := database.GetDB().First(row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errReachProbeNotFound
		}
		return nil, err
	}
	return row, nil
}

// Authenticate resolves an agent's bearer token to its enabled probe.
func (s *ReachProbeService) Authenticate(token string) (*model.ReachProbe, error) {
	token = strings.TrimSpace(token)
	if len(token) != reachProbeTokenLength {
		return nil, ErrReachProbeUnauthorized
	}
	row := &model.ReachProbe{}
	err := database.GetDB().Where("token_hash = ?", crypto.HashTokenSHA256(token)).First(row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !row.Enabled) {
		return nil, ErrReachProbeUnauthorized
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (s *ReachProbeService) List() ([]ReachProbeView, error) {
	var rows []*model.ReachProbe
	if err := database.GetDB().Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	now := s.clock()
	out := make([]ReachProbeView, 0, len(rows))
	for _, row := range rows {
		v, err := s.view(row, now)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

func reachProbeStale(row *model.ReachProbe, now time.Time) bool {
	if !row.Enabled || row.LastSeenAt == 0 {
		return false
	}
	limit := time.Duration(ReachProbeStaleFactor*row.IntervalSec) * time.Second
	return now.Sub(time.UnixMilli(row.LastSeenAt)) > limit
}

func (s *ReachProbeService) view(row *model.ReachProbe, now time.Time) (*ReachProbeView, error) {
	v := &ReachProbeView{
		Id:           row.Id,
		Name:         row.Name,
		Enabled:      row.Enabled,
		ClientEmail:  row.ClientEmail,
		InboundIds:   row.InboundIds,
		TestUrl:      row.TestURL,
		DirectUrl:    row.DirectURL,
		IntervalSec:  row.IntervalSec,
		LastSeenAt:   row.LastSeenAt,
		AgentVersion: row.AgentVersion,
		Stale:        reachProbeStale(row, now),
		CreatedAt:    row.CreatedAt,
		Targets:      []ReachTargetView{},
	}
	if v.InboundIds == nil {
		v.InboundIds = []int{}
	}
	db := database.GetDB()
	var states []model.ReachProbeTarget
	if err := db.Where("probe_id = ?", row.Id).Order("target_key asc").Find(&states).Error; err != nil {
		return nil, err
	}
	since := now.Add(-reachProbeHistory).UnixMilli()
	type agg struct {
		TargetKey string
		Checks    int
		Oks       int
	}
	var aggs []agg
	if err := db.Model(&model.ReachProbeResult{}).
		Select("target_key, COUNT(*) AS checks, SUM(CASE WHEN ok THEN 1 ELSE 0 END) AS oks").
		Where("probe_id = ? AND at >= ?", row.Id, since).
		Group("target_key").Scan(&aggs).Error; err != nil {
		return nil, err
	}
	byKey := make(map[string]agg, len(aggs))
	for _, a := range aggs {
		byKey[a.TargetKey] = a
	}
	for _, st := range states {
		tv := ReachTargetView{
			Key:              st.TargetKey,
			Status:           st.Status,
			ConsecutiveFails: st.ConsecutiveFails,
			ChangedAt:        st.ChangedAt,
		}
		var last model.ReachProbeResult
		err := db.Where("probe_id = ? AND target_key = ?", row.Id, st.TargetKey).Order("at desc").Limit(1).Find(&last).Error
		if err != nil {
			return nil, err
		}
		if last.Id != 0 {
			tv.Label, tv.InboundId = last.Label, last.InboundId
			tv.LastAt, tv.LastOk, tv.LastError = last.At, last.Ok, last.ErrorText
			tv.LastTtfbMs, tv.LastTotalMs, tv.LastBytes = last.TtfbMs, last.TotalMs, last.Bytes
		}
		if a, ok := byKey[st.TargetKey]; ok && a.Checks > 0 {
			tv.Checks7d = a.Checks
			tv.Uptime7d = float64(a.Oks*1000/a.Checks) / 10
		}
		v.Targets = append(v.Targets, tv)
	}
	return v, nil
}

// History returns the probe's results of the last seven days, oldest first.
func (s *ReachProbeService) History(id int) ([]ReachHistoryPoint, error) {
	if _, err := s.get(id); err != nil {
		return nil, err
	}
	since := s.clock().Add(-reachProbeHistory).UnixMilli()
	var rows []model.ReachProbeResult
	if err := database.GetDB().Where("probe_id = ? AND at >= ?", id, since).Order("at asc, id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ReachHistoryPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReachHistoryPoint{Key: r.TargetKey, At: r.At, Ok: r.Ok, TtfbMs: r.TtfbMs, TotalMs: r.TotalMs})
	}
	return out, nil
}

// BuildJob renders the probe client's share links for every chosen inbound,
// the same links a subscription would hand out, and converts each to an
// Xray outbound. host is the panel host the agent called, as for "copy link".
func (s *ReachProbeService) BuildJob(row *model.ReachProbe, host string) (*ReachProbeJob, error) {
	job := &ReachProbeJob{
		Probe:       row.Name,
		IntervalSec: row.IntervalSec,
		TestUrl:     row.TestURL,
		DirectUrl:   row.DirectURL,
		Targets:     []ReachJobTarget{},
	}
	provider := subLinkProvider()
	if provider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	for _, inboundID := range row.InboundIds {
		ib, err := s.inboundService.GetInbound(inboundID)
		if err != nil || !ib.Enable || !reachProbeSupports(ib) {
			continue
		}
		links := provider.LinksForClient(host, ib, row.ClientEmail)
		if len(links) > reachProbeMaxLinks {
			links = links[:reachProbeMaxLinks]
		}
		for _, l := range links {
			target, ok := reachJobTarget(ib, l, len(links) > 1)
			if !ok || slices.ContainsFunc(job.Targets, func(t ReachJobTarget) bool { return t.Key == target.Key }) {
				continue
			}
			job.Targets = append(job.Targets, target)
		}
	}
	return job, nil
}

// reachJobTarget labels a target by its inbound's remark: the link's own
// remark carries the probe client's email through the remark template.
func reachJobTarget(ib *model.Inbound, shareLink string, manyEndpoints bool) (ReachJobTarget, bool) {
	parsed, err := link.ParseLink(shareLink)
	if err != nil {
		return ReachJobTarget{}, false
	}
	ob := parsed.Outbound
	settings, _ := ob["settings"].(map[string]any)
	address, _ := settings["address"].(string)
	port := 0
	switch p := settings["port"].(type) {
	case int:
		port = p
	case float64:
		port = int(p)
	}
	if address == "" || port <= 0 {
		return ReachJobTarget{}, false
	}
	endpoint := net.JoinHostPort(address, strconv.Itoa(port))
	label := strings.TrimSpace(ib.Remark)
	if label == "" {
		label = ib.Tag
	}
	if manyEndpoints {
		label += " (" + endpoint + ")"
	}
	ob["tag"] = "probe"
	raw, err := json.Marshal(ob)
	if err != nil {
		return ReachJobTarget{}, false
	}
	protocol, _ := ob["protocol"].(string)
	return ReachJobTarget{
		Key:       strconv.Itoa(ib.Id) + "@" + endpoint,
		Label:     truncateRunes(oneLineText(label), 64),
		InboundId: ib.Id,
		Protocol:  protocol,
		Outbound:  raw,
	}, true
}

// oneLineText keeps text that came from outside the panel on one plain line.
func oneLineText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s))
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func cleanAgentVersion(v string) string {
	v = truncateRunes(strings.TrimSpace(v), reachProbeMaxVersionLen)
	for _, r := range v {
		if (r != '.' && r != '-' && r != '_' && r != '+' && !unicode.IsLetter(r) && !unicode.IsDigit(r)) || r > unicode.MaxASCII {
			return ""
		}
	}
	return v
}

// reachTransition is what one check changed about a target's alert state.
type reachTransition int

const (
	reachNoChange reachTransition = iota
	reachWentDown
	reachCameBack
)

// stepReachTarget advances a target's alert state by one check: down only
// after ReachProbeFailThreshold failures in a row, back up on the first success.
func stepReachTarget(st *model.ReachProbeTarget, ok bool, nowMs int64) reachTransition {
	if ok {
		st.ConsecutiveFails = 0
		switch st.Status {
		case ReachStatusFail:
			st.Status, st.ChangedAt = ReachStatusOk, nowMs
			return reachCameBack
		case ReachStatusOk:
			return reachNoChange
		default:
			st.Status, st.ChangedAt = ReachStatusOk, nowMs
			return reachNoChange
		}
	}
	st.ConsecutiveFails++
	if st.ConsecutiveFails >= ReachProbeFailThreshold && st.Status != ReachStatusFail {
		st.Status, st.ChangedAt = ReachStatusFail, nowMs
		return reachWentDown
	}
	return reachNoChange
}

// Ingest stores an agent's report. Results for targets that are not in the
// probe's current job are dropped, so a token can only describe its own checks.
func (s *ReachProbeService) Ingest(row *model.ReachProbe, host string, report *ReachReport) (int, error) {
	if report == nil {
		return 0, common.NewError("empty report")
	}
	if len(report.Results) > reachProbeMaxTargets {
		return 0, ErrReachReportTooLarge
	}
	job, err := s.BuildJob(row, host)
	if err != nil {
		return 0, err
	}
	known := make(map[string]ReachJobTarget, len(job.Targets)+1)
	for _, t := range job.Targets {
		known[t.Key] = t
	}
	if row.DirectURL != "" {
		known[ReachDirectKey] = ReachJobTarget{Key: ReachDirectKey, Label: ReachDirectKey}
	}
	now := s.clock()
	nowMs := now.UnixMilli()
	var events []eventbus.Event
	stored := 0
	seen := map[string]bool{}
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		for _, r := range report.Results {
			target, ok := known[r.Key]
			if !ok || seen[r.Key] {
				continue
			}
			seen[r.Key] = true
			errText := ""
			if !r.Ok {
				errText = truncateRunes(oneLineText(r.Error), reachProbeMaxErrorLen)
			}
			res := model.ReachProbeResult{
				ProbeId:   row.Id,
				TargetKey: r.Key,
				InboundId: target.InboundId,
				Label:     target.Label,
				Ok:        r.Ok,
				ErrorText: errText,
				TtfbMs:    clampInt(r.TtfbMs, 0, 600000),
				TotalMs:   clampInt(r.TotalMs, 0, 600000),
				Bytes:     max(min(r.Bytes, 1<<40), 0),
				At:        nowMs,
			}
			if err := tx.Create(&res).Error; err != nil {
				return err
			}
			stored++
			st := model.ReachProbeTarget{ProbeId: row.Id, TargetKey: r.Key, Status: ReachStatusUnknown}
			if err := tx.Where("probe_id = ? AND target_key = ?", row.Id, r.Key).FirstOrCreate(&st).Error; err != nil {
				return err
			}
			switch stepReachTarget(&st, r.Ok, nowMs) {
			case reachWentDown:
				events = append(events, eventbus.Event{
					Type: eventbus.EventProbeDown, Source: row.Name + " · " + target.Label, Timestamp: now,
					Data: &eventbus.ProbeHealthData{Probe: row.Name, Target: target.Label, Error: errText},
				})
			case reachCameBack:
				events = append(events, eventbus.Event{
					Type: eventbus.EventProbeUp, Source: row.Name + " · " + target.Label, Timestamp: now,
					Data: &eventbus.ProbeHealthData{Probe: row.Name, Target: target.Label, LatencyMs: res.TtfbMs},
				})
			}
			if err := tx.Model(&model.ReachProbeTarget{}).Where("id = ?", st.Id).Updates(map[string]any{
				"status": st.Status, "consecutive_fails": st.ConsecutiveFails, "changed_at": st.ChangedAt,
			}).Error; err != nil {
				return err
			}
		}
		if row.StaleAlerted {
			events = append(events, eventbus.Event{
				Type: eventbus.EventProbeUp, Source: row.Name, Timestamp: now,
				Data: &eventbus.ProbeHealthData{Probe: row.Name},
			})
		}
		if err := tx.Model(&model.ReachProbe{}).Where("id = ?", row.Id).Updates(map[string]any{
			"last_seen_at":  nowMs,
			"agent_version": cleanAgentVersion(report.AgentVersion),
			"stale_alerted": false,
		}).Error; err != nil {
			return err
		}
		return tx.Where("probe_id = ? AND at < ?", row.Id, now.Add(-reachProbeHistory).UnixMilli()).
			Delete(&model.ReachProbeResult{}).Error
	})
	if err != nil {
		return 0, err
	}
	for _, e := range events {
		s.emit(e)
	}
	return stored, nil
}

func clampInt(v, lo, hi int) int {
	return max(lo, min(v, hi))
}

// CheckStale alerts once for every probe that has stopped reporting; the
// probe's next report clears the mark and announces it is back.
func (s *ReachProbeService) CheckStale() error {
	var rows []*model.ReachProbe
	if err := database.GetDB().Where("enabled = ? AND stale_alerted = ? AND last_seen_at > 0", true, false).Find(&rows).Error; err != nil {
		return err
	}
	now := s.clock()
	for _, row := range rows {
		if !reachProbeStale(row, now) {
			continue
		}
		res := database.GetDB().Model(&model.ReachProbe{}).
			Where("id = ? AND stale_alerted = ?", row.Id, false).Update("stale_alerted", true)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		s.emit(eventbus.Event{
			Type: eventbus.EventProbeStale, Source: row.Name, Timestamp: now,
			Data: &eventbus.ProbeHealthData{
				Probe:     row.Name,
				SilentFor: int(now.Sub(time.UnixMilli(row.LastSeenAt)) / time.Minute),
			},
		})
	}
	return nil
}
