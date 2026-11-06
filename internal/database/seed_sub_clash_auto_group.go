package database

import (
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const subClashAutoGroupSeeder = "SubClashAutoGroupKeepOff"

// seedSubClashAutoGroupKeepOff pins the Auto group off on panels older than it, so subscribers find
// no new default on refresh; a fresh install keeps the default (on), a saved choice is kept.
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
