package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"filippo.io/age"
	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/backup"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Exit codes of `x-ui xray-unit-check`, read by install.sh and update.sh.
const (
	unitCheckOK      = 0
	unitCheckError   = 1
	unitCheckBlocked = 3
)

// xrayUnitCheck prints what in the panel's current Xray config the still-xray service could not
// run; install.sh and update.sh keep Xray inside the panel when it exits with unitCheckBlocked.
func xrayUnitCheck(args []string) int {
	fs := flag.NewFlagSet("xray-unit-check", flag.ContinueOnError)
	netAdmin := fs.Bool("net-admin", false, "the still-xray service has CAP_NET_ADMIN (the shipped unit grants it)")
	devices := fs.Bool("devices", false, "a drop-in gives the still-xray service the host's /dev (PrivateDevices=no)")
	if err := fs.Parse(args); err != nil {
		return unitCheckError
	}
	logger.InitLogger(logging.ERROR)
	if !database.IsPostgres() {
		if _, err := os.Stat(config.GetDBPath()); errors.Is(err, os.ErrNotExist) {
			return unitCheckOK // a new install: nothing to check yet
		}
	}
	if err := database.InitDB(config.GetDBPath()); err != nil {
		fmt.Fprintln(os.Stderr, "cannot open the database:", err)
		return unitCheckError
	}
	defer func() { _ = database.CloseDB() }()
	cfg, err := (&service.XrayService{}).GetXrayConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot build the Xray config:", err)
		return unitCheckError
	}
	blockers := xray.UnitBlockers(cfg, xray.UnitCaps{NetAdmin: *netAdmin, Devices: *devices})
	for _, b := range blockers {
		fmt.Println(b)
	}
	if len(blockers) > 0 {
		return unitCheckBlocked
	}
	return unitCheckOK
}

// runLocalBackup takes one local backup with the panel's backup settings and prints its path.
func runLocalBackup() int {
	logger.InitLogger(logging.WARNING)
	if err := database.InitDB(config.GetDBPath()); err != nil {
		fmt.Fprintln(os.Stderr, "cannot open the database:", err)
		return 1
	}
	defer func() { _ = database.CloseDB() }()
	res, err := (&service.LocalBackupService{}).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup failed:", err)
		return 1
	}
	fmt.Printf("%s/%s\n", config.GetBackupFolderPath(), res.File)
	fmt.Printf("size=%d encrypted=%v\n", res.Size, res.Encrypted)
	if res.Telegram != "" {
		fmt.Println("telegram:", res.Telegram)
	}
	return 0
}

// panelServiceActive reports a running panel service, which must not have its database replaced.
var panelServiceActive = func() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "is-active", "--quiet", config.ServiceName).Run() == nil
}

// restoreLocalBackup replaces the panel database with a backup (see service.RestoreLocalBackup).
func restoreLocalBackup(args []string, stdin io.Reader) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	file := fs.String("file", "", "backup file: a .db/.pgdump from still-ui, optionally age-encrypted (.age)")
	identity := fs.String("identity", "", "age identity file or OpenSSH private key for an encrypted backup; - reads it from standard input")
	force := fs.Bool("force", false, "restore even though the panel service is running (not recommended)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" && fs.NArg() > 0 {
		*file = fs.Arg(0)
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "usage: x-ui restore -file <backup> [-identity <key file>|-]")
		return 2
	}
	if !*force && panelServiceActive() {
		fmt.Fprintln(os.Stderr, "the panel is running: stop it first (still-ui restore does that for you)")
		return 1
	}
	logger.InitLogger(logging.WARNING)
	var ids []age.Identity
	if *identity != "" {
		var data []byte
		var err error
		if *identity == "-" {
			data, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
		} else {
			data, err = os.ReadFile(*identity)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot read the key:", err)
			return 1
		}
		parsed, err := backup.ParseIdentities(data)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot read the key:", err)
			return 1
		}
		ids = parsed
	}
	res, err := service.RestoreLocalBackup(*file, ids)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore failed:", err)
		return 1
	}
	fmt.Printf("restored %s into the %s database\n", strings.TrimSpace(*file), res.Kind)
	if res.Previous != "" {
		fmt.Println("the previous database is kept at", res.Previous)
	}
	return 0
}
