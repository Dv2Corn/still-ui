package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type HwidRequest struct {
	Hwid        string
	UserAgent   string
	DeviceOS    string
	OsVersion   string
	DeviceModel string
}

type HwidGateResult struct {
	Allowed           bool
	Active            bool
	NotSupported      bool
	MaxDevicesReached bool
	LimitReached      bool
	Limit             int
	Registered        int
}

// HwidSlotStatus is the aggregate device-slot view exposed to subscribers:
// counters only, no hwid value or hash, no email, no device metadata.
type HwidSlotStatus struct {
	Active     bool `json:"active" example:"true"`
	Limit      int  `json:"limit" example:"2"`
	Registered int  `json:"registered" example:"1"`
	Remaining  int  `json:"remaining" example:"1"`
	Full       bool `json:"full" example:"false"`
}

const (
	minHwidLength         = 6
	hwidFingerprintLength = 12
)

var errClientHwidWriteNotSerialized = errors.New("client HWID write requires the serialized transaction")

type ClientHwidInfo struct {
	Id          int    `json:"id"`
	FirstSeen   int64  `json:"firstSeen"`
	LastSeen    int64  `json:"lastSeen"`
	UserAgent   string `json:"userAgent"`
	DeviceOS    string `json:"deviceOs"`
	OsVersion   string `json:"osVersion"`
	DeviceModel string `json:"deviceModel"`
	Fingerprint string `json:"fingerprint"`
	// NoID marks the one slot all apps without a device id share; it has no fingerprint.
	NoID bool `json:"noId"`
}

