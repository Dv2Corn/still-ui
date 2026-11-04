#!/usr/bin/env bash
# End-to-end test of migrate.sh on real systems: throwaway Incus system
# containers (real systemd) get a real upstream 3x-ui release from its own
# install script, inbounds and clients through the 3x-ui API, then
#
#   migrate            -> still-ui runs: same ports, login, inbounds, clients,
#                         every client's subscription byte-identical
#   migrate-rollback   -> 3x-ui runs again with the same data, still-ui gone
#   (fail case)        -> a failing post-start check rolls back on its own
#
#   scripts/test-migrate.sh <still-ui archive> <scenario>...
#
# scenario = <image>:<3x-ui tag>[:fail], e.g.
#   scripts/test-migrate.sh out/still-ui-linux-amd64.tar.gz \
#       debian/12:v3.11.0 ubuntu/24.04:v3.7.0 debian/12:v2.6.7 ubuntu/24.04:v3.11.0:fail
#   scripts/test-migrate.sh out/still-ui-linux-amd64.tar.gz debian/12:v2.3.15:refuse
#
# ":refuse" expects migrate.sh to refuse the release (too old) and change nothing.
# ":pg" installs 3x-ui with a local PostgreSQL database (3.x installers only).
# FLOOR_PROBE=1 lets migrate.sh try releases below its MIN_VERSION.
# Needs incus with the images: remote and internet access in the containers.
# Containers are named su-mig-<image>-<tag>[-<mode>] and deleted afterwards
# (KEEP=1 keeps them), so several runs with different scenarios can go in parallel.
# migrate.sh, install.sh and the seed helper come from this checkout.
set -uo pipefail

