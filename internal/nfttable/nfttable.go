// Package nfttable loads, removes and reads one nftables inet table the panel owns.
package nfttable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const commandTimeout = 10 * time.Second

var (
	ErrUnsupported = errors.New("nftables filtering needs Linux")
	ErrNftMissing  = errors.New("nft is not installed")
)

// Runner abstracts the host so the callers are testable off Linux.
type Runner interface {
	LookPath(name string) error
	Run(stdin string, args ...string) (string, error)
}

type osRunner struct{}

func (osRunner) LookPath(name string) error {
	_, err := exec.LookPath(name)
	return err
}

func (osRunner) Run(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nft", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Table is one inet table; it holds no lock, its owner serialises the calls.
type Table struct {
	Name string
	R    Runner
	GOOS string
}

// New is the table name on the real host.
func New(name string) *Table { return &Table{Name: name, R: osRunner{}, GOOS: runtime.GOOS} }

// Usable reports why nft cannot run here, or nil.
func (t *Table) Usable() error {
	if t.GOOS != "linux" {
		return ErrUnsupported
	}
	if t.R.LookPath("nft") != nil {
		return ErrNftMissing
	}
	return nil
}

// Load runs an nft transaction.
func (t *Table) Load(tx string) error {
	if out, err := t.R.Run(tx, "-f", "-"); err != nil {
		return fmt.Errorf("nft: %w: %s", err, out)
	}
	return nil
}

// Remove deletes the table; nothing to do on hosts that cannot have one, and
// it succeeds when the table is already gone.
func (t *Table) Remove() error {
	if t.Usable() != nil {
		return nil
	}
	return t.Load(fmt.Sprintf("add table inet %s\ndelete table inet %s\n", t.Name, t.Name))
}

// Present reports whether the table is loaded.
func (t *Table) Present() bool {
	_, err := t.R.Run("", "list", "table", "inet", t.Name)
	return err == nil
}

// State is the host side of the table: Packets are its named counters since it was last loaded.
type State struct {
	Supported    bool
	NftInstalled bool
	Active       bool
	Packets      map[string]uint64
}

// State reports whether the table is loaded and reads its counters.
func (t *Table) State() State {
	st := State{Supported: t.GOOS == "linux"}
	if !st.Supported {
		return st
	}
	st.NftInstalled = t.R.LookPath("nft") == nil
	if !st.NftInstalled {
		return st
	}
	out, err := t.R.Run("", "-j", "list", "counters", "table", "inet", t.Name)
	if err != nil {
		return st
	}
	st.Active = true
	st.Packets = t.parseCounters(out)
	return st
}

func (t *Table) parseCounters(out string) map[string]uint64 {
	var doc struct {
		Nftables []struct {
			Counter *struct {
				Name    string `json:"name"`
				Table   string `json:"table"`
				Packets uint64 `json:"packets"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	c := map[string]uint64{}
	if json.Unmarshal([]byte(out), &doc) != nil {
		return c
	}
	for _, item := range doc.Nftables {
		if item.Counter == nil || item.Counter.Table != t.Name {
			continue
		}
		c[item.Counter.Name] = item.Counter.Packets
	}
	return c
}