func hashHwid(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

const (
	// HwidNoIdShared: every app that sends no device id shares one slot of the subscription.
	HwidNoIdShared = "shared"
	HwidNoIdAllow  = database.HwidNoIdAllow
	HwidNoIdRefuse = database.HwidNoIdRefuse

	// noIDHwidHash marks the shared slot's row. A device hash is 64 hex characters, so it cannot collide.
	noIDHwidHash = "no-device-id"

	SubHwidDefaultLimitMax = 1000
	SubHwidExpireDaysMax   = 3650
	SubHwidNoticeMaxLength = 256
)

// hwidPolicy is the panel-wide part of the device limit, read once per request.
type hwidPolicy struct {
	// defaultLimit applies to a subscription whose clients set no limit of their own; 0 is none.
	defaultLimit int
	noID         string
	// staleBefore is the last_seen (ms) below which a device has expired; 0 keeps every device.
	staleBefore int64
}

var hwidPolicyKeys = []string{"subHwidDefaultLimit", database.HwidNoIdPolicySetting, database.HwidExpireDaysSetting}

// loadHwidPolicy reads through the caller's handle, so the limit write can call it inside its transaction.
func loadHwidPolicy(db *gorm.DB) (hwidPolicy, error) {
	values := map[string]string{}
	for _, key := range hwidPolicyKeys {
		values[key] = defaultValueMap[key]
	}
	var rows []model.Setting
	if err := db.Where("key IN ?", hwidPolicyKeys).Find(&rows).Error; err != nil {
		return hwidPolicy{}, err
	}
	for _, row := range rows {
		if row.Value != "" {
			values[row.Key] = row.Value
		}
	}
	policy := hwidPolicy{noID: values[database.HwidNoIdPolicySetting]}
	// A value nobody can have saved must not open the limit: it reads as the default.
	if policy.noID != HwidNoIdAllow && policy.noID != HwidNoIdRefuse {
		policy.noID = HwidNoIdShared
	}
	if limit, err := strconv.Atoi(values["subHwidDefaultLimit"]); err == nil && limit > 0 {
		policy.defaultLimit = limit
	}
	if days, err := strconv.Atoi(values[database.HwidExpireDaysSetting]); err == nil && days > 0 {
		policy.staleBefore = time.Now().AddDate(0, 0, -days).UnixMilli()
	}
	return policy, nil
}

// liveHwids narrows a client_hwids query to the rows that hold a slot: not expired, and the
// shared no-id slot only while the policy counts it.
func (p hwidPolicy) liveHwids(tx *gorm.DB) *gorm.DB {
	if p.noID == HwidNoIdShared {
		return tx.Where("last_seen >= ?", p.staleBefore)
	}
	return tx.Where("last_seen >= ? AND hwid_hash <> ?", p.staleBefore, noIDHwidHash)
}

// staleHwids is the complement of liveHwids: the rows to delete.
func (p hwidPolicy) staleHwids(tx *gorm.DB) *gorm.DB {
	if p.noID == HwidNoIdShared {
		return tx.Where("last_seen < ?", p.staleBefore)
	}
	return tx.Where("(last_seen < ? OR hwid_hash = ?)", p.staleBefore, noIDHwidHash)
}

func (p hwidPolicy) deleteStaleHwids(tx *gorm.DB, subID string) error {
	return p.staleHwids(tx.Where("sub_id = ?", subID)).Delete(&model.ClientHwid{}).Error
}

func shortHwidFingerprint(hash string) string {
	if len(hash) <= hwidFingerprintLength {
		return hash
	}
	return hash[:hwidFingerprintLength]
}

func trimHwidMeta(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 512 {
		return string(r[:512])
	}
	return s
}

func normalizeHwidRequest(req HwidRequest) HwidRequest {
	return HwidRequest{
		Hwid:        strings.TrimSpace(req.Hwid),
		UserAgent:   trimHwidMeta(req.UserAgent),
		DeviceOS:    trimHwidMeta(req.DeviceOS),
		OsVersion:   trimHwidMeta(req.OsVersion),
		DeviceModel: trimHwidMeta(req.DeviceModel),
	}
}

// The subscription body is built for disabled clients too, so an expired,
// depleted or suspended client must keep holding the limit of its subId.
func effectiveHwidLimitForSubID(tx *gorm.DB, subID string, defaultLimit int) (int, error) {
	var row struct {
		Clients  int64
		OwnLimit int
	}
	err := tx.Model(&model.ClientRecord{}).
		Where("sub_id = ?", subID).
		Select("COUNT(*) AS clients, COALESCE(MAX(limit_hwid), 0) AS own_limit").
		Scan(&row).Error
	// The panel default belongs to subscriptions that exist: a subId no client owns has no limit.
	if err != nil || row.OwnLimit > 0 || row.Clients == 0 {
		return row.OwnLimit, err
	}
	return defaultLimit, nil
}

// lockHwidLimitForSubID is effectiveHwidLimitForSubID that also row-locks the subId's
// clients (PostgreSQL; SQLite runs one writer at a time anyway), so concurrent new
// devices of one subscription are counted and inserted one after another.
func lockHwidLimitForSubID(tx *gorm.DB, subID string, defaultLimit int) (int, error) {
	var limits []*int
	if err := tx.Model(&model.ClientRecord{}).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("sub_id = ?", subID).
		Order("id ASC").
		Pluck("limit_hwid", &limits).Error; err != nil {
		return 0, err
	}
	limit := 0
	for _, l := range limits {
		if l != nil {
			limit = max(limit, *l)
		}
	}
	if limit <= 0 && len(limits) > 0 {
		limit = defaultLimit
	}
	return limit, nil
}

// subIDHasEnabledClient separates "no device limit" from "no such subscription":
// effectiveHwidLimitForSubID yields 0 for both.
func subIDHasEnabledClient(tx *gorm.DB, subID string) (bool, error) {
	var enabled int64
	err := tx.Model(&model.ClientRecord{}).
		Where("sub_id = ? AND enable = ?", subID, true).
		Count(&enabled).Error
	return enabled > 0, err
}

// EnforceHwidForSubID is the device gate of a subscription fetch. A registered device always
// passes; a new one takes a slot while fewer than the limit are registered, else it is refused.
func (s *ClientService) EnforceHwidForSubID(subID string, req HwidRequest) (HwidGateResult, error) {
	var res HwidGateResult
	subID = strings.TrimSpace(subID)
	if subID == "" {
		res.Allowed = true
		return res, nil
	}

	db := database.GetDB()
	policy, err := loadHwidPolicy(db)
	if err != nil {
		return res, err
	}
	limit, err := effectiveHwidLimitForSubID(db, subID, policy.defaultLimit)
	if err != nil {
		return res, err
	}
	req = normalizeHwidRequest(req)
	hasID := len(req.Hwid) >= minHwidLength
	if limit <= 0 {
		if !hasID {
			return HwidGateResult{Allowed: true}, nil
		}
		// The gate runs before the sub lookup; a subId no client owns gets no row.
		known, err := subIDHasEnabledClient(db, subID)
		if err != nil {
			return res, err
		}
		// A limit set since the read above sends the request through the gate below.
		if !known || trackUnlimitedHwid(db, subID, req, policy) {
			return HwidGateResult{Allowed: true}, nil
		}
	}

	res.Active = true
	res.Limit = limit
	// Without an id two devices cannot be told apart: they are refused, share one slot, or pass uncounted.
	if !hasID && policy.noID != HwidNoIdShared {
		res.NotSupported = policy.noID == HwidNoIdRefuse
		res.Allowed = !res.NotSupported
		return res, nil
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		limit, err := lockHwidLimitForSubID(tx, subID, policy.defaultLimit)
		if err != nil {
			return err
		}
		if limit <= 0 {
			res = HwidGateResult{Allowed: true}
			return nil
		}
		res.Active = true
		res.Limit = limit
		hwidHash := noIDHwidHash
		if hasID {
			hwidHash = hashHwid(req.Hwid)
		}
		// Expired devices leave before the count, so the slot they held is free for this request.
		if err := policy.deleteStaleHwids(tx, subID); err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		var existing model.ClientHwid
		err = tx.Where("sub_id = ? AND hwid_hash = ?", subID, hwidHash).First(&existing).Error
		if err == nil {
			if err := tx.Model(&model.ClientHwid{}).Where("id = ?", existing.Id).Updates(map[string]any{
				"last_seen": now, "user_agent": req.UserAgent, "device_os": req.DeviceOS, "os_version": req.OsVersion, "device_model": req.DeviceModel,
			}).Error; err != nil {
				return err
			}
			var count int64
			if err := tx.Model(&model.ClientHwid{}).Where("sub_id = ?", subID).Count(&count).Error; err != nil {
				return err
			}
			res.Allowed = true
			res.Registered = int(count)
			res.LimitReached = count >= int64(limit)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		if err := tx.Model(&model.ClientHwid{}).Where("sub_id = ?", subID).Count(&count).Error; err != nil {
			return err
		}
		res.Registered = int(count)
		if count >= int64(limit) {
			res.MaxDevicesReached = true
			res.LimitReached = true
			return nil
		}
		if err := tx.Create(&model.ClientHwid{SubID: subID, HwidHash: hwidHash, FirstSeen: now, LastSeen: now, UserAgent: req.UserAgent, DeviceOS: req.DeviceOS, OsVersion: req.OsVersion, DeviceModel: req.DeviceModel}).Error; err != nil {
			return err
		}
		res.Allowed = true
		res.Registered = int(count) + 1
		res.LimitReached = res.Registered >= limit
		return nil
	})
	return res, err
}

// trackUnlimitedHwid lists devices of a sub with no HWID limit in the panel. It is
// best-effort: a failed write must not deny a subscription nothing restricts.
// False means the sub has a limit by now and no row was written.
func trackUnlimitedHwid(db *gorm.DB, subID string, req HwidRequest, policy hwidPolicy) bool {
	limited := false
	now := time.Now().UnixMilli()
	err := db.Transaction(func(tx *gorm.DB) error {
		// A row landing behind setClientLimitHwidByEmailTx's trim would stay registered
		// above the limit, so the caller's read does not count here.
		limit, err := effectiveHwidLimitForSubID(tx, subID, policy.defaultLimit)
		if err != nil {
			return err
		}
		if limited = limit > 0; limited {
			return nil
		}
		if err := policy.deleteStaleHwids(tx, subID); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "sub_id"}, {Name: "hwid_hash"}},
			DoUpdates: clause.AssignmentColumns([]string{"last_seen", "user_agent", "device_os", "os_version", "device_model"}),
		}).Create(&model.ClientHwid{SubID: subID, HwidHash: hashHwid(req.Hwid), FirstSeen: now, LastSeen: now, UserAgent: req.UserAgent, DeviceOS: req.DeviceOS, OsVersion: req.OsVersion, DeviceModel: req.DeviceModel}).Error
	})
	if err != nil {
		logger.Warning("track HWID for unlimited subscription failed:", err)
	}
	return !limited
}

