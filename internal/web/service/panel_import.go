package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
)

// ErrPanelNotEmpty refuses an import into a panel that already has inbounds without Merge.
var ErrPanelNotEmpty = errors.New("this panel already has inbounds: import with merge to add to them, skipping what conflicts")

const marzbanSubSecretKey = "marzbanSubSecret"

type PanelImportOptions struct {
	DryRun bool
	Merge  bool
	UserId int
}

// PanelImportInbound is one source inbound and what the import does with it.
type PanelImportInbound struct {
	Tag      string `json:"tag" example:"VLESS TCP REALITY"`
	Protocol string `json:"protocol" example:"vless"`
	Port     int    `json:"port" example:"443"`
	Clients  int    `json:"clients" example:"12"`
	Hosts    int    `json:"hosts" example:"1"`
	// Skipped is why the inbound is left out; empty when it is imported.
	Skipped string `json:"skipped" example:""`
}

// PanelImportReport is the dry-run answer and, with Applied, the record of a real import.
type PanelImportReport struct {
	Source   string               `json:"source" example:"marzban"`
	Applied  bool                 `json:"applied" example:"false"`
	NotEmpty bool                 `json:"notEmpty" example:"false"`
	Inbounds []PanelImportInbound `json:"inbounds"`
	Clients  int                  `json:"clients" example:"12"`
	// LegacySubLinks counts the users whose subscription links from the source keep working.
	LegacySubLinks int      `json:"legacySubLinks" example:"12"`
	Notes          []string `json:"notes" example:"[\"user bob: the yearly traffic reset has no equivalent, set to never\"]"`
	Admins         []string `json:"admins" example:"[\"admin\"]"`
	Backup         string   `json:"backup" example:"/etc/still-ui/backups/still-ui-host-20261111T101500Z.db"`
}

type PanelImportService struct {
	inboundService InboundService
}

type plannedInbound struct {
	inbound   *model.Inbound
	clients   []model.Client
	shareAddr string
	hosts     []model.Host
}

// Run checks a plan against this panel and, unless DryRun, backs the database up and
// writes everything the report lists in one transaction.
func (s *PanelImportService) Run(plan *panelimport.Plan, opts PanelImportOptions) (*PanelImportReport, error) {
	report := &PanelImportReport{
		Source: plan.Source, Inbounds: []PanelImportInbound{},
		Notes: append([]string{}, plan.Notes...), Admins: append([]string{}, plan.Admins...),
	}
	var existing int64
	if err := database.GetDB().Model(&model.Inbound{}).Count(&existing).Error; err != nil {
		return nil, err
	}
	report.NotEmpty = existing > 0

	screen, err := s.newScreen(plan)
	if err != nil {
		return nil, err
	}
	var accepted []plannedInbound
	subIDs := map[string]bool{}
	emails := map[string]bool{}
	for _, in := range plan.Inbounds {
		in.UserId = opts.UserId
		entry := PanelImportInbound{Tag: in.Tag, Protocol: string(in.Protocol), Port: in.Port}
		planned, err := s.check(in, screen, report)
		if err != nil {
			entry.Skipped = err.Error()
			report.Inbounds = append(report.Inbounds, entry)
			continue
		}
		planned.hosts = plan.Hosts[entry.Tag]
		entry.Tag, entry.Clients, entry.Hosts = in.Tag, len(planned.clients), len(planned.hosts)
		for _, c := range planned.clients {
			emails[strings.ToLower(c.Email)] = true
			subIDs[c.SubID] = true
		}
		report.Inbounds = append(report.Inbounds, entry)
		accepted = append(accepted, planned)
	}
	report.Clients = len(emails)
	if n := len(screen.shared); n > 0 {
		// One subscription for several inbounds is how x-ui does it: a line each would bury the report.
		report.Notes = append(report.Notes, fmt.Sprintf("subscription links that several clients share in the source stay shared here: %d", n))
	}

	subUsers, err := s.legacySubUsers(plan, subIDs, report)
	if err != nil {
		return nil, err
	}
	report.LegacySubLinks = len(subUsers)
	if opts.DryRun {
		return report, nil
	}
	if report.NotEmpty && !opts.Merge {
		return report, ErrPanelNotEmpty
	}

	backup, err := (&LocalBackupService{}).Run()
	if err != nil {
		return report, fmt.Errorf("no backup, nothing imported: %w", err)
	}
	report.Backup = filepath.Join(config.GetBackupFolderPath(), backup.File)

	outcomes := make([]*inboundAddOutcome, len(accepted))
	err = runSerializedTx(func(tx *gorm.DB) error {
		for i, p := range accepted {
			outcomes[i] = &inboundAddOutcome{}
			if err := s.inboundService.addInboundTx(tx, p.inbound, p.clients, p.shareAddr, outcomes[i]); err != nil {
				return fmt.Errorf("inbound %q: %w", p.inbound.Tag, err)
			}
			for _, host := range p.hosts {
				host.InboundId, host.GroupId = p.inbound.Id, random.NumLower(16)
				if err := tx.Create(&host).Error; err != nil {
					return fmt.Errorf("host %q of inbound %q: %w", host.Remark, p.inbound.Tag, err)
				}
			}
		}
		if len(subUsers) == 0 {
			return nil
		}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(subUsers, 200).Error; err != nil {
			return err
		}
		var secret model.Setting
		if err := tx.Where("key = ?", marzbanSubSecretKey).Take(&secret).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		secret.Key, secret.Value = marzbanSubSecretKey, plan.SubSecret
		return tx.Save(&secret).Error
	})
	if err != nil {
		return report, fmt.Errorf("nothing imported: %w", err)
	}
	InvalidateSubData()
	for _, out := range outcomes {
		if out.postCommit != nil {
			out.postCommit()
		}
	}
	report.Applied = true
	return report, nil
}

