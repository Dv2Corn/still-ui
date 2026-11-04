package tgbot

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// callSites maps "callee" to the functions of dir (non-test files) that call it, by the name of
// the called function or method.
func callSites(t *testing.T, dir string, callees ...string) map[string][]string {
	t.Helper()
	want := map[string]bool{}
	for _, c := range callees {
		want[c] = true
	}
	out := map[string][]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := ""
				switch f := call.Fun.(type) {
				case *ast.Ident:
					callee = f.Name
				case *ast.SelectorExpr:
					callee = f.Sel.Name
				}
				if want[callee] {
					out[callee] = append(out[callee], name+":"+fn.Name.Name)
				}
				return true
			})
		}
	}
	return out
}

func onlyFrom(t *testing.T, sites map[string][]string, callee string, allowed ...string) {
	t.Helper()
	for _, site := range sites[callee] {
		fn := site[strings.Index(site, ":")+1:]
		ok := false
		for _, a := range allowed {
			ok = ok || fn == a
		}
		if !ok {
			t.Errorf("%s calls %s: backup bytes must reach a chat only through the encrypting helpers (allowed: %v)", site, callee, allowed)
		}
	}
}

// STILL-BKP-1 guard: a new path that posts the database or the Xray config to Telegram or
// Discord without service.PrepareChatBackup/CheckChatBackup fails here. The raw upload function
// is reachable only through deliverBackup, and nothing in the bots reads the DB or config itself.
func TestBackupBytesLeaveOnlyThroughDeliverBackup(t *testing.T) {
	tg := callSites(t, ".", "sendDocumentWithCaption", "SendDocument", "GetDb", "GetConfigPath", "FileFromBytes")
	onlyFrom(t, tg, "sendDocumentWithCaption", "deliverBackup")
	// The ban log is the one other file the bot uploads; it never carries the DB.
	onlyFrom(t, tg, "SendDocument", "sendDocumentWithCaption", "sendBanLogsFile")
	// Bytes uploads: the backup (through deliverBackup), the QR sheet and the screen artwork.
	onlyFrom(t, tg, "FileFromBytes", "sendDocumentWithCaption", "qrSheetPicture", "artUpload")
	onlyFrom(t, tg, "GetDb")
	onlyFrom(t, tg, "GetConfigPath")
	if len(tg["sendDocumentWithCaption"]) == 0 || len(tg["SendDocument"]) == 0 {
		t.Fatal("the guard found no upload calls at all: it no longer looks at the right code")
	}

	dc := callSites(t, filepath.Join("..", "discord"), "GetDb", "GetConfigPath", "PrepareChatBackup")
	onlyFrom(t, dc, "GetDb")
	onlyFrom(t, dc, "GetConfigPath")
	onlyFrom(t, dc, "PrepareChatBackup", "backupAttachments")

	svc := callSites(t, "..", "GetDb")
	onlyFrom(t, svc, "GetDb", "PrepareChatBackup")
}
