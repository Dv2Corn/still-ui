#!/usr/bin/env bash
# Variables assigned below are read by the sourced migrate.sh functions.
# shellcheck disable=SC2034
# Unit tests for migrate.sh's helpers (no root, no systemd, no network).
# The end-to-end test on real systems is scripts/test-migrate.sh.
set -uo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=SCRIPTDIR/../../migrate.sh disable=SC1091
source "$repo_root/migrate.sh"
set +e

test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
failures=0

expect() { # description command...
    local what="$1"
    shift
    if "$@"; then
        echo "ok   $what"
    else
        echo "FAIL $what"
        failures=$((failures + 1))
    fi
}
expect_not() {
    local what="$1"
    shift
    if "$@"; then
        echo "FAIL $what"
        failures=$((failures + 1))
    else
        echo "ok   $what"
    fi
}
equals() { [[ "$1" == "$2" ]] || { echo "     got '$1', want '$2'"; return 1; }; }

# --- versions --------------------------------------------------------------
expect "3.11.0 >= 2.4.0" version_ge 3.11.0 2.4.0
expect "v2.4.0 >= 2.4.0" version_ge v2.4.0 2.4.0
expect "2.10.0 >= 2.9.4 (numeric, not lexical)" version_ge 2.10.0 2.9.4
expect_not "2.3.15 >= 2.4.0" version_ge 2.3.15 2.4.0
expect "minor of 3.11.0" equals "$(minor_of 3.11.0)" 3.11
expect "3.11.999 covers 3.11.4" version_ge 3.11.999 3.11.4
expect_not "3.11.999 does not cover 3.12.0" version_ge 3.11.999 3.12.0

# --- env files are parsed, never sourced -----------------------------------
cat > "$test_dir/x-ui" << 'EOF'
# comment
export XUI_DB_TYPE="postgres"
XUI_DB_DSN='postgres://xui:p%40ss@127.0.0.1:5432/xui?sslmode=disable'
XUI_DB_FOLDER=/etc/x-ui
XUI_LOG_FOLDER=/var/log/x-ui
EVIL=$(touch /tmp/should-not-exist-migrate-unit)
EOF
expect "export + double quotes" equals "$(env_value "$test_dir/x-ui" XUI_DB_TYPE)" postgres
expect "single quotes" equals "$(env_value "$test_dir/x-ui" XUI_DB_DSN)" 'postgres://xui:p%40ss@127.0.0.1:5432/xui?sslmode=disable'
expect "missing key is empty" equals "$(env_value "$test_dir/x-ui" XUI_NOPE)" ""
expect "missing file is empty" equals "$(env_value "$test_dir/none" XUI_DB_TYPE)" ""
expect "command substitution is not run" test ! -e /tmp/should-not-exist-migrate-unit

# --- the PostgreSQL password stays out of argv ----------------------------
XUI_DB_DSN='postgres://xui:p%40ss@127.0.0.1:5432/xui?sslmode=disable'
pg_split_dsn
expect "URI password removed from the DSN" equals "$PG_DSN_NOPASS" 'postgres://xui@127.0.0.1:5432/xui?sslmode=disable'
expect "URI password percent-decoded" equals "$PG_PASS" 'p@ss'
XUI_DB_DSN='host=127.0.0.1 user=xui password=secret dbname=xui'
pg_split_dsn
expect "key=value password removed" equals "$PG_DSN_NOPASS" 'host=127.0.0.1 user=xui  dbname=xui'
expect "key=value password kept aside" equals "$PG_PASS" secret

# --- probes reach the listener the way a client does ----------------------
probe_url "" "" 2053 "" ""
expect "any-address listener: placeholder name resolved to loopback, http" equals "$PROBE_URL ${PROBE_RESOLVE[*]}" \
    "http://migrate-check.invalid:2053 --resolve migrate-check.invalid:2053:127.0.0.1"
