package service

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// nodeBillingSettingKey maps each master GUID to the multiplier it bills this panel's traffic at.
const nodeBillingSettingKey = "nodeTrafficMultipliers"

// A master that stopped pushing for this long no longer bills this panel; refreshes are hourly.
const (
	nodeBillingTTL     = 30 * 24 * time.Hour
	nodeBillingRefresh = time.Hour
)

type nodeBillingEntry struct {
	M  float64 `json:"m"`
	At int64   `json:"at"`
}

func readMasterBillingMultipliers(db *gorm.DB) map[string]nodeBillingEntry {
	var row model.Setting
	out := map[string]nodeBillingEntry{}
	if err := db.Where("key = ?", nodeBillingSettingKey).Limit(1).Find(&row).Error; err != nil || row.Value == "" {
		return out
	}
	_ = json.Unmarshal([]byte(row.Value), &out)
	return out
}

// SetMasterBillingMultiplier records the multiplier a master bills this panel at; 1 forgets it.
func (s *InboundService) SetMasterBillingMultiplier(masterGuid string, m float64) error {
	masterGuid = strings.TrimSpace(masterGuid)
	if masterGuid == "" || len(masterGuid) > maxPanelGuidLen || math.IsNaN(m) ||
		m < nodeTrafficMultiplierMin || m > nodeTrafficMultiplierMax {
		return nil
	}
	db := database.GetDB()
	now := time.Now()
	if old, ok := readMasterBillingMultipliers(db)[masterGuid]; (!ok && m == 1) ||
		(ok && old.M == m && now.Sub(time.UnixMilli(old.At)) < nodeBillingRefresh) {
		return nil
	}
	return submitTrafficWrite(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			all := readMasterBillingMultipliers(tx)
			if m == 1 {
				delete(all, masterGuid)
			} else {
				all[masterGuid] = nodeBillingEntry{M: m, At: now.UnixMilli()}
			}
			if err := tx.Where("key = ?", nodeBillingSettingKey).Delete(&model.Setting{}).Error; err != nil {
				return err
			}
			if len(all) == 0 {
				return nil
			}
			raw, err := json.Marshal(all)
			if err != nil {
				return err
			}
			return tx.Create(&model.Setting{Key: nodeBillingSettingKey, Value: string(raw)}).Error
		})
	})
}

// localBillingMultiplier is the smallest multiplier any master bills this panel at (1 for none):
// a check on this panel's own counters then never cuts a client before any master would.
func localBillingMultiplier(db *gorm.DB) float64 {
	m, seen := 1.0, false
	cutoff := time.Now().Add(-nodeBillingTTL).UnixMilli()
	for _, e := range readMasterBillingMultipliers(db) {
		if e.At < cutoff {
			continue
		}
		if v := effectiveTrafficMultiplier(e.M); !seen || v < m {
			m, seen = v, true
		}
	}
	return m
}

// NodeBillingMultiplier is what this master bills the node's traffic at, pushed to the node.
func NodeBillingMultiplier(n *model.Node) float64 {
	return effectiveTrafficMultiplier(n.TrafficMultiplier)
}

// billedLocalUsage bills a quota predicate's own counters like the master does (per-counter
// rounding); with multiplier 1 the predicate is returned unchanged.
func billedLocalUsage(cond string, m float64) string {
	if m == 1 {
		return cond
	}
	f := strconv.FormatFloat(m, 'f', -1, 64)
	if !strings.Contains(f, ".") {
		f += ".0" // a float literal: no bigint overflow on PostgreSQL
	}
	billed := "ROUND(up * " + f + ") + ROUND(down * " + f + ") >= total"
	return strings.Replace(cond, "up + down >= total", billed, 1)
}
