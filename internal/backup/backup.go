// Package backup names, rotates, encrypts and decrypts the panel's local database backups.
// Encryption is age (filippo.io/age): the server holds only the recipients (public keys), so
// a copy that leaks from the server or from Telegram cannot be read without the operator's key.
package backup

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
)

// File name parts: still-ui-<host>-<YYYYMMDD-HHMMSS><ext>[.age].
const (
	namePrefix   = "still-ui-"
	timeLayout   = "20060102-150405"
	ExtSQLite    = ".db"
	ExtPostgres  = ".pgdump"
	ExtEncrypted = ".age"
)

var nameRe = regexp.MustCompile(`^still-ui-[A-Za-z0-9.-]+-(\d{8}-\d{6})(\.db|\.pgdump)(\.age)?$`)

var hostUnsafe = regexp.MustCompile(`[^A-Za-z0-9.-]+`)

// FileName names a backup taken at now; ext is ExtSQLite or ExtPostgres.
func FileName(host string, now time.Time, ext string, encrypted bool) string {
	host = strings.Trim(hostUnsafe.ReplaceAllString(host, "-"), ".-")
	if len(host) > 63 {
		host = host[:63]
	}
	if host == "" {
		host = "panel"
	}
	name := namePrefix + host + "-" + now.Format(timeLayout) + ext
	if encrypted {
		name += ExtEncrypted
	}
	return name
}

// Entry is one backup file in the folder.
type Entry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Time      time.Time `json:"time"`
	Encrypted bool      `json:"encrypted"`
}

// parseName reports whether name is a backup this package wrote, and when it was taken.
func parseName(name string) (time.Time, bool) {
	m := nameRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(timeLayout, m[1], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// List returns the backups in dir, newest first. Files with other names are not listed.
func List(dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, item := range items {
		if !item.Type().IsRegular() {
			continue
		}
		taken, ok := parseName(item.Name())
		if !ok {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name: item.Name(), Size: info.Size(), Time: taken,
			Encrypted: strings.HasSuffix(item.Name(), ExtEncrypted),
		})
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if c := b.Time.Compare(a.Time); c != 0 {
			return c
		}
		return strings.Compare(b.Name, a.Name)
	})
	return out, nil
}

// Prune keeps the newest keep backups in dir and deletes the rest; it touches only files
// named by FileName, so anything else an operator stores there is left alone.
func Prune(dir string, keep int) ([]string, error) {
	if keep < 1 {
		keep = 1
	}
	entries, err := List(dir)
	if err != nil || len(entries) <= keep {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, e := range entries[keep:] {
		if err := os.Remove(filepath.Join(dir, e.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, e.Name)
	}
	return removed, errors.Join(errs...)
}

// splitKeys turns a settings value into one key per entry: lines and commas separate them,
// blank entries and # comments are skipped.
func splitKeys(text string) []string {
	var out []string
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// An SSH key has spaces in it but never a comma; an age key has neither.
		for part := range strings.SplitSeq(line, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// ParseRecipients reads the public keys backups are encrypted to: age keys (age1..., age1pq1...)
// and SSH public keys (ssh-ed25519, ssh-rsa), one per line or separated by commas.
// An empty value means no encryption (nil, nil).
func ParseRecipients(text string) ([]age.Recipient, error) {
	var out []age.Recipient
	for i, key := range splitKeys(text) {
		var (
			r   []age.Recipient
			err error
		)
		switch {
		case strings.HasPrefix(key, "AGE-SECRET-KEY-"):
			// Never echo it back: it is a private key pasted into the wrong field.
			return nil, fmt.Errorf("recipient %d is an age private key; paste its public key (age1...), and keep the private key off the server", i+1)
		case strings.HasPrefix(key, "ssh-"):
			var one age.Recipient
			one, err = agessh.ParseRecipient(key)
			r = []age.Recipient{one}
		default:
			r, err = age.ParseRecipients(strings.NewReader(key))
		}
		if err != nil {
			return nil, fmt.Errorf("recipient %d is not an age or SSH public key: %v", i+1, err)
		}
		out = append(out, r...)
	}
	return out, nil
}

// ParseIdentities reads the private keys a backup is decrypted with: an age identity file
// (AGE-SECRET-KEY-...) or an unencrypted OpenSSH private key.
func ParseIdentities(data []byte) ([]age.Identity, error) {
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("-----BEGIN")) {
		id, err := agessh.ParseIdentity(trimmed)
		if err != nil {
			return nil, fmt.Errorf("SSH private key: %w", err)
		}
		return []age.Identity{id}, nil
	}
	return age.ParseIdentities(bytes.NewReader(trimmed))
}

var (
	ageMagic      = []byte("age-encryption.org/v1\n")
	ageArmorMagic = []byte("-----BEGIN AGE ENCRYPTED FILE-----")
)

// IsEncrypted reports whether a file header is an age file (binary or armored).
func IsEncrypted(header []byte) bool {
	return bytes.HasPrefix(header, ageMagic) || bytes.HasPrefix(bytes.TrimSpace(header), ageArmorMagic)
}

// EncryptFile writes src encrypted to recipients as dst (0600), through a temporary file
// in dst's folder so a crash never leaves a truncated backup under the final name.
func EncryptFile(src, dst string, recipients []age.Recipient) error {
	if len(recipients) == 0 {
		return errors.New("no recipients")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeAtomic(dst, func(w io.Writer) error {
		enc, err := age.Encrypt(w, recipients...)
		if err != nil {
			return err
		}
		if _, err := io.Copy(enc, in); err != nil {
			return err
		}
		return enc.Close()
	})
}

// EncryptBytes returns data encrypted to recipients.
func EncryptBytes(data []byte, recipients []age.Recipient) ([]byte, error) {
	var buf bytes.Buffer
	enc, err := age.Encrypt(&buf, recipients...)
	if err != nil {
		return nil, err
	}
	if _, err := enc.Write(data); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecryptFile writes src decrypted with identities as dst (0600). A wrong key or a damaged
// file fails before dst exists: age authenticates every chunk.
func DecryptFile(src, dst string, identities []age.Identity) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	br := bufio.NewReader(in)
	var r io.Reader = br
	if head, _ := br.Peek(len(ageArmorMagic) + 8); bytes.HasPrefix(bytes.TrimSpace(head), ageArmorMagic) {
		r = armor.NewReader(br)
	}
	dec, err := age.Decrypt(r, identities...)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return errors.New("this key cannot open the backup: it was encrypted to another recipient")
		}
		return err
	}
	return writeAtomic(dst, func(w io.Writer) error {
		_, err := io.Copy(w, dec)
		return err
	})
}

// writeAtomic creates dst (0600) from fill, via a temporary file that is renamed only when
// fill and the sync succeed.
func writeAtomic(dst string, fill func(io.Writer) error) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if err = fill(tmp); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
