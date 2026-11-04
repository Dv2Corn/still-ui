package panelmigrate

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestPostgresEnv(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want map[string]string
	}{
		{"URL with percent-encoded password and sslmode",
			"postgres://xui:p%40ss%2Fw%3Ard%20x@127.0.0.1:5432/xui?sslmode=disable",
			map[string]string{"PGUSER": "xui", "PGPASSWORD": "p@ss/w:rd x", "PGHOST": "127.0.0.1", "PGPORT": "5432", "PGDATABASE": "xui", "PGSSLMODE": "disable"}},
		{"postgresql:// scheme, IPv6 host, password in the query",
			"postgresql://xui@[::1]:6432/db?password=q%26uery&connect_timeout=5&application_name=x",
			map[string]string{"PGUSER": "xui", "PGPASSWORD": "q&uery", "PGHOST": "::1", "PGPORT": "6432", "PGDATABASE": "db", "PGCONNECT_TIMEOUT": "5", "PGAPPNAME": "x"}},
		{"keyword/value with quoted password and escapes",
			`host=db.local port=5432 user=xui password='a b\'c\\d' dbname=xui sslmode=require`,
			map[string]string{"PGHOST": "db.local", "PGPORT": "5432", "PGUSER": "xui", "PGPASSWORD": `a b'c\d`, "PGDATABASE": "xui", "PGSSLMODE": "require"}},
		{"keyword/value with spaces around '=' and an escaped space",
			`host = /var/run/postgresql  user=xui password=sp\ ace dbname=xui`,
			map[string]string{"PGHOST": "/var/run/postgresql", "PGUSER": "xui", "PGPASSWORD": "sp ace", "PGDATABASE": "xui"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PostgresEnv(c.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("env = %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestPostgresEnvRefusesWhatItCannotMap(t *testing.T) {
	for name, dsn := range map[string]string{
		"empty":                  "",
		"unknown keyword":        "host=h user=u password=secret-pw dbname=d pool_max_conns=4",
		"unknown URL parameter":  "postgres://u:secret-pw@h/d?pool_max_conns=4",
		"unterminated quote":     "host=h password='secret-pw",
		"not a DSN":              "secret-pw",
		"line break in password": "host=h user=u password='secret\npw' dbname=d",
		"repeated keyword":       "host=h password=secret-pw password=other",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PostgresEnv(dsn)
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("the error leaks the password: %v", err)
			}
		})
	}
}

func TestRunCLIPgenv(t *testing.T) {
	t.Setenv("XUI_DB_DSN", "postgres://xui:pw@127.0.0.1:5432/xui?sslmode=disable")
	var out bytes.Buffer
	if code := RunCLI([]string{"pgenv"}, &out, io.Discard); code != 0 {
		t.Fatalf("pgenv = %d", code)
	}
	want := "PGDATABASE\txui\nPGHOST\t127.0.0.1\nPGPASSWORD\tpw\nPGPORT\t5432\nPGSSLMODE\tdisable\nPGUSER\txui\n"
	if out.String() != want {
		t.Fatalf("pgenv printed %q, want %q", out.String(), want)
	}
	t.Setenv("XUI_DB_DSN", "host=h foo=bar")
	if code := RunCLI([]string{"pgenv"}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("pgenv of an unmappable DSN = %d, want 1", code)
	}
}
