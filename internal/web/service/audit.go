package service

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	AuditSuccess = "success"
	AuditFailure = "failure"

	AuditActionLogin = "LOGIN"
	AuditActionPrune = "PRUNE"
	AuditActorSystem = "system"

	// AuditExportMaxRows bounds one CSV export; a longer answer needs a narrower filter.
	AuditExportMaxRows = 100000
	auditMaxPageSize   = 500
	auditVerifyBatch   = 1000
	// auditAnchorKey holds the hash of the last pruned entry: the first kept entry's PrevHash.
	auditAnchorKey = "auditLogAnchor"
)

// auditMu serializes appends and prunes: each entry chains to the one written just before it.
var auditMu sync.Mutex

// AuditService keeps the tamper-evident journal of admin actions (model.AuditLog).
type AuditService struct {
	settingService SettingService
}

// AuditFilter selects journal entries; empty strings and zero times leave that side open.
type AuditFilter struct {
	From     int64  `json:"from" example:"1735689600000"`
	To       int64  `json:"to" example:"1735775999999"`
	Actor    string `json:"actor" example:"admin"`
	Action   string `json:"action" example:"inbounds"`
	Outcome  string `json:"outcome" validate:"omitempty,oneof=success failure" example:"failure"`
	Page     int    `json:"page" example:"1"`
	PageSize int    `json:"pageSize" example:"50"`
}

// AuditPage is one page of journal entries, newest first.
type AuditPage struct {
	Rows  []model.AuditLog `json:"rows"`
	Total int64            `json:"total" example:"1234"`
}

// AuditVerifyResult reports the chain check; BrokenId is the first entry that does not link.
type AuditVerifyResult struct {
	Ok       bool   `json:"ok" example:"false"`
	Checked  int64  `json:"checked" example:"1234"`
	BrokenId int64  `json:"brokenId" example:"42"`
	Reason   string `json:"reason" example:"the previous entry is missing or was changed"`
}

// auditHash chains an entry to prevHash; the id is left out because it is assigned on insert.
func auditHash(prevHash string, e *model.AuditLog) string {
	content, _ := json.Marshal([]any{e.Time, e.Actor, e.IP, e.Action, e.Target, e.Outcome, e.Detail})
	sum := sha256.Sum256(append([]byte(prevHash), content...))
	return hex.EncodeToString(sum[:])
}

func auditAnchor(tx *gorm.DB) (string, error) {
	var row model.Setting
	err := tx.Where("key = ?", auditAnchorKey).First(&row).Error
	if database.IsNotFound(err) {
		return "", nil
	}
	return row.Value, err
}

func auditAppend(tx *gorm.DB, e *model.AuditLog) error {
	var last model.AuditLog
	err := tx.Order("id DESC").Limit(1).Find(&last).Error
	if err != nil {
		return err
	}
	prev := last.Hash
	if last.Id == 0 {
		if prev, err = auditAnchor(tx); err != nil {
			return err
		}
	}
	e.Id = 0
	e.PrevHash = prev
	e.Hash = auditHash(prev, e)
	return tx.Create(e).Error
}