SRC=$(cd "$(dirname "$0")/.." && pwd)
ARCHIVE="${1:?usage: $0 <still-ui archive> <image>:<tag>[:fail|:refuse]...}"
shift
[[ -s "$ARCHIVE" ]] || { echo "no archive at $ARCHIVE" >&2; exit 2; }
[[ $# -gt 0 ]] || { echo "no scenario given" >&2; exit 2; }

USER_NAME='admin'
USER_PASS=adminpass1
PANEL_PORT=2053
BASE_PATH=panelpath
FAILED=0
RESULTS=()

log() { echo "[$(date +%H:%M:%S)] $*"; }

# Executables of the running Xray processes, for the snippets below (they
# run in the container, hence the single quotes).
# shellcheck disable=SC2016
XRAY_EXE='xray_exe() { local p; for p in $(pgrep -f xray-linux); do readlink -f /proc/$p/exe; done; }'
# dbq DB SQLITE-SQL PG-SQL: query a panel database, DB = a SQLite file or
# pg:<env file holding XUI_DB_DSN>.
# shellcheck disable=SC2016
DBQ='dbq() { if [[ $1 == pg:* ]]; then psql "$(sed -n "s/^XUI_DB_DSN=//p" "${1#pg:}" | tr -d "\"'"'"'")" -Atc "$3"; else sqlite3 "$1" "$2"; fi; }'

# in_ct CT SNIPPET: run a bash snippet in the container.
in_ct() {
    local ct="$1"
    shift
    incus exec "$ct" -- bash -c "${XRAY_EXE}; ${DBQ}; $*"
}

result() { # condition-exit-code ok-text fail-text
    if [[ "$1" == 0 ]]; then
        log "  ok   $2"
    else
        log "  FAIL $3"
        CASE_FAILED=1
    fi
}

check() { # ct description snippet
    if in_ct "$1" "$3" > /dev/null 2>&1; then
        log "  ok   $2"
    else
        log "  FAIL $2"
        CASE_FAILED=1
    fi
}

# The client-visible state of whatever panel runs: listening ports (not the
# loopback-only ones, which clients cannot reach and which carry random API ports), the
# number of inbounds and the subscription of every client, fetched by name
# like a client does (sub.test resolved to this host).
snapshot() { # ct dir db
    local ct="$1" dir="$2" db="$3"
    in_ct "$ct" "set -e; rm -rf $dir; mkdir -p $dir
        ss -lntuH | awk '\$5 !~ /^(127\\.|\\[::1\\]|\\[::ffff:127\\.)/ {n = split(\$5, a, \":\"); print \$1, a[n]}' | grep -Ev ' (22|53|5355)\$' | sort -u > $dir/ports
        dbq $db 'select count(*) from inbounds' 'select count(*) from inbounds' > $dir/inbounds
        dbq $db \"select json_extract(c.value,'\$.subId') from inbounds i, json_each(json_extract(i.settings,'\$.clients')) c where i.enable=1\" \\
            \"select c->>'subId' from inbounds i, json_array_elements((i.settings::json)->'clients') c where i.enable\" | sort -u > $dir/subids
        q=\"select value from settings where key='subPath'\"; sub_path=\$(dbq $db \"\$q\" \"\$q\"); sub_path=\${sub_path:-/sub/}
        while read -r id; do
            [[ -n \$id ]] || continue
            curl -s --resolve sub.test:2096:127.0.0.1 -o $dir/sub-\$id -w '%{http_code}\n' http://sub.test:2096\$sub_path\$id > $dir/code-\$id
        done < $dir/subids"
}

# same_view CT A B: the same ports, inbounds and HTTP answers; every
# subscription compared with "x-ui migrate-3xui subcompare" (identical, or the
# same settings in a newer link format). Prints the per-subscription levels.
same_view() {
    in_ct "$1" "set -e; cd /root/t
        diff $2/ports $3/ports; diff $2/inbounds $3/inbounds; diff $2/subids $3/subids
        for f in $2/code-*; do grep -qx 200 \$f || continue; diff \$f $3/\${f##*/}; done
        for f in $2/sub-*; do
            # 3x-ui itself failed this one (2.2.x answers 500 for some): nothing to compare.
            grep -qx 200 $2/code-\${f##*/sub-} || { echo \"3x-ui-answered-\$(cat $2/code-\${f##*/sub-})\"; continue; }
            rc=0; note=\$(./check-bin migrate-3xui subcompare -a \$f -b $3/\${f##*/}) || rc=\$?
            case \$rc in 0) echo identical;; 3) echo remarks;; 4) echo \"same-settings \$note\";; *) echo \"DIFFERENT \${f##*/} \$note\"; exit 1;; esac
        done | sort | uniq -c"
}

# same_view_norm CT A B: like same_view, but byte equality once the spiderX
# path that 3x-ui 2.x draws per request is blanked out (3x-ui vs 3x-ui).
same_view_norm() {
    in_ct "$1" "set -e; cd /root/t
        diff $2/ports $3/ports; diff $2/inbounds $3/inbounds; diff $2/subids $3/subids
        norm() { { base64 -d \$1 2> /dev/null || cat \$1; } | sed -E 's/([?&])spx=[^&#]*/\\1spx=/g'; }
        for f in $2/code-*; do diff \$f $3/\${f##*/}; done
        for f in $2/sub-*; do diff <(norm \$f) <(norm $3/\${f##*/}) > /dev/null || { echo \"DIFFERENT \${f##*/}\"; exit 1; }; done"
}

install_3xui() { # ct tag [pg]
    local ct="$1" tag="$2" db_type=sqlite
    [[ "${3:-}" == "pg" ]] && db_type=postgres
    in_ct "$ct" "curl -fsSL -o /root/xui-install.sh https://raw.githubusercontent.com/MHSanaei/3x-ui/${tag}/install.sh"
    local pg_env=""
    if [[ "$db_type" == "postgres" ]]; then
        # A PostgreSQL server of the host's own, handed to the 3x-ui installer
        # as an external DSN (its "install PostgreSQL for me" path trips over
        # XUI_DB_TYPE in its own environment before the DSN exists).
        in_ct "$ct" "set -e; export DEBIAN_FRONTEND=noninteractive; apt-get install -y -qq postgresql > /dev/null
            systemctl enable --now postgresql > /dev/null 2>&1 || true
            for _ in \$(seq 1 30); do runuser -u postgres -- psql -Atc 'select 1' > /dev/null 2>&1 && break; sleep 1; done
            runuser -u postgres -- psql -qc \"create user xui with password 'xuipass'\"
            runuser -u postgres -- psql -qc 'create database xui owner xui'" > "$LOG_DIR/pg.log" 2>&1 || return 1
        pg_env="XUI_DB_DSN=postgres://xui:xuipass@127.0.0.1:5432/xui?sslmode=disable"
    fi
    if in_ct "$ct" "grep -q XUI_NONINTERACTIVE /root/xui-install.sh"; then
        in_ct "$ct" "XUI_NONINTERACTIVE=1 XUI_DB_TYPE=$db_type $pg_env XUI_USERNAME=$USER_NAME XUI_PASSWORD=$USER_PASS XUI_PANEL_PORT=$PANEL_PORT \
            XUI_WEB_BASE_PATH=$BASE_PATH XUI_SSL_MODE=none XUI_ENABLE_FAIL2BAN=false bash /root/xui-install.sh $tag" > "$LOG_DIR/xui-install.log" 2>&1
    else
        # 2.x asks whether to pick the panel port, then which one.
        in_ct "$ct" "printf 'y\n$PANEL_PORT\n' | bash /root/xui-install.sh $tag" > "$LOG_DIR/xui-install.log" 2>&1
    fi
    # Known login and base path on every release; 2.x keeps its subscription
    # server off by default.
    in_ct "$ct" "set -e
        systemctl stop x-ui
        /usr/local/x-ui/x-ui setting -username $USER_NAME -password $USER_PASS -port $PANEL_PORT > /dev/null
        # Old releases have no -webBasePath flag: write the setting directly.
        q=\"delete from settings where key in ('subEnable', 'webBasePath');
           insert into settings(key, value) values('subEnable', 'true'), ('webBasePath', '/$BASE_PATH/')\"
        dbq $PRE_DB \"\$q\" \"\$q\" > /dev/null
        systemctl start x-ui
        for i in \$(seq 1 30); do curl -s -o /dev/null http://127.0.0.1:$PANEL_PORT/$BASE_PATH/ && break; sleep 1; done"
}

run_case() { # ct image tag mode
    local ct="$1" image="$2" tag="$3" mode="${4:-}"
    CASE_FAILED=0
    LOG_DIR="${TMPDIR:-/tmp}/test-migrate-$ct"
    rm -rf "$LOG_DIR" && mkdir -p "$LOG_DIR"
    log "== $ct: $image, 3x-ui $tag ${mode:+($mode)} (logs: $LOG_DIR)"
    incus delete -f "$ct" > /dev/null 2>&1
    incus launch "images:$image" "$ct" -c limits.cpu=2 -c limits.memory=2GiB > /dev/null 2>&1 || { log "  FAIL launch"; return 1; }
    for _ in $(seq 1 60); do
        in_ct "$ct" "getent hosts deb.debian.org archive.ubuntu.com > /dev/null" && break
        sleep 2
    done
    in_ct "$ct" "export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq curl ca-certificates sqlite3 python3 openssl iproute2" > "$LOG_DIR/apt.log" 2>&1 ||
        { log "  FAIL apt"; return 1; }

    PRE_DB=/etc/x-ui/x-ui.db
    STILL_DB=/etc/still-ui/x-ui.db
    if [[ "$mode" == "pg" ]]; then
        PRE_DB=pg:/etc/default/x-ui
        STILL_DB=pg:/etc/default/still-ui
    fi
    install_3xui "$ct" "$tag" "$mode" || { log "  FAIL 3x-ui install"; return 1; }
    in_ct "$ct" "mkdir -p /root/t"
    incus file push -q "$ARCHIVE" "$ct/root/t/still-ui-linux-amd64.tar.gz"
    incus file push -q "$SRC/migrate.sh" "$SRC/install.sh" "$SRC/scripts/test-migrate-seed.py" "$ct/root/t/"
    # FLOOR_PROBE=1 lifts the minimum 3x-ui version, to find out which old
    # releases migrate cleanly (that is how MIN_VERSION was chosen).
    [[ "${FLOOR_PROBE:-}" == "1" ]] && in_ct "$ct" "sed -i 's/^MIN_VERSION=.*/MIN_VERSION=\"0.0.0\"/' /root/t/migrate.sh"
    # The still-ui binary of the archive compares subscriptions for the test.
    in_ct "$ct" "cd /root/t && tar -xzf still-ui-linux-amd64.tar.gz still-ui/x-ui && mv still-ui/x-ui check-bin && rmdir still-ui"
    # 3x-ui before 2.4 has neither Hysteria nor AmneziaWG, yet its API stores
    # them and Xray then fails to start: seed only what it can run.
    local only=""
    [[ "$(printf '%s\n' "${tag#v}" 2.4.0 | sort -V | head -n1)" == "2.4.0" ]] || only="--only vless-reality,vmess-ws,trojan-tls,shadowsocks-2022"
    in_ct "$ct" "python3 /root/t/test-migrate-seed.py http://127.0.0.1:$PANEL_PORT/$BASE_PATH $USER_NAME $USER_PASS $only" | tee "$LOG_DIR/seed.json" | sed 's/^/  seeded: /'
    check "$ct" "3x-ui $tag runs with Xray" "systemctl is-active x-ui && xray_exe | grep -q '^/usr/local/x-ui/'"
    snapshot "$ct" /root/t/pre "$PRE_DB"
    in_ct "$ct" "(cd / && find usr/local/x-ui -type f ! -name config.json ! -name '*.log' -print0 | xargs -0 sha256sum) > /root/t/xui-files.sha256"

    local env="STILL_UI_ARCHIVE=/root/t/still-ui-linux-amd64.tar.gz STILL_UI_INSTALL_SCRIPT=/root/t/install.sh STILL_UI_MIGRATE_SCRIPT=/root/t/migrate.sh STILL_UI_MIGRATE_YES=1"
    local rc
    if [[ "$mode" == "refuse" ]]; then
        in_ct "$ct" "$env bash /root/t/install.sh migrate" > "$LOG_DIR/migrate.log" 2>&1
        rc=$?
        sed 's/^/  | /' "$LOG_DIR/migrate.log" | tail -n 3
        result "$((rc == 0))" "migrate refused (exit $rc)" "migrate did not refuse"
        check "$ct" "3x-ui untouched and running" "systemctl is-active x-ui && [[ ! -e /usr/local/still-ui ]] && ! ls -d /root/still-ui-migrate-* && (cd / && sha256sum --quiet -c /root/t/xui-files.sha256)"
        snapshot "$ct" /root/t/post "$PRE_DB"
        if same_view_norm "$ct" pre post > "$LOG_DIR/view-post.txt" 2>&1; then
            log "  ok   clients see the same ports and subscriptions"
        else
            log "  FAIL clients see something else: $(tail -n 3 "$LOG_DIR/view-post.txt" | tr '\n' ' ')"
            CASE_FAILED=1
        fi
        return "$CASE_FAILED"
    fi

    if [[ "$mode" == "fail" ]]; then
        in_ct "$ct" "$env STILL_UI_MIGRATE_TEST_FAIL=health bash /root/t/install.sh migrate" > "$LOG_DIR/migrate.log" 2>&1
        rc=$?
        sed 's/^/  | /' "$LOG_DIR/migrate.log" | grep -E 'FAIL|fail|restor|Rollback|running again|byte' | head -n 12
        result "$((rc == 0))" "migrate failed as injected (exit $rc)" "migrate succeeded despite the injected failure"
        check "$ct" "automatic rollback: 3x-ui active and enabled, still-ui gone" \
            "systemctl is-active x-ui && systemctl is-enabled x-ui && ! systemctl cat still-ui && [[ ! -e /usr/local/still-ui && ! -e /usr/bin/still-ui && ! -e /etc/still-ui ]]"
        check "$ct" "backup state is rolled-back" "grep -qx rolled-back /root/still-ui-migrate-*/STATE"
    else
        in_ct "$ct" "$env bash /root/t/install.sh migrate" > "$LOG_DIR/migrate.log" 2>&1
        rc=$?
        sed 's/^/  | /' "$LOG_DIR/migrate.log" | grep -E 'ok  |FAIL|Found|has [0-9]' | head -n 20
        result "$rc" "migrate exit 0" "migrate exit $rc"
        check "$ct" "still-ui active, its own Xray running, 3x-ui unit gone" \
            "systemctl is-active still-ui && xray_exe | grep -q '^/usr/local/still-ui/' && ! xray_exe | grep -q '^/usr/local/x-ui/' && [[ -z \$(systemctl show -p FragmentPath --value x-ui) ]]"
        check "$ct" "backup folder is 0700 with manifest and consistent copies" \
            "B=\$(ls -d /root/still-ui-migrate-*); [[ \$(stat -c %a \$B) == 700 ]] && (cd \$B && sha256sum --quiet -c manifest.sha256) && grep -qx migrated \$B/STATE"
        check "$ct" "same login works on still-ui" "python3 /root/t/test-migrate-seed.py http://127.0.0.1:$PANEL_PORT/$BASE_PATH $USER_NAME $USER_PASS --check-login"
        check "$ct" "'x-ui' opens still-ui's menu" "grep -q 'exec /usr/bin/still-ui' /usr/bin/x-ui"
        snapshot "$ct" /root/t/migrated "$STILL_DB"
        if same_view "$ct" pre migrated > "$LOG_DIR/view-migrated.txt" 2>&1; then
            log "  ok   same ports, inbounds, answers; subscriptions: $(tr -s ' \n' ' ' < "$LOG_DIR/view-migrated.txt")"
        else
            log "  FAIL clients see something else after the migration: $(tail -n 3 "$LOG_DIR/view-migrated.txt" | tr '\n' ' ')"
            CASE_FAILED=1
        fi
        check "$ct" "3x-ui files untouched" "(cd / && sha256sum --quiet -c /root/t/xui-files.sha256)"

        # A client added in still-ui must not leak back into 3x-ui: rollback
        # restores the database 3x-ui stopped with, and keeps still-ui's.
        in_ct "$ct" "STILL_UI_MIGRATE_YES=1 still-ui migrate-rollback" > "$LOG_DIR/rollback.log" 2>&1
        rc=$?
        sed 's/^/  | /' "$LOG_DIR/rollback.log" | grep -E 'ok  |running again|differs|FAIL|finished' | head -n 10
        result "$rc" "migrate-rollback exit 0" "migrate-rollback exit $rc"
        check "$ct" "3x-ui active and enabled again, still-ui gone" \
            "systemctl is-active x-ui && systemctl is-enabled x-ui && ! systemctl cat still-ui && [[ ! -e /usr/local/still-ui && ! -e /usr/bin/still-ui && ! -e /etc/still-ui ]] && ! grep -q 'exec /usr/bin/still-ui' /usr/bin/x-ui"
        if [[ "$mode" == "pg" ]]; then
            check "$ct" "still-ui's data dumped under after-rollback/" "ls /root/still-ui-migrate-*/after-rollback/*/still-ui.pgdump"
        else
            check "$ct" "still-ui's database kept under after-rollback/" "ls /root/still-ui-migrate-*/after-rollback/*/etc-still-ui/x-ui.db"
        fi
    fi
    check "$ct" "same login works on 3x-ui" "python3 /root/t/test-migrate-seed.py http://127.0.0.1:$PANEL_PORT/$BASE_PATH $USER_NAME $USER_PASS --check-login"
    check "$ct" "Xray runs under 3x-ui" "xray_exe | grep -q '^/usr/local/x-ui/' && ! xray_exe | grep -q '^/usr/local/still-ui/'"
    snapshot "$ct" /root/t/back "$PRE_DB"
    if same_view_norm "$ct" pre back > "$LOG_DIR/view-back.txt" 2>&1; then
        log "  ok   same ports, inbounds, answers and subscriptions as before the migration"
    else
        log "  FAIL clients see something else after the rollback: $(tail -n 3 "$LOG_DIR/view-back.txt" | tr '\n' ' ')"
        CASE_FAILED=1
    fi
    check "$ct" "3x-ui files as before" "(cd / && sha256sum --quiet -c /root/t/xui-files.sha256)"
    return "$CASE_FAILED"
}

for scenario in "$@"; do
    IFS=: read -r image tag mode <<< "$scenario"
    ct="su-mig-$(echo "${image}-${tag}${mode:+-$mode}" | tr -c 'a-z0-9\n' '-' | tr -s '-')"
    if run_case "$ct" "$image" "$tag" "$mode"; then
        RESULTS+=("PASS $scenario")
    else
        RESULTS+=("FAIL $scenario")
        FAILED=1
    fi
    [[ "${KEEP:-}" == "1" ]] || incus delete -f "$ct" > /dev/null 2>&1
done
echo
printf '%s\n' "${RESULTS[@]}"
exit "$FAILED"
