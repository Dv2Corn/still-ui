package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/tgbot"
)

// LoginStatus represents the status of a login attempt.
type LoginStatus byte

const (
	LoginSuccess LoginStatus = 1 // Successful login
	LoginFail    LoginStatus = 0 // Failed login attempt
)

// reportSender is the part of the Telegram bot this job uses.
type reportSender interface{ SendReport() }

// StatsNotifyJob sends periodic statistics reports via Telegram bot.
type StatsNotifyJob struct {
	tgbotService reportSender
}

// NewStatsNotifyJob creates a new statistics notification job instance.
func NewStatsNotifyJob() *StatsNotifyJob {
	return &StatsNotifyJob{tgbotService: &tgbot.Tgbot{}}
}

// Run sends the statistics report (and the DB backup, when enabled). It runs
// while Xray is down too -- the report shows Xray's state, and this is the only
// scheduled backup, so skipping it then hid an outage and lost the backup.
func (j *StatsNotifyJob) Run() {
	j.tgbotService.SendReport()
}
