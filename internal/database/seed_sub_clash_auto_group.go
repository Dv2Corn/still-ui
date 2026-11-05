package database

import (
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const subClashAutoGroupSeeder = "SubClashAutoGroupKeepOff"

// seedSubClashAutoGroupKeepOff pins the Clash/Mihomo "Auto" group off on a
// panel that existed before the group shipped: its subscribers would otherwise
// find a new default choice in their client on the next refresh. A fresh
// install keeps the factory default (on). A saved choice is never touched.
func seedSubClashAutoGroupKeepOff(fresh bool) error {
	var count int64
	if err := db.Model(&model.HistoryOfSeeders{}).
		Where("seeder_name = ?", subClashAutoGroupSeeder).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if !fresh {
			row := model.Setting{Key: "subClashAutoGroup", Value: "false"}
			if err := tx.Where("key = ?", row.Key).FirstOrCreate(&row).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.HistoryOfSeeders{SeederName: subClashAutoGroupSeeder}).Error
	})
}
