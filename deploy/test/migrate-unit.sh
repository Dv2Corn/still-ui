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
# pg.env is what "x-ui migrate-3xui pgenv" prints (its parsing of tricky DSNs
# is tested in Go). A stub pg_dump records its argv and the libpq variables.
mkdir -p "$test_dir/bin"
cat > "$test_dir/bin/pg_dump" << 'STUB'
#!/bin/bash
printf '%s\n' "$0" "$@" > "$STUB_DIR/argv"
env | grep -E '^(PG[A-Z_]*|XUI_DB_DSN)=' | sort > "$STUB_DIR/env"
STUB
chmod 755 "$test_dir/bin/pg_dump"
# shellcheck disable=SC2016 # a literal $(x): the password must not be evaluated
printf 'PGDATABASE\txui\nPGHOST\t127.0.0.1\nPGPASSWORD\tp@ss w0rd'"'"'$(x)\nPGSSLMODE\tdisable\nPGUSER\txui\nNOTPG\tnope\n' > "$test_dir/pg.env"
export STUB_DIR="$test_dir"
PG_ENV_FILE="$test_dir/pg.env"
XUI_DB_DSN='postgres://xui:p%40ss%20w0rd@127.0.0.1/xui?sslmode=disable'
export XUI_DB_DSN
PATH="$test_dir/bin:$PATH" pg_dump_db -Fc -f "$test_dir/out.pgdump"
expect "pg_dump ran" test -s "$test_dir/argv"
expect_not "no password in pg_dump's argv" grep -q 'w0rd' "$test_dir/argv"
expect_not "no DSN in pg_dump's argv" grep -qE 'postgres(ql)?://|password=' "$test_dir/argv"
expect "argv is only the dump options" equals "$(tail -n +2 "$test_dir/argv" | tr '\n' ' ')" "--no-owner -Fc -f $test_dir/out.pgdump "
expect "password handed over in PGPASSWORD, verbatim" grep -qxF "PGPASSWORD=p@ss w0rd'\$(x)" "$test_dir/env"
expect "host, user, database, sslmode handed over" equals "$(grep -cE '^PG(HOST|USER|DATABASE|SSLMODE)=' "$test_dir/env")" 4
expect_not "XUI_DB_DSN not passed on to pg_dump" grep -q '^XUI_DB_DSN=' "$test_dir/env"
expect_not "only PG* names are exported" grep -q NOTPG "$test_dir/env"
expect "the DSN stays in this shell" equals "$XUI_DB_DSN" 'postgres://xui:p%40ss%20w0rd@127.0.0.1/xui?sslmode=disable'
sed -e 's/pg_dump/pg_restore/' "$test_dir/bin/pg_dump" > "$test_dir/bin/pg_restore"
chmod 755 "$test_dir/bin/pg_restore"
PATH="$test_dir/bin:$PATH" pg_restore_db "$test_dir/in.pgdump"
expect "pg_restore gets the database by name only" equals "$(tail -n +2 "$test_dir/argv" | tr '\n' ' ')" \
    "--no-owner --clean --if-exists --single-transaction -d xui $test_dir/in.pgdump "
expect "pg_restore gets the password in PGPASSWORD" grep -q '^PGPASSWORD=p@ss' "$test_dir/env"
PG_ENV_FILE=""
expect_not "no pg.env, no pg_dump" pg_dump_db -Fc
unset XUI_DB_DSN

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
# The copy may hold the database password: it must be 0600 from the moment it
# exists, whatever the caller's umask. grep is wrapped to report the mode of
# the file its output goes to while it writes.
XUI_ENV_FILE="$test_dir/x-ui"
XUI_DB_TYPE=postgres
grep() {
    stat -L -c %a "/proc/${BASHPID}/fd/1" > "$test_dir/mode-while-writing" 2> /dev/null
    command grep "$@"
}
(umask 022 && hand_db_to_still_ui)
unset -f grep
expect "env file copied next to the original" test -f "$test_dir/still-ui"
expect "DSN carried over" grep -q '^XUI_DB_DSN=' "$test_dir/still-ui"
expect_not "XUI_DB_FOLDER/XUI_LOG_FOLDER dropped" grep -qE 'XUI_(DB|LOG)_FOLDER' "$test_dir/still-ui"
expect "copy is 0600" equals "$(stat -c %a "$test_dir/still-ui" 2> /dev/null || stat -f %Lp "$test_dir/still-ui")" 600
if [[ -d /proc/self/fd ]]; then
    expect "copy was already 0600 while being written (umask 022)" equals "$(cat "$test_dir/mode-while-writing")" 600
fi
expect_not "no temporary file left" compgen -G "$test_dir/still-ui.*"
expect "main runs with umask 077" grep -q -A3 '^main() {' "$repo_root/migrate.sh"
expect "main sets umask 077 first" equals "$(grep -A3 '^main() {' "$repo_root/migrate.sh" | grep -c 'umask 077')" 1

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

