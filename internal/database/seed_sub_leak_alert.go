package database

import (
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const subLeakAlertSeeder = "SubLeakAlertKeepOff"

// TgEnabledEventsBeforeSubLeak is the Telegram events default of the releases before the sub.leak
// alert: an upgraded panel that never saved its events keeps exactly these.
const TgEnabledEventsBeforeSubLeak = "login.attempt,cpu.high,xray.down,xray.flapping,process.memory,disk.high,cert.expiring,backup.failed,node.traffic"

// seedSubLeakAlertKeepOff keeps the leak alert off on panels older than it; a fresh install keeps
// the default (on), and a saved choice is kept.
func seedSubLeakAlertKeepOff(fresh bool) error {
	var count int64
	if err := db.Model(&model.HistoryOfSeeders{}).
		Where("seeder_name = ?", subLeakAlertSeeder).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if !fresh {
			row := model.Setting{Key: "tgEnabledEvents", Value: TgEnabledEventsBeforeSubLeak}
			if err := tx.Where("key = ?", row.Key).FirstOrCreate(&row).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.HistoryOfSeeders{SeederName: subLeakAlertSeeder}).Error
	})
}
