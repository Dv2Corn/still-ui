package database

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

// HwidHashKeySetting names the settings row holding this panel's device-hash key.
// A settings reset must keep it: without it every registered device reads as new.
const HwidHashKeySetting = "hwidHashKey"

// HwidNoIdPolicySetting and its values live here because the upgrade seeder below writes them.
const (
	HwidNoIdPolicySetting = "subHwidNoIdPolicy"
	HwidNoIdAllow         = "allow"
	HwidNoIdRefuse        = "refuse"
)

// KeyedHwidHash is what client_hwids stores for a device: keyed per panel and bound to the
// subscription, so neither two leaked tables nor two subscriptions join on one device.
func KeyedHwidHash(key []byte, subID, digest string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(subID))
	mac.Write([]byte{0})
	mac.Write([]byte(digest))
	return hex.EncodeToString(mac.Sum(nil))
}

// HwidHashKey returns the panel's device-hash key, creating it on first use.
func HwidHashKey(tx *gorm.DB) ([]byte, error) {
	var stored model.Setting
	err := tx.Where("key = ?", HwidHashKeySetting).First(&stored).Error
	if err == nil && stored.Value != "" {
		return []byte(stored.Value), nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(raw)
	if stored.Id != 0 {
		err = tx.Model(&model.Setting{}).Where("id = ?", stored.Id).UpdateColumn("value", key).Error
	} else {
		err = tx.Create(&model.Setting{Key: HwidHashKeySetting, Value: key}).Error
	}
	return []byte(key), err
}

// client_hwids held a plain SHA-256 of the device id; the raw id is gone, so the keyed hash
// is taken over that digest, here for stored rows and in the gate for new ones.
func rehashClientHwidsKeyed() error {
	return db.Transaction(func(tx *gorm.DB) error {
		key, err := HwidHashKey(tx)
		if err != nil {
			return err
		}
		var rows []model.ClientHwid
		if err := tx.Select("id", "sub_id", "hwid_hash").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := tx.Model(&model.ClientHwid{}).Where("id = ?", row.Id).
				UpdateColumn("hwid_hash", KeyedHwidHash(key, row.SubID, row.HwidHash)).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.HistoryOfSeeders{SeederName: "ClientHwidKeyedHash"}).Error
	})
}

// An upgraded panel keeps what its subscribers have: one that limits devices went on refusing
// apps without a device id, any other served them. Only a new panel gets the shared slot.
func keepHwidNoIdPolicy() error {
	return db.Transaction(func(tx *gorm.DB) error {
		var limited, stored int64
		if err := tx.Model(&model.ClientRecord{}).Where("limit_hwid > 0").Count(&limited).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Setting{}).Where("key = ?", HwidNoIdPolicySetting).Count(&stored).Error; err != nil {
			return err
		}
		if stored == 0 {
			policy := HwidNoIdAllow
			if limited > 0 {
				policy = HwidNoIdRefuse
			}
			if err := tx.Create(&model.Setting{Key: HwidNoIdPolicySetting, Value: policy}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.HistoryOfSeeders{SeederName: "HwidNoIdPolicyKeep"}).Error
	})
}