probe_url "0.0.0.0" "" 2053 /c.pem /k.pem
expect "cert + key means https" equals "$PROBE_URL" "https://migrate-check.invalid:2053"
probe_url "::1" "" 2096 "" ""
expect "IPv6 listener resolved without brackets" equals "${PROBE_RESOLVE[*]}" "--resolve migrate-check.invalid:2096:::1"
probe_url "203.0.113.5" "panel.example.com" 8443 /c.pem /k.pem
expect "domain: Host/SNI of the domain, resolved to the listener" equals "$PROBE_URL ${PROBE_RESOLVE[*]}" \
    "https://panel.example.com:8443 --resolve panel.example.com:8443:203.0.113.5"

printf 'dialect\tsqlite\ninbounds\t3\nclients\t7\nsetting.webPort\t54321\nsetting.webBasePath\t/p/\nsetting.subPath\tsubs\nsampleSubId\tabc\nsampleSubId\tdef\n' > "$test_dir/report.env"
load_report "$test_dir/report.env"
expect "report counts" equals "$REP_INBOUNDS/$REP_CLIENTS/${#REP_SAMPLES[@]}" "3/7/2"
panel_target
expect "panel URL from stored port and base path" equals "$TARGET_URL" "http://migrate-check.invalid:54321/p/"
sub_target
expect "sub URL: default port, path normalised" equals "$TARGET_URL" "http://migrate-check.invalid:2096/subs/"
expect "unset setting falls back to the default" equals "$(setting subEnable true)" true

# --- 2.x draws spiderX per request; the rollback check ignores only that --
printf '%s' "$(printf 'vless://u@h:1?sid=1&spx=%%2FAAA&type=tcp#r\n' | base64 -w0 2> /dev/null || printf 'vless://u@h:1?sid=1&spx=%%2FAAA&type=tcp#r\n' | base64)" > "$test_dir/s1"
printf 'vless://u@h:1?sid=1&spx=%%2FBBB&type=tcp#r\n' > "$test_dir/s2"
printf 'vless://u@h:1?sid=2&spx=%%2FBBB&type=tcp#r\n' > "$test_dir/s3"
expect "spiderX ignored, base64 decoded" equals "$(sub_normalized "$test_dir/s1")" "$(sub_normalized "$test_dir/s2")"
expect "other parameters still count" test "$(sub_normalized "$test_dir/s2")" != "$(sub_normalized "$test_dir/s3")"

# --- still-ui gets 3x-ui's env file without 3x-ui's folders ---------------
XUI_ENV_FILE="$test_dir/x-ui"
XUI_DB_TYPE=postgres
hand_db_to_still_ui
expect "env file copied next to the original" test -f "$test_dir/still-ui"
expect "DSN carried over" grep -q '^XUI_DB_DSN=' "$test_dir/still-ui"
expect_not "XUI_DB_FOLDER/XUI_LOG_FOLDER dropped" grep -qE 'XUI_(DB|LOG)_FOLDER' "$test_dir/still-ui"
expect "copy is 0600" equals "$(stat -c %a "$test_dir/still-ui" 2> /dev/null || stat -f %Lp "$test_dir/still-ui")" 600

# --- the rollback picks the newest backup of a migration that happened ----
BACKUP_PARENT="$test_dir/root"
mkdir -p "$BACKUP_PARENT"
mk_backup() {
    mkdir -p "$BACKUP_PARENT/$1"
    touch "$BACKUP_PARENT/$1/manifest.env"
    echo "$2" > "$BACKUP_PARENT/$1/STATE"
}
mk_backup still-ui-migrate-20261101T000000Z migrated
mk_backup still-ui-migrate-20261102T000000Z rolled-back
mk_backup still-ui-migrate-20261103T000000Z aborted
expect "aborted and rolled-back attempts are skipped" equals "$(newest_backup)" "$BACKUP_PARENT/still-ui-migrate-20261101T000000Z"
mk_backup still-ui-migrate-20261104T000000Z stopped
expect "a run that stopped 3x-ui is the newest candidate" equals "$(newest_backup)" "$BACKUP_PARENT/still-ui-migrate-20261104T000000Z"
mkdir -p "$BACKUP_PARENT/still-ui-migrate-20261105T000000Z"
expect "a folder without manifest is ignored" equals "$(newest_backup)" "$BACKUP_PARENT/still-ui-migrate-20261104T000000Z"

echo
if ((failures > 0)); then
    echo "$failures check(s) failed"
    exit 1
fi
echo "all migrate.sh unit checks passed"