# --- the still-ui archive must match its .sha256; install.sh comes from it --
mk_still_archive() { # DEST WITH_INSTALL_SH(0|1)
    local d
    d=$(mktemp -d "$test_dir/pkg.XXXXXX")
    mkdir -p "$d/still-ui"
    # shellcheck disable=SC2016
    printf '#!/bin/bash\n[[ "$1 $2" == "migrate-3xui version" ]] && echo 3.11.0\n' > "$d/still-ui/x-ui"
    chmod 755 "$d/still-ui/x-ui"
    [[ "$2" == 1 ]] && printf '# STILL_UI_MIGRATING\n' > "$d/still-ui/install.sh"
    tar -C "$d" -czf "$1" still-ui
}
mk_still_archive "$test_dir/good.tar.gz" 1
mk_still_archive "$test_dir/noinstall.tar.gz" 0
# shellcheck disable=SC2317,SC2329
curl() {
    local out="" prev="" url="${*: -1}"
    for arg in "$@"; do
        [[ "$prev" == -o ]] && out="$arg"
        prev="$arg"
    done
    if [[ "$url" == *.sha256 ]]; then
        case "$SIDECAR" in
            ok) printf '%s  still-ui.tar.gz\n' "$(sha256sum "$SERVE" | cut -d' ' -f1)" > "$out" ;;
            wrong) printf '%064d  still-ui.tar.gz\n' 0 > "$out" ;;
            missing) return 22 ;;
        esac
        return 0
    fi
    cp "$SERVE" "$out"
}
fetch_case() { # SIDECAR SERVE
    WORK=$(mktemp -d "$test_dir/work.XXXXXX")
    (SIDECAR="$1" SERVE="$2" STILL_UI_VERSION=v9.9.9 STILL_UI_ARCHIVE="" STILL_UI_INSTALL_SCRIPT="" fetch_still_ui && cat "${WORK}/install.sh") 2>&1
}
contains() { [[ "$1" == *"$2"* ]] || { echo "     got: $1"; return 1; }; }
expect "a matching .sha256 passes, install.sh from the archive" contains "$(fetch_case ok "$test_dir/good.tar.gz")" STILL_UI_MIGRATING
expect "a missing .sha256 stops the migration" contains "$(fetch_case missing "$test_dir/good.tar.gz")" "Could not download the checksum"
expect "a wrong .sha256 stops the migration" contains "$(fetch_case wrong "$test_dir/good.tar.gz")" "Checksum mismatch"
expect "an archive without install.sh stops the migration" contains "$(fetch_case ok "$test_dir/noinstall.tar.gz")" "has no install.sh"
# shellcheck disable=SC2016
expect_not "nothing is fetched from a branch" grep -q 'raw.githubusercontent.com/${REPO}/main' "$repo_root/migrate.sh"
unset -f curl

# --- acme.sh renewals restart still-ui after the migration -----------------
# 3x-ui's installers set these reload commands; after the migration x-ui.service is gone, so
# "systemctl restart x-ui" fails and the panel kept serving the expired certificate.
expect "systemctl restart" equals "$(hook_for_still_ui 'systemctl restart x-ui')" 'systemctl restart still-ui'
expect "systemctl, rc-service fallback" equals "$(hook_for_still_ui 'systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true')" \
    'systemctl restart still-ui 2>/dev/null || rc-service still-ui restart 2>/dev/null || true'
expect "nginx reload, then x-ui" equals "$(hook_for_still_ui 'systemctl reload nginx ; systemctl restart x-ui.service')" 'systemctl reload nginx ; systemctl restart still-ui'
expect "the x-ui menu command" equals "$(hook_for_still_ui 'x-ui restart')" 'still-ui restart'
expect "x-ui as part of another name is left alone" equals "$(hook_for_still_ui 'systemctl restart x-ui-helper; /opt/x-ui/x-ui restart')" \
    'systemctl restart x-ui-helper; /opt/x-ui/x-ui restart'

ACME_HOME="$test_dir/acme"
mkdir -p "$ACME_HOME/203.0.113.5_ecc" "$ACME_HOME/panel.example.com" "$ACME_HOME/other.example.com"
b64() { printf '%s' "$1" | base64 | tr -d '\n'; }
ip_conf="$ACME_HOME/203.0.113.5_ecc/203.0.113.5.conf"
dom_conf="$ACME_HOME/panel.example.com/panel.example.com.conf"
other_conf="$ACME_HOME/other.example.com/other.example.com.conf"
printf "Le_Domain='203.0.113.5'\nLe_ReloadCmd='__ACME_BASE64__START_%s__ACME_BASE64__END_'\nLe_NextRenewTime='100'\n" \
    "$(b64 'systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true')" > "$ip_conf"
