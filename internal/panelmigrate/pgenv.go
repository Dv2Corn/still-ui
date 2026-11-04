package panelmigrate

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgEnvNames maps libpq connection keywords to the environment variables
// pg_dump and pg_restore read them from. A DSN that uses a keyword outside
// this list is refused: dropping it could connect somewhere else, and
// passing the DSN on the command line would show its password in ps.
var pgEnvNames = map[string]string{
	"host":                     "PGHOST",
	"hostaddr":                 "PGHOSTADDR",
	"port":                     "PGPORT",
	"dbname":                   "PGDATABASE",
	"user":                     "PGUSER",
	"password":                 "PGPASSWORD",
	"passfile":                 "PGPASSFILE",
	"service":                  "PGSERVICE",
	"servicefile":              "PGSERVICEFILE",
	"sslmode":                  "PGSSLMODE",
	"sslnegotiation":           "PGSSLNEGOTIATION",
	"sslrootcert":              "PGSSLROOTCERT",
	"sslcert":                  "PGSSLCERT",
	"sslkey":                   "PGSSLKEY",
	"sslcrl":                   "PGSSLCRL",
	"sslsni":                   "PGSSLSNI",
	"sslcertmode":              "PGSSLCERTMODE",
	"ssl_min_protocol_version": "PGSSLMINPROTOCOLVERSION",
	"ssl_max_protocol_version": "PGSSLMAXPROTOCOLVERSION",
	"requirepeer":              "PGREQUIREPEER",
	"require_auth":             "PGREQUIREAUTH",
	"channel_binding":          "PGCHANNELBINDING",
	"gssencmode":               "PGGSSENCMODE",
	"gsslib":                   "PGGSSLIB",
	"krbsrvname":               "PGKRBSRVNAME",
	"connect_timeout":          "PGCONNECT_TIMEOUT",
	"application_name":         "PGAPPNAME",
	"client_encoding":          "PGCLIENTENCODING",
	"options":                  "PGOPTIONS",
	"target_session_attrs":     "PGTARGETSESSIONATTRS",
	"load_balance_hosts":       "PGLOADBALANCEHOSTS",
}

// PostgresEnv turns a PostgreSQL DSN (URL or keyword/value form, as 3x-ui's
// XUI_DB_DSN holds it) into libpq environment variables, so the migration can
// run pg_dump/pg_restore without the DSN -- and its password -- on their
// command line. It refuses a DSN it cannot map completely, and one whose
// user, password, database, host or port it reads differently from pgx, the
// driver the panel itself connects with.
func PostgresEnv(dsn string) (map[string]string, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("empty PostgreSQL DSN")
	}
	if strings.IndexByte(dsn, 0) >= 0 {
		return nil, errors.New("NUL byte in the PostgreSQL DSN")
	}
	var settings map[string]string
	var err error
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		settings, err = parseURLDSN(dsn)
	} else {
		settings, err = parseKeywordValueDSN(dsn)
	}
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for k, v := range settings {
		name, ok := pgEnvNames[k]
		if !ok {
			return nil, fmt.Errorf("the DSN uses %q, which cannot be handed to pg_dump without the command line", k)
		}
		if strings.ContainsAny(v, "\t\r\n") {
			return nil, fmt.Errorf("the DSN value of %q holds a tab or line break", k)
		}
		env[name] = v
	}
	if err := crossCheckWithPgx(dsn, settings); err != nil {
		return nil, err
	}
	return env, nil
}

