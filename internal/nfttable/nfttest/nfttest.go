// Package nfttest is a fake nft for the tests of the nfttable users.
package nfttest

import (
	"errors"
	"strings"
)

// Runner answers like nft for one table: a "flush set" transaction is recorded
// as a refresh, a ruleset ("{") loads the table, any other one removes it.
type Runner struct {
	NoNft bool
	// NoPermission makes "list tables" fail, as nft does without NET_ADMIN.
	NoPermission bool
	TableUp      bool
	Counters     string
	ApplyErr     error
	Loads        []string
	Removed      int
	Refresh      string
}

func (f *Runner) LookPath(string) error {
	if f.NoNft {
		return errors.New("not found")
	}
	return nil
}

func (f *Runner) Run(stdin string, args ...string) (string, error) {
	switch {
	case strings.Join(args, " ") == "-f -" && strings.HasPrefix(stdin, "flush set"):
		f.Refresh = stdin
	case strings.Join(args, " ") == "-f -" && strings.Contains(stdin, " {"):
		if f.ApplyErr != nil {
			return "Error: syntax error", f.ApplyErr
		}
		f.Loads = append(f.Loads, stdin)
		f.TableUp = true
	case strings.Join(args, " ") == "-f -":
		f.Removed++
		f.TableUp = false
	case strings.Join(args, " ") == "list tables":
		if f.NoPermission {
			return "Operation not permitted (you must be root)", errors.New("exit status 1")
		}
	case args[0] == "list" || args[0] == "-j":
		if !f.TableUp {
			return "Error: No such file or directory", errors.New("exit status 1")
		}
		if args[0] == "-j" {
			return f.Counters, nil
		}
	}
	return "", nil
}