// HwidDeviceHoldsSlotForSubID is SELECT-only: EnforceHwidForSubID's verdict for a device that
// takes no new slot, so a page view never registers the browser as a device.
func (s *ClientService) HwidDeviceHoldsSlotForSubID(subID string, hwid string) (bool, error) {
	subID = strings.TrimSpace(subID)
	if subID == "" {
		return true, nil
	}
	db := database.GetDB()
	policy, err := loadHwidPolicy(db)
	if err != nil {
		return false, err
	}
	limit, err := effectiveHwidLimitForSubID(db, subID, policy.defaultLimit)
	if err != nil || limit <= 0 {
		return err == nil, err
	}
	hwidHash := noIDHwidHash
	if hwid = strings.TrimSpace(hwid); len(hwid) >= minHwidLength {
		hwidHash = hashHwid(hwid)
	} else if policy.noID != HwidNoIdShared {
		return policy.noID == HwidNoIdAllow, nil
	}
	var held int64
	err = policy.liveHwids(db).Model(&model.ClientHwid{}).Where("sub_id = ? AND hwid_hash = ?", subID, hwidHash).Count(&held).Error
	return held > 0, err
}

// HwidSlotStatusForSubID is SELECT-only: it must never write client_hwids or
// last_seen. It shares the gate's limit query, so limit == limit enforced.
func (s *ClientService) HwidSlotStatusForSubID(subID string) (status HwidSlotStatus, found bool, err error) {
	subID = strings.TrimSpace(subID)
	if subID == "" {
		return status, false, nil
	}

	db := database.GetDB()
	known, err := subIDHasEnabledClient(db, subID)
	if err != nil || !known {
		return status, false, err
	}

	policy, err := loadHwidPolicy(db)
	if err != nil {
		return status, false, err
	}
	limit, err := effectiveHwidLimitForSubID(db, subID, policy.defaultLimit)
	if err != nil {
		return status, false, err
	}
	if limit <= 0 {
		return status, true, nil
	}

	var registered int64
	if err := policy.liveHwids(db).Model(&model.ClientHwid{}).Where("sub_id = ?", subID).Count(&registered).Error; err != nil {
		return status, false, err
	}
	status.Active = true
	status.Limit = limit
	status.Registered = int(registered)
	status.Remaining = max(limit-status.Registered, 0)
	status.Full = status.Registered >= limit
	return status, true, nil
}

