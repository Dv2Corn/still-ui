package service

import (
	"cmp"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// nodeBillingSettingKey maps each master GUID to the multiplier it bills this panel's traffic at.
const nodeBillingSettingKey = "nodeTrafficMultipliers"

// A master that stopped pushing for this long no longer bills this panel; refreshes are hourly.
const (
	nodeBillingTTL     = 7 * 24 * time.Hour
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

// SetMasterBillingMultiplier records the multiplier a master bills this panel at (×1 included).
func (s *InboundService) SetMasterBillingMultiplier(masterGuid string, m float64) error {
	masterGuid = strings.TrimSpace(masterGuid)
	if masterGuid == "" || len(masterGuid) > maxPanelGuidLen || math.IsNaN(m) ||
		m < nodeTrafficMultiplierMin || m > nodeTrafficMultiplierMax {
		return nil
	}
	db := database.GetDB()
	now := time.Now()
	if old, ok := readMasterBillingMultipliers(db)[masterGuid]; ok && old.M == m && now.Sub(time.UnixMilli(old.At)) < nodeBillingRefresh {
		return nil
	}
	return submitTrafficWrite(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			all := readMasterBillingMultipliers(tx)
			cutoff := now.Add(-nodeBillingTTL).UnixMilli()
			for g, e := range all {
				if e.At < cutoff {
					delete(all, g)
				}
			}
			all[masterGuid] = nodeBillingEntry{M: m, At: now.UnixMilli()}
			raw, err := json.Marshal(all)
			if err != nil {
				return err
			}
			if err := tx.Where("key = ?", nodeBillingSettingKey).Delete(&model.Setting{}).Error; err != nil {
				return err
			}
			return tx.Create(&model.Setting{Key: nodeBillingSettingKey, Value: string(raw)}).Error
		})
	})
}

// liveMasterMultipliers are the multipliers of masters that pushed within the TTL.
func liveMasterMultipliers(db *gorm.DB) map[string]float64 {
	cutoff := time.Now().Add(-nodeBillingTTL).UnixMilli()
	out := map[string]float64{}
	for g, e := range readMasterBillingMultipliers(db) {
		if e.At >= cutoff {
			out[g] = effectiveTrafficMultiplier(e.M)
		}
	}
	return out
}

// billingTier is the masters billing at one multiplier other than 1.
type billingTier struct {
	m     float64
	guids []string
}

func billingTiers(db *gorm.DB) []billingTier {
	by := map[float64][]string{}
	for g, m := range liveMasterMultipliers(db) {
		if m != 1 {
			by[m] = append(by[m], g)
		}
	}
	tiers := make([]billingTier, 0, len(by))
	for m, guids := range by {
		slices.Sort(guids)
		tiers = append(tiers, billingTier{m: m, guids: guids})
	}
	slices.SortFunc(tiers, func(a, b billingTier) int { return cmp.Compare(a.m, b.m) })
	return tiers
}

// NodeBillingMultiplier is what this master bills the node's traffic at, pushed to the node.
func NodeBillingMultiplier(n *model.Node) float64 {
	return effectiveTrafficMultiplier(n.TrafficMultiplier)
}

func sqlFactor(m float64) string {
	f := strconv.FormatFloat(m, 'f', -1, 64)
	if !strings.Contains(f, ".") {
		f += ".0" // a float literal: no bigint overflow on PostgreSQL
	}
	return f
}

// withBilledQuota rewrites the own-counter quota test of cond per client: a client a master
// bills at m≠1 is judged by that master's fresh push only, else by its own counters × m.
func withBilledQuota(db *gorm.DB, cond string, args []any) (string, []any) {
	tiers := billingTiers(db)
	if len(tiers) == 0 {
		return cond, args
	}
	const own = "up + down >= total"
	has := func(op string) string {
		return "EXISTS (SELECT 1 FROM client_global_traffics b WHERE b.email = client_traffics.email AND b.master_guid " + op + " ?)"
	}
	noFresh := "NOT EXISTS (SELECT 1 FROM client_global_traffics f WHERE f.email = client_traffics.email AND f.updated_at >= ?)"
	freshSince := globalTrafficFreshSince(db.NowFunc())
	var all, above []string
	for _, t := range tiers {
		all = append(all, t.guids...)
		if t.m > 1 {
			above = append(above, t.guids...)
		}
	}
	var b strings.Builder
	var out []any
	b.WriteString("(CASE")
	one := func() {
		b.WriteString(" WHEN (" + has("NOT IN") + ")")
		out = append(out, all)
		if len(above) > 0 {
			b.WriteString(" OR NOT " + has("IN"))
			out = append(out, above)
		} else {
			b.WriteString(" OR 1 = 1")
		}
		b.WriteString(" THEN " + own)
	}
	oneDone := false
	for _, t := range tiers {
		if t.m > 1 && !oneDone {
			one()
			oneDone = true
		}
		f := sqlFactor(t.m)
		b.WriteString(" WHEN " + has("IN") + " THEN (" + noFresh + " AND ROUND(up * " + f + ") + ROUND(down * " + f + ") >= total)")
		out = append(out, t.guids, freshSince)
	}
	if !oneDone {
		one()
	}
	b.WriteString(" ELSE " + own + " END)")
	return strings.Replace(cond, own, b.String(), 1), append(out, args...)
}

// billedUsage rewrites rows' own counters as the quota check sees them, in place: a client
// billed at m≠1 carries its master's fresh push, or without one its counters × m.
func billedUsage(db *gorm.DB, rows []xray.ClientTraffic) {
	live := liveMasterMultipliers(db)
	billed := false
	for _, m := range live {
		billed = billed || m != 1
	}
	if !billed || len(rows) == 0 {
		return
	}
	emails := make([]string, 0, len(rows))
	for i := range rows {
		emails = append(emails, rows[i].Email)
	}
	freshSince := globalTrafficFreshSince(db.NowFunc())
	type usage struct {
		m        float64
		fresh    bool
		up, down int64
		anyRows  bool
	}
	by := map[string]*usage{}
	for _, batch := range chunkStrings(uniqueNonEmptyStrings(emails), sqlInChunk) {
		var globals []model.ClientGlobalTraffic
		if err := db.Where("email IN ?", batch).Find(&globals).Error; err != nil {
			return
		}
		for _, g := range globals {
			key := strings.ToLower(g.Email)
			u := by[key]
			if u == nil {
				u = &usage{m: 1}
				by[key] = u
			}
			m, ok := live[g.MasterGuid]
			if !ok {
				m = 1
			}
			if !u.anyRows || m < u.m {
				u.m = m
			}
			u.anyRows = true
			if g.UpdatedAt >= freshSince {
				u.fresh = true
				u.up, u.down = max(u.up, g.Up), max(u.down, g.Down)
			}
		}
	}
	for i := range rows {
		u := by[strings.ToLower(rows[i].Email)]
		switch {
		case u == nil || u.m == 1:
		case u.fresh:
			rows[i].Up, rows[i].Down = u.up, u.down
		default:
			rows[i].Up, rows[i].Down = scaleNodeTraffic(rows[i].Up, u.m), scaleNodeTraffic(rows[i].Down, u.m)
		}
	}
}