// RunUpload stages an uploaded source database in a private temporary file, since SQLite
// reads from a path, and runs the import from it.
func (s *PanelImportService) RunUpload(source string, db io.Reader, xrayConfig []byte, opts PanelImportOptions) (*PanelImportReport, error) {
	dir, err := os.MkdirTemp("", "still-ui-import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "source.db")
	staged, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(staged, db)
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	plan, err := panelimport.Read(source, path, xrayConfig)
	if err != nil {
		return nil, err
	}
	return s.Run(plan, opts)
}

// check prepares one inbound for this panel, dropping the clients the screen refuses.
func (s *PanelImportService) check(in *model.Inbound, screen *importScreen, report *PanelImportReport) (plannedInbound, error) {
	if in.Tag != "" {
		taken, err := s.inboundService.tagExists(in.Tag, 0)
		if err != nil {
			return plannedInbound{}, err
		}
		if taken {
			return plannedInbound{}, errors.New("an inbound with this tag already exists")
		}
	}
	conflict, err := checkPortConflictTx(database.GetDB(), in, 0)
	if err != nil {
		return plannedInbound{}, err
	}
	if conflict != nil {
		return plannedInbound{}, errors.New(conflict.String())
	}
	if err := screen.apply(in, report); err != nil {
		return plannedInbound{}, err
	}
	if err := normalizeVlessFallbacks(in); err != nil {
		return plannedInbound{}, err
	}
	clients, shareAddr, err := s.inboundService.prepareInboundAdd(in)
	if err != nil {
		return plannedInbound{}, err
	}
	return plannedInbound{inbound: in, clients: clients, shareAddr: shareAddr}, nil
}

// importScreen knows who holds each email and subscription id, on this panel and earlier
// in the plan: a subscription id is what a subscription link is looked up by.
type importScreen struct {
	source        string
	panelEmails   map[string]string
	panelSubOwner map[string]string
	planEmailSub  map[string]string
	planSubOwner  map[string]string
	// shared are the subscription ids several clients of an x-ui source hold together.
	shared map[string]bool
}

func (s *PanelImportService) newScreen(plan *panelimport.Plan) (*importScreen, error) {
	sc := &importScreen{
		source: plan.Source, panelSubOwner: map[string]string{}, planEmailSub: map[string]string{},
		planSubOwner: map[string]string{}, shared: map[string]bool{},
	}
	var all []model.Client
	seen := map[string]struct{}{}
	var subIDs []string
	for _, in := range plan.Inbounds {
		// An inbound whose settings do not parse is reported by its own check.
		clients, _ := ParseInboundSettingsClients(in.Settings)
		for _, c := range clients {
			all = append(all, c)
			if _, dup := seen[c.SubID]; c.SubID != "" && !dup {
				seen[c.SubID] = struct{}{}
				subIDs = append(subIDs, c.SubID)
			}
		}
	}
	var err error
	if sc.panelEmails, err = s.inboundService.emailSubIDsForClients(all); err != nil {
		return nil, err
	}
	for _, batch := range chunkStrings(subIDs, sqlInChunk) {
		var rows []model.ClientRecord
		if err := database.GetDB().Select("email", "sub_id").Where("sub_id IN ?", batch).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			sc.panelSubOwner[r.SubID] = r.Email
		}
	}
	return sc, nil
}