func (s *ClientService) ListClientHwids(email string) ([]ClientHwidInfo, error) {
	rec, err := s.GetRecordByEmail(nil, email)
	if err != nil {
		return nil, err
	}
	subID := strings.TrimSpace(rec.SubID)
	if subID == "" {
		return nil, nil
	}
	db := database.GetDB()
	policy, err := loadHwidPolicy(db)
	if err != nil {
		return nil, err
	}
	var rows []model.ClientHwid
	if err := policy.liveHwids(db).
		Where("sub_id = ?", subID).
		Order("last_seen DESC").
		Order("id DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ClientHwidInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClientHwidInfo{
			Id:          r.Id,
			FirstSeen:   r.FirstSeen,
			LastSeen:    r.LastSeen,
			UserAgent:   r.UserAgent,
			DeviceOS:    r.DeviceOS,
			OsVersion:   r.OsVersion,
			DeviceModel: r.DeviceModel,
			Fingerprint: shortHwidFingerprint(r.HwidHash),
			NoID:        r.HwidHash == noIDHwidHash,
		})
		if r.HwidHash == noIDHwidHash {
			out[len(out)-1].Fingerprint = ""
		}
	}
	return out, nil
}

func (s *ClientService) ClearClientHwids(email string) error {
	rec, err := s.GetRecordByEmail(nil, email)
	if err != nil {
		return err
	}
	subID := strings.TrimSpace(rec.SubID)
	if subID == "" {
		return nil
	}
	return database.GetDB().Where("sub_id = ?", subID).Delete(&model.ClientHwid{}).Error
}