func parseURLDSN(dsn string) (map[string]string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, errors.New("the PostgreSQL DSN is not a valid URL") // never echo it: it holds the password
	}
	settings := map[string]string{}
	if u.User != nil {
		if name := u.User.Username(); name != "" {
			settings["user"] = name
		}
		if pw, ok := u.User.Password(); ok {
			settings["password"] = pw
		}
	}
	if u.Host != "" {
		var hosts, ports []string
		for _, hp := range strings.Split(u.Host, ",") {
			host, port := hp, ""
			if i := strings.LastIndexByte(hp, ':'); i >= 0 && !strings.HasSuffix(hp, "]") {
				host, port = hp[:i], hp[i+1:]
			}
			host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
			hosts = append(hosts, host)
			ports = append(ports, port)
		}
		settings["host"] = strings.Join(hosts, ",")
		if strings.Join(ports, "") != "" {
			settings["port"] = strings.Join(ports, ",")
		}
	}
	if db := strings.TrimPrefix(u.Path, "/"); db != "" {
		settings["dbname"] = db
	}
	for k, vs := range u.Query() {
		if len(vs) != 1 {
			return nil, fmt.Errorf("the DSN repeats %q", k)
		}
		settings[k] = vs[0]
	}
	return settings, nil
}

// parseKeywordValueDSN reads libpq's "key=value key='quoted value'" form:
// values may be single-quoted, and a backslash escapes the next character.
func parseKeywordValueDSN(s string) (map[string]string, error) {
	const space = " \t\n\r\v\f"
	settings := map[string]string{}
	s = strings.TrimLeft(s, space)
	for s != "" {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, errors.New("the PostgreSQL DSN is not a URL and not key=value pairs")
		}
		key := strings.TrimRight(s[:eq], space)
		if key == "" || strings.ContainsAny(key, space) {
			return nil, errors.New("malformed key=value pair in the PostgreSQL DSN")
		}
		s = strings.TrimLeft(s[eq+1:], space)
		var val strings.Builder
		if strings.HasPrefix(s, "'") {
			s = s[1:]
			closed := false
			for i := 0; i < len(s); i++ {
				c := s[i]
				if c == '\\' && i+1 < len(s) {
					i++
					val.WriteByte(s[i])
					continue
				}
				if c == '\'' {
					s = s[i+1:]
					closed = true
					break
				}
				val.WriteByte(c)
			}
			if !closed {
				return nil, errors.New("unterminated quoted value in the PostgreSQL DSN")
			}
		} else {
			i := 0
			for ; i < len(s) && !strings.ContainsRune(space, rune(s[i])); i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
			}
			s = s[i:]
		}
		if key == "user" && val.Len() == 0 {
			// libpq and pgx ignore an empty user.
		} else {
			if _, dup := settings[key]; dup {
				return nil, fmt.Errorf("the DSN repeats %q", key)
			}
			settings[key] = val.String()
		}
		s = strings.TrimLeft(s, space)
	}
	return settings, nil
}

// crossCheckWithPgx parses the DSN with pgx as well and refuses on any
// disagreement about the fields that decide where and as whom we connect.
func crossCheckWithPgx(dsn string, settings map[string]string) error {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return errors.New("pgx does not accept the PostgreSQL DSN")
	}
	mismatch := func(what string) error {
		return fmt.Errorf("the PostgreSQL DSN reads differently here than in the panel's driver (%s); refusing rather than guessing", what)
	}
	if v, ok := settings["user"]; ok && v != cfg.User {
		return mismatch("user")
	}
	if v, ok := settings["password"]; ok && v != cfg.Password {
		return mismatch("password")
	}
	if v, ok := settings["dbname"]; ok && v != cfg.Database {
		return mismatch("database")
	}
	if v, ok := settings["host"]; ok && !strings.Contains(v, ",") && !strings.HasPrefix(v, "/") && v != cfg.Host {
		return mismatch("host")
	}
	if v, ok := settings["port"]; ok && !strings.Contains(v, ",") {
		if p, err := strconv.Atoi(v); err != nil || uint16(p) != cfg.Port {
			return mismatch("port")
		}
	}
	return nil
}

// WritePostgresEnv prints env as sorted "NAME<TAB>value" lines.
func WritePostgresEnv(w io.Writer, env map[string]string) error {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", k, env[k]); err != nil {
			return err
		}
	}
	return nil
}
