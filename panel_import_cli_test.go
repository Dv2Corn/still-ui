package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/panelimport/marzbantest"
)

func importedInbounds(t *testing.T) int64 {
	t.Helper()
	if err := database.InitDB(config.GetDBPath()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.CloseDB() }()
	var n int64
	if err := database.GetDB().Model(&model.Inbound{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPanelImportCLI(t *testing.T) {
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	dbPath, cfg := marzbantest.Build(t, 23000)
	marzbantest.WriteConfig(t, dbPath, cfg)
	folder := filepath.Dir(dbPath)

	var out, errOut bytes.Buffer
	// The flag after the path must count: without it this would be a real import.
	if code := runPanelImport([]string{"marzban", folder, "-dry-run"}, &out, &errOut); code != 0 {
		t.Fatalf("dry run: exit %d, stderr %s", code, errOut.String())
	}
	for _, want := range []string{
		`OK    vless       port 23001 "VLESS TCP REALITY": 5 client(s), 1 host(s)`,
		"would import: 8 client(s); source subscription links that keep working: 6",
		"admins are not imported: root, reseller",
		`note: inbound "VLESS FALLBACK WS": no single numeric port`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out.String())
		}
	}
	if n := importedInbounds(t); n != 0 {
		t.Fatalf("the dry run created %d inbounds", n)
	}

	out.Reset()
	if code := runPanelImport([]string{"marzban", dbPath}, &out, &errOut); code != 0 {
		t.Fatalf("import: exit %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "imported: 8 client(s)") || !strings.Contains(out.String(), "backup taken before the import: ") {
		t.Errorf("import output:\n%s", out.String())
	}
	if n := importedInbounds(t); n != 9 {
		t.Fatalf("%d inbounds after the import, want 9", n)
	}

	errOut.Reset()
	if code := runPanelImport([]string{"marzban", dbPath}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "already has inbounds") {
		t.Errorf("second import without -merge: exit %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	if code := runPanelImport([]string{"marzban", "-merge", dbPath}, &out, &errOut); code != panelImportPartial {
		t.Errorf("second import with -merge: exit %d, want %d (everything skipped)\n%s", code, panelImportPartial, out.String())
	}
	errOut.Reset()
	if code := runPanelImport([]string{"marzban", "mysql+pymysql://u:p@db/marzban"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "only a SQLite file is read") {
		t.Errorf("a database address: exit %d, stderr %q", code, errOut.String())
	}
}