// DeleteClientHwid removes one device, scoped to the client's sub_id: ids
// are a global auto-increment, so an id outside this subscription won't match.
func (s *ClientService) DeleteClientHwid(email string, id int) error {
	rec, err := s.GetRecordByEmail(nil, email)
	if err != nil {
		return err
	}
	subID := strings.TrimSpace(rec.SubID)
	if subID == "" {
		return errors.New("client has no subscription id")
	}
	res := database.GetDB().Where("sub_id = ? AND id = ?", subID, id).Delete(&model.ClientHwid{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("device not found")
	}
	return nil
}

// PurgeExpiredClientHwids drops every device not seen for the configured days; the gate
// only does so for the subscription being fetched.
func (s *ClientService) PurgeExpiredClientHwids() error {
	db := database.GetDB()
	policy, err := loadHwidPolicy(db)
	if err != nil {
		return err
	}
	return policy.staleHwids(db).Delete(&model.ClientHwid{}).Error
}

// applyHwidPanelDefault brings the registry under the panel default when settings are saved, the
// way a client's own limit does when it is set: a subscription keeps its most recently seen devices.
func applyHwidPanelDefault() error {
	return runSerializedTx(func(tx *gorm.DB) error {
		policy, err := loadHwidPolicy(tx)
		if err != nil || policy.defaultLimit <= 0 {
			return err
		}
		if err := policy.staleHwids(tx).Delete(&model.ClientHwid{}).Error; err != nil {
			return err
		}
		var subIDs []string
		if err := tx.Model(&model.ClientHwid{}).Group("sub_id").
			Having("COUNT(*) > ?", policy.defaultLimit).Pluck("sub_id", &subIDs).Error; err != nil {
			return err
		}
		for _, subID := range subIDs {
			// The lock resolves the limit too: where a client has its own, that one counts.
			limit, err := lockHwidLimitForSubID(tx, subID, policy.defaultLimit)
			if err != nil {
				return err
			}
			if err := trimClientHwidsForSubID(tx, subID, limit); err != nil {
				return err
			}
		}
		return nil
	})
}

// Serialize the limit write and trim with SyncInbound and client deletion.
func (s *ClientService) setClientLimitHwidByEmail(email string, limit int) error {
	return runSerializedTx(func(tx *gorm.DB) error {
		return s.setClientLimitHwidByEmailTx(tx, email, limit)
	})
}

func (s *ClientService) setClientLimitHwidByEmailTx(tx *gorm.DB, email string, limit int) error {
	if !isSerializedTx(tx) {
		return errClientHwidWriteNotSerialized
	}
	if limit < 0 {
		limit = 0
	}
	var rec model.ClientRecord
	if err := tx.Where("email = ?", email).First(&rec).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.ClientRecord{}).Where("id = ?", rec.Id).UpdateColumn("limit_hwid", limit).Error; err != nil {
		return err
	}
	subID := strings.TrimSpace(rec.SubID)
	if subID == "" {
		return nil
	}
	policy, err := loadHwidPolicy(tx)
	if err != nil {
		return err
	}
	effective, err := effectiveHwidLimitForSubID(tx, subID, policy.defaultLimit)
	if err != nil {
		return err
	}
	return trimClientHwidsForSubID(tx, subID, effective)
}

func trimClientHwidsForSubID(tx *gorm.DB, subID string, limit int) error {
	subID = strings.TrimSpace(subID)
	if subID == "" || limit <= 0 {
		return nil
	}
	var keep []int
	if err := tx.Model(&model.ClientHwid{}).
		Where("sub_id = ?", subID).
		Order("last_seen DESC").
		Order("id DESC").
		Limit(limit).
		Pluck("id", &keep).Error; err != nil {
		return err
	}
	if len(keep) == 0 {
		return tx.Where("sub_id = ?", subID).Delete(&model.ClientHwid{}).Error
	}
	return tx.Where("sub_id = ? AND id NOT IN ?", subID, keep).Delete(&model.ClientHwid{}).Error
}

func clearClientHwidsBySubIDTx(tx *gorm.DB, subIDs ...string) error {
	if !isSerializedTx(tx) {
		return errClientHwidWriteNotSerialized
	}
	clean := make([]string, 0, len(subIDs))
	seen := map[string]struct{}{}
	for _, subID := range subIDs {
		subID = strings.TrimSpace(subID)
		if subID == "" {
			continue
		}
		if _, ok := seen[subID]; ok {
			continue
		}
		seen[subID] = struct{}{}
		clean = append(clean, subID)
	}
	for _, batch := range chunkStrings(clean, sqlInChunk) {
		if err := tx.Where("sub_id IN ?", batch).Delete(&model.ClientHwid{}).Error; err != nil {
			return err
		}
	}
	return nil
}
