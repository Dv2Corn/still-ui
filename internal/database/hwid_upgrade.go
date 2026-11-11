package database

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

// Device-limit settings the upgrade seeder below writes, with the values an upgraded panel keeps.
const (
	HwidNoIdPolicySetting = "subHwidNoIdPolicy"
	HwidNoIdAllow         = "allow"
	HwidNoIdRefuse        = "refuse"
	HwidExpireDaysSetting = "subHwidExpireDays"
)

// An upgraded panel keeps what its subscribers have. Apps without a device id stay refused where
// devices were limited and served elsewhere, and no registered device expires until the admin says so.
func keepHwidBehaviourOnUpgrade() error {
	return db.Transaction(func(tx *gorm.DB) error {
		var limited int64
		if err := tx.Model(&model.ClientRecord{}).Where("limit_hwid > 0").Count(&limited).Error; err != nil {
			return err
		}
		noID := HwidNoIdAllow
		if limited > 0 {
			noID = HwidNoIdRefuse
		}
		for _, keep := range []model.Setting{
			{Key: HwidNoIdPolicySetting, Value: noID},
			{Key: HwidExpireDaysSetting, Value: "0"},
		} {
			var stored int64
			if err := tx.Model(&model.Setting{}).Where("key = ?", keep.Key).Count(&stored).Error; err != nil {
				return err
			}
			if stored > 0 {
				continue
			}
			if err := tx.Create(&keep).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.HistoryOfSeeders{SeederName: "HwidUpgradeKeep"}).Error
	})
}