printf "Le_Domain='panel.example.com'\nLe_ReloadCmd='systemctl restart x-ui'\nLe_PostHook='__ACME_BASE64__START_%s__ACME_BASE64__END_'\n" "$(b64 'x-ui start')" > "$dom_conf"
printf "Le_Domain='other.example.com'\nLe_ReloadCmd='__ACME_BASE64__START_%s__ACME_BASE64__END_'\n" "$(b64 'systemctl reload nginx')" > "$other_conf"
printf "Le_ReloadCmd='systemctl restart x-ui'\n" > "$ACME_HOME/panel.example.com/panel.example.com.csr.conf"
chmod 600 "$ip_conf"
cp "$other_conf" "$test_dir/other.before"
hook_of() { # FILE KEY: the command a hook line holds, decoded
    local v
    v=$(sed -n "s/^$2='\(.*\)'$/\1/p" "$1")
    if [[ "$v" == __ACME_BASE64__START_* ]]; then
        v="${v#__ACME_BASE64__START_}"
        printf '%s' "${v%__ACME_BASE64__END_}" | base64 -d
    else
        printf '%s' "$v"
    fi
}
retarget_acme_hooks "$test_dir/acme-hooks.tsv" > /dev/null
expect "IP certificate: base64 reload command restarts still-ui" equals "$(hook_of "$ip_conf" Le_ReloadCmd)" \
    'systemctl restart still-ui 2>/dev/null || rc-service still-ui restart 2>/dev/null || true'
expect "IP certificate: stays base64-encoded" grep -q "^Le_ReloadCmd='__ACME_BASE64__START_" "$ip_conf"
expect "IP certificate: other settings kept" grep -qx "Le_NextRenewTime='100'" "$ip_conf"
expect "IP certificate: mode kept" equals "$(stat -c %a "$ip_conf" 2> /dev/null || stat -f %Lp "$ip_conf")" 600
expect "domain: plain reload command" equals "$(hook_of "$dom_conf" Le_ReloadCmd)" 'systemctl restart still-ui'
expect "domain: post hook too" equals "$(hook_of "$dom_conf" Le_PostHook)" 'still-ui start'
expect "a certificate that never restarted x-ui is untouched" cmp -s "$other_conf" "$test_dir/other.before"
expect "the CSR settings file is not a domain config" grep -q 'restart x-ui' "$ACME_HOME/panel.example.com/panel.example.com.csr.conf"
expect "three changed lines recorded" equals "$(wc -l < "$test_dir/acme-hooks.tsv" | tr -d ' ')" 3
expect_not "no temporary file left" compgen -G "$ACME_HOME/*/*.conf.*"
# A renewal after the migration rewrites Le_NextRenewTime; the rollback must put back
# only the hook lines, not the whole file.
sed -i.bak "s/^Le_NextRenewTime='100'$/Le_NextRenewTime='200'/" "$ip_conf" && rm -f "$ip_conf.bak"
restore_acme_hooks "$test_dir/acme-hooks.tsv"
expect "rollback: x-ui reload command back" equals "$(hook_of "$ip_conf" Le_ReloadCmd)" \
    'systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true'
expect "rollback: domain hooks back" equals "$(hook_of "$dom_conf" Le_ReloadCmd)/$(hook_of "$dom_conf" Le_PostHook)" 'systemctl restart x-ui/x-ui start'
expect "rollback: what the renewal wrote stays" grep -qx "Le_NextRenewTime='200'" "$ip_conf"
printf 'export LE_WORKING_DIR="%s"\nexport LE_CONFIG_HOME="%s"\n' "$ACME_HOME" "$test_dir/acme-config" > "$ACME_HOME/acme.sh.env"
expect "LE_CONFIG_HOME of acme.sh.env is where the settings are" equals "$(acme_config_home)" "$test_dir/acme-config"
rm -f "$ACME_HOME/acme.sh.env"

# --- 3x-ui stays up while packages install; the stopped window is bounded --
# A stub install.sh records how it was called; $STUB_DIR/hang makes it hang like a dead mirror.
WORK="$test_dir/inst-work" BK="$test_dir/inst-bk"
mkdir -p "$WORK" "$BK"
cat > "$WORK/install.sh" << 'STUB'
# install-deps) is what migrate.sh looks for.
echo "args=$* migrating=${STILL_UI_MIGRATING:-} deps=${STILL_UI_DEPS_INSTALLED:-}" >> "$STUB_DIR/install-calls"
[[ -e "$STUB_DIR/hang" ]] && exec sleep 30
exit 0
STUB
STILL_ARCHIVE=/dev/null
DEPS_INSTALLED=0
install_deps > /dev/null
run_install > /dev/null
expect "packages are installed on their own step, then the installer skips them" equals "$(cat "$test_dir/install-calls")" \
    "$(printf 'args=install-deps migrating= deps=\nargs= migrating=1 deps=1')"
INSTALL_TIMEOUT=1
start=$SECONDS
touch "$test_dir/hang"
quiet_run_install() { run_install > /dev/null; }
expect_not "an installer that hangs while 3x-ui is stopped fails (and the migration rolls back)" quiet_run_install
expect "it is given up on after INSTALL_TIMEOUT" test $((SECONDS - start)) -lt 20
: > "$test_dir/install-calls"
DEPS_INSTALLED=0
start=$SECONDS
install_deps > /dev/null
expect "a hung package mirror before the stop does not stop the migration" equals "$DEPS_INSTALLED" 1
expect "and is given up on as well" test $((SECONDS - start)) -lt 20
rm -f "$test_dir/hang"
INSTALL_TIMEOUT=600

echo
if ((failures > 0)); then
    echo "$failures check(s) failed"
    exit 1
fi
echo "all migrate.sh unit checks passed"