// Record appends one entry to the journal; a zero Time is now.
func (s *AuditService) Record(e model.AuditLog) error {
	if e.Time == 0 {
		e.Time = time.Now().UnixMilli()
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	return database.GetDB().Transaction(func(tx *gorm.DB) error { return auditAppend(tx, &e) })
}

// Verify recomputes the chain from the prune anchor and names the first entry that does not link.
func (s *AuditService) Verify() (*AuditVerifyResult, error) {
	db := database.GetDB()
	prev, err := auditAnchor(db)
	if err != nil {
		return nil, err
	}
	res := &AuditVerifyResult{Ok: true}
	var batch []model.AuditLog
	err = db.Order("id").FindInBatches(&batch, auditVerifyBatch, func(_ *gorm.DB, _ int) error {
		for i := range batch {
			e := &batch[i]
			switch {
			case e.PrevHash != prev:
				res.Reason = auditReasonMissing
			case auditHash(prev, e) != e.Hash:
				res.Reason = auditReasonChanged
			default:
				prev = e.Hash
				res.Checked++
				continue
			}
			res.Ok, res.BrokenId = false, e.Id
			return errAuditBroken
		}
		return nil
	}).Error
	if err != nil && !errors.Is(err, errAuditBroken) {
		return nil, err
	}
	return res, nil
}

var errAuditBroken = errors.New("audit chain broken")

const (
	auditReasonMissing = "the previous entry is missing or was changed"
	auditReasonChanged = "the entry was changed"
)

// Prune deletes the entries older than the retention setting, moves the anchor to the last
// deleted entry's hash so Verify still passes, and records the prune.
func (s *AuditService) Prune(now time.Time) (int64, error) {
	days, err := s.settingService.GetAuditLogRetentionDays()
	if err != nil {
		return 0, err
	}
	cutoff := now.AddDate(0, 0, -days).UnixMilli()
	auditMu.Lock()
	defer auditMu.Unlock()
	var pruned int64
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		var last model.AuditLog
		if err := tx.Where("time < ?", cutoff).Order("id DESC").Limit(1).Find(&last).Error; err != nil {
			return err
		}
		if last.Id == 0 {
			return nil
		}
		res := tx.Where("id <= ?", last.Id).Delete(&model.AuditLog{})
		if res.Error != nil {
			return res.Error
		}
		pruned = res.RowsAffected
		anchor := model.Setting{Key: auditAnchorKey}
		if err := tx.Where("key = ?", auditAnchorKey).Assign(model.Setting{Value: last.Hash}).FirstOrCreate(&anchor).Error; err != nil {
			return err
		}
		return auditAppend(tx, &model.AuditLog{
			Time: now.UnixMilli(), Actor: AuditActorSystem, Action: AuditActionPrune,
			Target: "count=" + strconv.FormatInt(pruned, 10), Outcome: AuditSuccess,
			Detail: "retention " + strconv.Itoa(days) + " days",
		})
	})
	return pruned, err
}

// PruneJob is the cron step around Prune.
func (s *AuditService) PruneJob() {
	if _, err := s.Prune(time.Now()); err != nil {
		logger.Warning("audit log: prune failed:", err)
	}
}

func auditQuery(f AuditFilter) *gorm.DB {
	q := database.GetDB().Model(&model.AuditLog{})
	if f.From > 0 {
		q = q.Where("time >= ?", f.From)
	}
	if f.To > 0 {
		q = q.Where("time <= ?", f.To)
	}
	if f.Actor != "" {
		q = q.Where("actor = ?", f.Actor)
	}
	if f.Action != "" {
		q = q.Where("action LIKE ? ESCAPE '\\'", "%"+escapeLikeLiteral(f.Action)+"%")
	}
	if f.Outcome != "" {
		q = q.Where("outcome = ?", f.Outcome)
	}
	return q
}

// Query returns one page of the journal, newest first.
func (s *AuditService) Query(f AuditFilter) (*AuditPage, error) {
	size := min(max(f.PageSize, 1), auditMaxPageSize)
	page := max(f.Page, 1)
	out := &AuditPage{Rows: []model.AuditLog{}}
	if err := auditQuery(f).Count(&out.Total).Error; err != nil {
		return nil, err
	}
	err := auditQuery(f).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&out.Rows).Error
	return out, err
}

// Export writes the entries matching the filter as CSV, newest first, at most
// AuditExportMaxRows of them. Times are UTC.
func (s *AuditService) Export(w io.Writer, f AuditFilter) error {
	var rows []model.AuditLog
	if err := auditQuery(f).Order("id DESC").Limit(AuditExportMaxRows).Find(&rows).Error; err != nil {
		return err
	}
	out := csv.NewWriter(w)
	_ = out.Write([]string{"id", "time_utc", "actor", "ip", "action", "target", "outcome", "detail", "hash"})
	for _, e := range rows {
		_ = out.Write([]string{
			strconv.FormatInt(e.Id, 10), time.UnixMilli(e.Time).UTC().Format("2006-01-02T15:04:05.000Z"),
			csvCell(e.Actor), csvCell(e.IP), csvCell(e.Action), csvCell(e.Target), e.Outcome, csvCell(e.Detail), e.Hash,
		})
	}
	out.Flush()
	return out.Error()
}

// HandleEvent records admin login attempts published on the event bus.
func (s *AuditService) HandleEvent(e eventbus.Event) {
	data, ok := e.Data.(*eventbus.LoginEventData)
	if e.Type != eventbus.EventLoginAttempt || !ok {
		return
	}
	entry := model.AuditLog{Actor: data.Username, IP: data.IP, Action: AuditActionLogin, Outcome: AuditFailure, Detail: data.Reason}
	if data.Status == "success" {
		entry.Outcome = AuditSuccess
	}
	if !e.Timestamp.IsZero() {
		entry.Time = e.Timestamp.UnixMilli()
	}
	if err := s.Record(entry); err != nil {
		logger.Warning("audit log: record login failed:", err)
	}
}
