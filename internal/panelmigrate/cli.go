package panelmigrate

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

// cliUsage documents "x-ui migrate-3xui"; migrate.sh is its intended caller.
const cliUsage = `usage: x-ui migrate-3xui <action> [flags]
  version                         print the 3x-ui release this still-ui is based on
  inspect  -db FILE | -postgres   report what a migration must keep (read-only);
           [-format json|env]     -postgres reads the DSN from $XUI_DB_DSN
  backup   -src FILE -dst FILE    consistent SQLite copy (VACUUM INTO) + integrity check
  verify   -pre FILE (-db FILE | -postgres)
                                  compare a saved "inspect -format json" report with a database
  pgenv                           print $XUI_DB_DSN as libpq environment variables
                                  ("NAME<TAB>value"), so pg_dump never gets it in argv
  subcompare -a FILE -b FILE      compare two subscription bodies, before (a) and after (b):
                                  exit 0 identical, 3 identical apart from remarks, 4 the
                                  same settings with parameters added in b, 1 different
`

// RunCLI implements "x-ui migrate-3xui" and returns the process exit code.
// The PostgreSQL DSN is read from the environment, never from the command
// line, where every local user could read its password in the process list.
func RunCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, cliUsage)
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	usage := func() int { fmt.Fprint(stderr, cliUsage); return 2 }
	failed := func(err error) int { fmt.Fprintf(stderr, "%s failed: %v\n", args[0], err); return 1 }

	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, config.GetUpstreamVersion())
		return 0
	case "inspect", "verify":
		dbPath := fs.String("db", "", "SQLite database file")
		usePostgres := fs.Bool("postgres", false, "use the PostgreSQL database named by $XUI_DB_DSN")
		format := fs.String("format", "json", "json or env (inspect only)")
		prePath := fs.String("pre", "", "report saved before the migration (verify only)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if (*dbPath == "") == !*usePostgres || (*format != "json" && *format != "env") ||
			(args[0] == "verify") != (*prePath != "") {
			return usage()
		}
		report, err := inspectTarget(*dbPath, *usePostgres)
		if err != nil {
			return failed(err)
		}
		if args[0] == "verify" {
			raw, err := os.ReadFile(*prePath)
			if err != nil {
				return failed(err)
			}
			var pre Report
			if err := json.Unmarshal(raw, &pre); err != nil {
				return failed(fmt.Errorf("%s: %w", *prePath, err))
			}
			lines, ok := Verify(&pre, report)
			for _, l := range lines {
				fmt.Fprintln(stdout, l)
			}
			if !ok {
				return 1
			}
			return 0
		}
		if *format == "env" {
			if err := WriteEnv(stdout, report); err != nil {
				return failed(err)
			}
			return 0
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return failed(err)
		}
		return 0
	case "backup":
		src := fs.String("src", "", "SQLite database to copy")
		dst := fs.String("dst", "", "destination file (must not exist)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *src == "" || *dst == "" {
			return usage()
		}
		if err := BackupSQLite(*src, *dst); err != nil {
			return failed(err)
		}
		fmt.Fprintf(stdout, "backup ok: %s\n", *dst)
		return 0
	case "pgenv":
		env, err := PostgresEnv(os.Getenv("XUI_DB_DSN"))
		if err != nil {
			return failed(err)
		}
		if err := WritePostgresEnv(stdout, env); err != nil {
			return failed(err)
		}
		return 0
	case "subcompare":
		a := fs.String("a", "", "first subscription body")
		b := fs.String("b", "", "second subscription body")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *a == "" || *b == "" {
			return usage()
		}
		ra, err := os.ReadFile(*a)
		if err != nil {
			return failed(err)
		}
		rb, err := os.ReadFile(*b)
		if err != nil {
			return failed(err)
		}
		code, note := CompareSubscriptions(ra, rb)
		if note != "" {
			fmt.Fprintln(stdout, note)
		}
		return code
	default:
		return usage()
	}
}

func inspectTarget(dbPath string, usePostgres bool) (*Report, error) {
	if usePostgres {
		return inspectPostgres(os.Getenv("XUI_DB_DSN"))
	}
	return inspectSQLite(dbPath)
}

func inspectSQLite(path string) (*Report, error) {
	gdb, err := OpenSQLiteReadOnly(path)
	if err != nil {
		return nil, err
	}
	if sqlDB, err := gdb.DB(); err == nil {
		defer sqlDB.Close()
	}
	return Inspect(gdb)
}

func inspectPostgres(dsn string) (*Report, error) {
	gdb, err := OpenPostgres(dsn)
	if err != nil {
		return nil, err
	}
	if sqlDB, err := gdb.DB(); err == nil {
		defer sqlDB.Close()
	}
	return Inspect(gdb)
}