// apply drops from an inbound every client whose email or subscription id belongs to
// someone else, here or earlier in the plan, and says so in the report.
func (sc *importScreen) apply(in *model.Inbound, report *PanelImportReport) error {
	var settings map[string]any
	if err := json.Unmarshal([]byte(in.Settings), &settings); err != nil {
		return err
	}
	list, _ := settings["clients"].([]any)
	kept := make([]any, 0, len(list))
	dropped := map[string]bool{}
	note := func(format string, args ...any) {
		report.Notes = append(report.Notes, fmt.Sprintf("inbound %q: ", in.Tag)+fmt.Sprintf(format, args...))
	}
	for _, raw := range list {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		email, _ := c["email"].(string)
		sub, _ := c["subId"].(string)
		key := strings.ToLower(email)
		reason := ""
		// A client this panel has is never touched: attaching would overwrite its state with the source's.
		if _, exists := sc.panelEmails[key]; exists {
			reason = "already exists here"
		} else if owner, taken := sc.panelSubOwner[sub]; taken && sub != "" {
			reason = fmt.Sprintf("has the subscription id of client %q of this panel", owner)
		} else if first, again := sc.planEmailSub[key]; again && (sub == "" || first != sub) {
			reason = "appears again in the source with another subscription id"
		}
		if reason != "" {
			dropped[key] = true
			note("client %q %s, skipped", email, reason)
			continue
		}
		owner, held := sc.planSubOwner[sub]
		if held && owner != key && sc.source != panelimport.SourceMarzban {
			sc.shared[sub] = true
		}
		sc.planEmailSub[key] = sub
		if sub != "" && !held {
			sc.planSubOwner[sub] = key
		}
		kept = append(kept, c)
	}
	settings["clients"] = kept
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	in.Settings = string(b)
	stats := in.ClientStats[:0]
	for _, stat := range in.ClientStats {
		if !dropped[strings.ToLower(stat.Email)] {
			stats = append(stats, stat)
		}
	}
	in.ClientStats = stats
	return nil
}

// legacySubUsers keeps the source links of the users that are imported; one panel holds
// one signing secret, so a second source with another secret gets none.
func (s *PanelImportService) legacySubUsers(plan *panelimport.Plan, subIDs map[string]bool, report *PanelImportReport) ([]model.LegacySubUser, error) {
	if plan.SubSecret == "" || len(plan.SubUsers) == 0 {
		return nil, nil
	}
	current, err := (&SettingService{}).GetMarzbanSubSecret()
	if err != nil {
		return nil, err
	}
	if current != "" && current != plan.SubSecret {
		report.Notes = append(report.Notes, "this panel already accepts the subscription links of another Marzban; the links of this one will not work, send users the new ones")
		return nil, nil
	}
	users := make([]model.LegacySubUser, 0, len(plan.SubUsers))
	for _, u := range plan.SubUsers {
		if subIDs[u.SubID] {
			users = append(users, u)
		}
	}
	if len(users) > 0 {
		report.Notes = append(report.Notes, "Marzban's subscription links keep working where their address points at this panel's subscription server; its signing secret is kept here for that, and whoever has it can open these subscriptions until you run: still-ui import forget-links")
	}
	return users, nil
}

// ResolveLegacySubID turns a subscription link of an imported Marzban into the
// subscription it now belongs to; ok is false for anything else, returned unchanged.
func ResolveLegacySubID(token string) (subID string, ok bool) {
	if len(token) < 15 {
		return token, false
	}
	// Read each time, not cached: the command line forgets the links from another process.
	secret, err := (&SettingService{}).GetMarzbanSubSecret()
	if err != nil || secret == "" {
		return token, false
	}
	username, issuedAt, valid := panelimport.VerifyMarzbanToken(secret, token)
	if !valid {
		return token, false
	}
	var user model.LegacySubUser
	err = database.GetDB().Where("username = ?", strings.ToLower(username)).Take(&user).Error
	if err != nil || issuedAt < user.NotBefore {
		return token, false
	}
	return user.SubID, true
}

// ForgetLegacySubLinks stops accepting the subscription links of an imported panel.
func ForgetLegacySubLinks() error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&model.LegacySubUser{}).Error; err != nil {
			return err
		}
		return tx.Where("key = ?", marzbanSubSecretKey).Delete(&model.Setting{}).Error
	})
}
