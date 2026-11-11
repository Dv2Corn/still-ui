package main

// "x-ui import" brings the inbounds and users of Marzban or an old x-ui into this panel;
// "still-ui import" runs it and restarts the panel afterwards.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
)

const panelImportUsage = `usage: x-ui import marzban <db.sqlite3 or its folder> [-config xray_config.json] [-dry-run] [-merge]
       x-ui import x-ui <x-ui.db> [-dry-run] [-merge]
       x-ui import forget-links
  -dry-run  report what would be imported and what cannot be, and write nothing
  -merge    import into a panel that already has inbounds, skipping what conflicts
  forget-links  stop accepting the subscription links an imported Marzban issued
The source is only read. A backup is taken first; "still-ui restore <backup>" undoes the import.
Only a SQLite file is read: a Marzban on MySQL or MariaDB has to be moved to SQLite first.
`

// Exit code of a dry run or import that leaves at least one inbound out.
const panelImportPartial = 3

func runPanelImport(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, panelImportUsage)
		return 2
	}
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "Marzban's xray_config.json (default: beside the database)")
	dryRun := fs.Bool("dry-run", false, "report only, write nothing")
	merge := fs.Bool("merge", false, "import into a panel that already has inbounds")
	// flag stops at the first path, so flags written after it need another pass.
	var paths []string
	for rest := args[1:]; ; rest = fs.Args()[1:] {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		paths = append(paths, fs.Arg(0))
	}

	logger.InitLogger(logging.WARNING)
	if args[0] == "forget-links" && len(paths) == 0 {
		return withPanelDB(stderr, func() int {
			if err := service.ForgetLegacySubLinks(); err != nil {
				fmt.Fprintln(stderr, "failed:", err)
				return 1
			}
			fmt.Fprintln(stdout, "subscription links of the imported panel are no longer accepted")
			return 0
		})
	}
	if len(paths) != 1 {
		fmt.Fprint(stderr, panelImportUsage)
		return 2
	}
	if strings.Contains(paths[0], "://") {
		fmt.Fprintln(stderr, "only a SQLite file is read, not a database address: move the Marzban database to SQLite first")
		return 2
	}
	dbPath := paths[0]
	var xrayConfig []byte
	if args[0] == panelimport.SourceMarzban {
		var cfg string
		dbPath, cfg = panelimport.ResolveMarzbanFiles(dbPath, *configPath)
		raw, err := os.ReadFile(cfg)
		if err != nil {
			fmt.Fprintln(stderr, "cannot read Marzban's Xray config (name it with -config):", err)
			return 1
		}
		xrayConfig = raw
	}
	plan, err := panelimport.Read(args[0], dbPath, xrayConfig)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read the source:", err)
		return 1
	}
	return withPanelDB(stderr, func() int {
		opts := service.PanelImportOptions{DryRun: *dryRun, Merge: *merge}
		if user, err := (&panel.UserService{}).GetFirstUser(); err == nil {
			opts.UserId = user.Id
		}
		report, err := (&service.PanelImportService{}).Run(plan, opts)
		if report != nil {
			printPanelImportReport(stdout, report)
		}
		if err != nil {
			fmt.Fprintln(stderr, "import failed:", err)
			return 1
		}
		for _, in := range report.Inbounds {
			if in.Skipped != "" {
				return panelImportPartial
			}
		}
		return 0
	})
}

func withPanelDB(stderr io.Writer, run func() int) int {
	if err := database.InitDB(config.GetDBPath()); err != nil {
		fmt.Fprintln(stderr, "cannot open the panel database:", err)
		return 1
	}
	defer func() { _ = database.CloseDB() }()
	return run()
}

func printPanelImportReport(w io.Writer, r *service.PanelImportReport) {
	verb := "would import"
	if r.Applied {
		verb = "imported"
	}
	fmt.Fprintf(w, "source: %s\n", r.Source)
	for _, in := range r.Inbounds {
		if in.Skipped != "" {
			fmt.Fprintf(w, "  SKIP  %-11s port %-5d %q: %s\n", in.Protocol, in.Port, in.Tag, in.Skipped)
			continue
		}
		fmt.Fprintf(w, "  OK    %-11s port %-5d %q: %d client(s), %d host(s)\n", in.Protocol, in.Port, in.Tag, in.Clients, in.Hosts)
	}
	fmt.Fprintf(w, "%s: %d client(s); source subscription links that keep working: %d\n", verb, r.Clients, r.LegacySubLinks)
	for _, note := range r.Notes {
		fmt.Fprintln(w, "  note:", note)
	}
	if len(r.Admins) > 0 {
		fmt.Fprintf(w, "admins are not imported: %s\n", strings.Join(r.Admins, ", "))
	}
	if r.NotEmpty && !r.Applied {
		fmt.Fprintln(w, "this panel already has inbounds: a real import needs -merge")
	}
	if r.Backup != "" {
		fmt.Fprintf(w, "backup taken before the import: %s\n", r.Backup)
	}
	if r.Applied {
		fmt.Fprintln(w, "restart the panel to start the imported inbounds (still-ui import does it for you)")
	}
}
