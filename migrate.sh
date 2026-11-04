#!/bin/bash
# still-ui: move an existing 3x-ui installation to still-ui, and back.
#
#   bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) migrate
#   bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) migrate-rollback [DIR]
#   still-ui migrate-rollback [DIR]          (after a migration)
#
# migrate:
#   1. finds 3x-ui (/usr/local/x-ui, x-ui.service, its SQLite or PostgreSQL
#      database, version) and refuses forks, releases it cannot migrate and
#      hosts that already run still-ui;
#   2. records what has to survive while 3x-ui still runs: inbound and client
#      counts, the panel login, panel/subscription listener settings, the HTTP
#      answer of the panel and the subscription of up to three clients;
#   3. backs everything up to /root/still-ui-migrate-<UTC time>/ (0700) before
#      changing anything: a consistent copy of the database (VACUUM INTO, or
#      pg_dump), /usr/local/x-ui, the unit and its drop-ins, /usr/bin/x-ui, the
#      env file, the fail2ban IP-limit files and a manifest with sha256 sums;
#   4. stops 3x-ui, gives still-ui a copy of the database and installs still-ui
#      (its own paths: /usr/local/still-ui, /etc/still-ui, still-ui.service; the
#      ports, certificates, base path and subscription URLs come from the
#      database and stay the same), starts it and checks it: panel answers,
#      Xray runs, the same inbounds/clients/login/settings, and the sampled
#      subscriptions are byte-identical;
#   5. on any failure after 3x-ui was stopped it rolls back on its own.
#   3x-ui's own files are not modified. On success its unit is removed (it is
#   in the backup) so it cannot start next to still-ui, and /usr/bin/x-ui runs
#   still-ui's menu.
#
# migrate-rollback [DIR] restores 3x-ui from the newest (or the given) backup
# folder: binaries, unit, env file, database; moves still-ui's database to
# DIR/after-rollback/ (nothing is deleted), removes still-ui and starts 3x-ui.
#
# Environment:
#   STILL_UI_ARCHIVE=FILE           install this still-ui release archive instead
#                                   of downloading the latest release (offline,
#                                   own builds, tests)
#   STILL_UI_VERSION=TAG            release to download (default: latest)
#   STILL_UI_INSTALL_SCRIPT=FILE    use this install.sh instead of downloading it
#   STILL_UI_MIGRATE_YES=1          do not ask for confirmation
#   STILL_UI_MIGRATE_ALLOW_NEWER=1  also migrate a 3x-ui newer than still-ui's base
#   STILL_UI_MIGRATE_ANY_FORK=1     also migrate an install that is not MHSanaei/3x-ui
#   STILL_UI_MIGRATE_TIMEOUT=SEC    how long still-ui may take to come up (default 120)
#   STILL_UI_MIGRATE_TEST_FAIL=health  test hook: fail the post-start check so the
#                                   automatic rollback runs

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

REPO="Dv2Corn/still-ui"
XUI_DIR="/usr/local/x-ui"
STILL_DIR="/usr/local/still-ui"
STILL_DB_DIR="/etc/still-ui"
BACKUP_PARENT="/root"
# Oldest 3x-ui release whose database still-ui is known to take over cleanly
# (tested with scripts/test-migrate.sh). Older ones are refused.
MIN_VERSION="2.0.0"
FAIL2BAN_FILES=(/etc/fail2ban/jail.d/3x-ipl.conf /etc/fail2ban/jail.d/3x-ipl-backend.conf
    /etc/fail2ban/filter.d/3x-ipl.conf /etc/fail2ban/action.d/3x-ipl.conf)
TIMEOUT="${STILL_UI_MIGRATE_TIMEOUT:-120}"
# Host name the checks ask for when the panel has no domain set (RFC 2606).
PROBE_HOST="migrate-check.invalid"

info() { echo -e "${green}$*${plain}"; }
warn() { echo -e "${yellow}$*${plain}"; }
err() { echo -e "${red}$*${plain}" >&2; }
die() {
    err "$*"
    exit 1
}

# Mutable state of a migration run.
BK=""
WORK=""
STILL_BIN=""
STOPPED_XUI=0
MIGRATION_DONE=0

arch() {
    case "$(uname -m)" in
        x86_64 | x64 | amd64) echo amd64 ;;
        i*86 | x86) echo 386 ;;
        armv8* | arm64 | aarch64) echo arm64 ;;
        armv7* | arm) echo armv7 ;;
        armv6*) echo armv6 ;;
        armv5*) echo armv5 ;;
        s390x) echo s390x ;;
        *) echo unknown ;;
    esac
}

# version_ge A B: A >= B for dotted versions (a leading v is ignored).
version_ge() {
    local a="${1#v}" b="${2#v}"
    [[ "$(printf '%s\n%s\n' "$a" "$b" | sort -V | head -n1)" == "$b" ]]
}

# major.minor of a version.
minor_of() {
    local v="${1#v}"
    echo "${v%.*}"
}

# env_value FILE KEY: value of KEY in an EnvironmentFile, without sourcing it.
env_value() {
    [[ -r "$1" ]] || return 0
    sed -n "s/^[[:space:]]*\(export[[:space:]]\+\)\?$2=//p" "$1" | tail -n 1 |
        sed -e 's/^"\(.*\)"[[:space:]]*$/\1/' -e "s/^'\(.*\)'[[:space:]]*$/\1/"
}

confirm() {
    local answer
    [[ "${STILL_UI_MIGRATE_YES:-}" == "1" ]] && return 0
    if [[ ! -t 0 ]]; then
        die "$1 Re-run with STILL_UI_MIGRATE_YES=1 to confirm without a terminal."
    fi
    read -rp "$1 [y/N]: " answer
    [[ "$answer" == [yY] || "$answer" == [yY][eE][sS] ]]
}

# --- detection -------------------------------------------------------------

# Fills XUI_* variables describing the 3x-ui install, or dies with the reason
# a migration is refused.
detect_3xui() {
    [[ $EUID -eq 0 ]] || die "Run this as root."
    if ! command -v systemctl > /dev/null 2>&1 || [[ ! -d /run/systemd/system ]]; then
        die "Only systemd hosts are supported (OpenRC/Alpine and Docker installs are not)."
    fi
    if [[ -e "${STILL_DIR}/x-ui" || -e "${STILL_DB_DIR}/x-ui.db" || -e /etc/systemd/system/still-ui.service || -e /usr/bin/still-ui ]]; then
        die "still-ui is already installed on this host (${STILL_DIR}, ${STILL_DB_DIR} or still-ui.service exists). Nothing to migrate."
    fi
    [[ -x "${XUI_DIR}/x-ui" ]] || die "No 3x-ui found: ${XUI_DIR}/x-ui does not exist. Docker installs are not supported."

    XUI_UNIT=$(systemctl show -p FragmentPath --value x-ui 2> /dev/null)
    [[ -n "$XUI_UNIT" && -f "$XUI_UNIT" ]] || die "x-ui.service is not installed; cannot migrate an install that systemd does not run."
    XUI_DROPINS=$(systemctl show -p DropInPaths --value x-ui 2> /dev/null)

    XUI_VERSION=$(timeout 20 "${XUI_DIR}/x-ui" -v 2> /dev/null | head -n1 | tr -d '[:space:]')
    XUI_VERSION="${XUI_VERSION#v}"
    [[ "$XUI_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "Could not read the 3x-ui version ('${XUI_DIR}/x-ui -v' printed '${XUI_VERSION}')."

    local script
    for script in /usr/bin/x-ui "${XUI_DIR}/x-ui.sh"; do
        if [[ -f "$script" ]] && grep -qi 'MHSanaei/3x-ui' "$script"; then
            XUI_FORK=3x-ui
            break
        fi
    done
    if [[ "${XUI_FORK:-}" != "3x-ui" ]]; then
        [[ "${STILL_UI_MIGRATE_ANY_FORK:-}" == "1" ]] ||
            die "This does not look like MHSanaei/3x-ui (its x-ui.sh does not mention it). Other x-ui forks are not supported; STILL_UI_MIGRATE_ANY_FORK=1 tries anyway."
        XUI_FORK=unknown
    fi
    version_ge "$XUI_VERSION" "$MIN_VERSION" ||
        die "3x-ui ${XUI_VERSION} is too old to migrate directly (oldest supported: ${MIN_VERSION}). Update 3x-ui first ('x-ui update'), then run the migration."

    XUI_ENV_FILE=""
    local f
    for f in /etc/default/x-ui /etc/conf.d/x-ui /etc/sysconfig/x-ui; do
        if [[ -f "$f" ]]; then
            XUI_ENV_FILE="$f"
            break
        fi
    done
    XUI_DB_TYPE=$(env_value "$XUI_ENV_FILE" XUI_DB_TYPE)
    case "${XUI_DB_TYPE:-sqlite}" in
        sqlite | "") XUI_DB_TYPE=sqlite ;;
        postgres) ;;
        *) die "Unknown XUI_DB_TYPE=${XUI_DB_TYPE} in ${XUI_ENV_FILE}." ;;
    esac
    XUI_DB=""
    if [[ "$XUI_DB_TYPE" == "sqlite" ]]; then
        local folder
        folder=$(env_value "$XUI_ENV_FILE" XUI_DB_FOLDER)
        XUI_DB="${folder:-/etc/x-ui}/x-ui.db"
        [[ -f "$XUI_DB" ]] || die "The 3x-ui database ${XUI_DB} does not exist."
        XUI_DB_ARGS=(-db "$XUI_DB")
    else
        XUI_DB_DSN=$(env_value "$XUI_ENV_FILE" XUI_DB_DSN)
        [[ -n "$XUI_DB_DSN" ]] || die "XUI_DB_TYPE=postgres but XUI_DB_DSN is empty in ${XUI_ENV_FILE}."
        { command -v pg_dump > /dev/null 2>&1 && command -v pg_restore > /dev/null 2>&1; } ||
            die "3x-ui uses PostgreSQL, and pg_dump/pg_restore are needed for the backup and a rollback. Install the PostgreSQL client of your server's major version (e.g. apt install postgresql-client-16), then run the migration again."
        XUI_DB_ARGS=(-postgres)
    fi

    XUI_ACTIVE=$(systemctl is-active x-ui 2> /dev/null)
    XUI_ENABLED=$(systemctl is-enabled x-ui 2> /dev/null)
    [[ "$XUI_ACTIVE" == "active" ]] ||
        die "x-ui is not running (state: ${XUI_ACTIVE}). Start it ('systemctl start x-ui') so the migration can record what clients see and compare it afterwards."
    XUI_XRAY=0
    xray_running "${XUI_DIR}/bin/" && XUI_XRAY=1
}

# xray_running PREFIX: an Xray process whose executable lies under PREFIX.
xray_running() {
    local pid exe
    for pid in $(pgrep -f 'xray' 2> /dev/null); do
        exe=$(readlink -f "/proc/${pid}/exe" 2> /dev/null) || continue
        [[ "$exe" == "$1"xray* ]] && return 0
    done
    return 1
}

# --- still-ui release ------------------------------------------------------

# Puts the release archive and install.sh into $WORK and the still-ui binary
# from the archive into $STILL_BIN (it does the database work below).
fetch_still_ui() {
    local archive tag url
    archive="${WORK}/still-ui-linux-$(arch).tar.gz"
    if [[ -n "${STILL_UI_ARCHIVE:-}" ]]; then
        [[ -s "$STILL_UI_ARCHIVE" ]] || die "STILL_UI_ARCHIVE=${STILL_UI_ARCHIVE} does not exist or is empty."
        cp -f "$STILL_UI_ARCHIVE" "$archive" || die "Could not copy ${STILL_UI_ARCHIVE}."
    else
        tag="${STILL_UI_VERSION:-}"
        if [[ -z "$tag" ]]; then
            tag=$(curl -fsSL --max-time 30 "https://api.github.com/repos/${REPO}/releases/latest" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
            [[ -n "$tag" ]] || die "Could not find the latest still-ui release (GitHub API). Set STILL_UI_VERSION=<tag> or STILL_UI_ARCHIVE=<file>."
        fi
        url="https://github.com/${REPO}/releases/download/${tag}/still-ui-linux-$(arch).tar.gz"
        info "Downloading still-ui ${tag} ..."
        curl -fLR --retry 5 --retry-delay 3 --connect-timeout 15 -o "$archive" "$url" || die "Download failed: ${url}"
        if curl -fsSL --max-time 30 -o "${archive}.sha256" "${url}.sha256"; then
            local want got
            want=$(awk '{print $1; exit}' "${archive}.sha256")
            got=$(sha256sum "$archive" | awk '{print $1}')
            [[ "$want" == "$got" ]] || die "Checksum mismatch for ${url}."
        else
            warn "No .sha256 published next to the archive; continuing without a checksum check."
        fi
    fi
    tar -xzf "$archive" -C "$WORK" still-ui/x-ui 2> /dev/null || die "Not a still-ui release archive: ${archive}."
    STILL_BIN="${WORK}/still-ui/x-ui"
    chmod 700 "$STILL_BIN"
    STILL_ARCHIVE="$archive"
    STILL_UPSTREAM=$("$STILL_BIN" migrate-3xui version 2> /dev/null)
    [[ "$STILL_UPSTREAM" =~ ^[0-9]+\.[0-9]+ ]] ||
        die "This still-ui build cannot migrate (no 'migrate-3xui' command). Use a still-ui release that ships migrate.sh."

    if [[ -n "${STILL_UI_INSTALL_SCRIPT:-}" ]]; then
        cp -f "$STILL_UI_INSTALL_SCRIPT" "${WORK}/install.sh" || die "Could not copy ${STILL_UI_INSTALL_SCRIPT}."
    else
        curl -fsSL --retry 3 -o "${WORK}/install.sh" "https://raw.githubusercontent.com/${REPO}/main/install.sh" ||
            die "Could not download install.sh."
    fi
    grep -q 'STILL_UI_MIGRATING' "${WORK}/install.sh" || die "This install.sh does not support migrations."
}

migrate_cli() {
    XUI_DB_DSN="${XUI_DB_DSN:-}" "$STILL_BIN" migrate-3xui "$@"
}

# --- what clients see ------------------------------------------------------

# Reads an "inspect -format env" report into REP_* variables.
load_report() {
    local key value
    REP_SAMPLES=()
    REP_SETTINGS=()
    while IFS=$'\t' read -r key value; do
        case "$key" in
            inbounds) REP_INBOUNDS="$value" ;;
            clients) REP_CLIENTS="$value" ;;
            sampleSubId) REP_SAMPLES+=("$value") ;;
            setting.*) REP_SETTINGS+=("${key#setting.}=${value}") ;;
        esac
    done < "$1"
}

# setting KEY [DEFAULT]: a panel setting from the loaded report.
setting() {
    local kv
    for kv in "${REP_SETTINGS[@]}"; do
        if [[ "${kv%%=*}" == "$1" ]]; then
            echo "${kv#*=}"
            return 0
        fi
    done
    echo "${2:-}"
}

# probe_url LISTEN DOMAIN PORT CERT KEY: sets PROBE_URL (scheme://host:port)
# and PROBE_RESOLVE (curl --resolve) to reach a listener of this host the way
# a client does: by name, with the Host header and SNI of the configured
# domain. Without one a placeholder name is used, because links in a
# subscription carry the host the request came in for, and 3x-ui releases
# disagree on what to write for a loopback address.
probe_url() {
    local listen="$1" domain="$2" port="$3" cert="$4" key="$5" scheme=http host
    [[ -n "$cert" && -n "$key" ]] && scheme=https
    case "$listen" in
        "" | 0.0.0.0 | "::" | "[::]") host=127.0.0.1 ;;
        *:*) host="[${listen#[}"; host="${host%]}]" ;;
        *) host="$listen" ;;
    esac
    domain="${domain:-$PROBE_HOST}"
    PROBE_RESOLVE=(--resolve "${domain}:${port}:${host//[\[\]]/}")
    host="$domain"
    PROBE_URL="${scheme}://${host}:${port}"
}

http_code() {
    curl -k -s -o "${2:-/dev/null}" -w '%{http_code}' --max-time 15 -A 'still-ui-migrate' "${PROBE_RESOLVE[@]}" "$1" 2> /dev/null
}

# panel_target / sub_target set TARGET_URL and PROBE_RESOLVE for the panel's
# base path and the subscription path (call them, do not use $(...): the
# --resolve arguments live in a variable of this shell).
panel_target() {
    local base
    base=$(setting webBasePath /)
    [[ "$base" == /* ]] || base="/$base"
    [[ "$base" == */ ]] || base="$base/"
    probe_url "$(setting webListen)" "$(setting webDomain)" "$(setting webPort 2053)" "$(setting webCertFile)" "$(setting webKeyFile)"
    TARGET_URL="${PROBE_URL}${base}"
}

sub_target() {
    local path
    path=$(setting subPath /sub/)
    [[ "$path" == /* ]] || path="/$path"
    [[ "$path" == */ ]] || path="$path/"
    probe_url "$(setting subListen)" "$(setting subDomain)" "$(setting subPort 2096)" "$(setting subCertFile)" "$(setting subKeyFile)"
    TARGET_URL="${PROBE_URL}${path}"
}

# record_client_view DIR: the panel's HTTP status and the sampled
# subscriptions, as clients see them now.
record_client_view() {
    local dir="$1" i code
    mkdir -p "$dir"
    panel_target
    http_code "$TARGET_URL" > "${dir}/panel.code"
    if [[ "$(setting subEnable true)" == "true" ]]; then
        sub_target
        for i in "${!REP_SAMPLES[@]}"; do
            code=$(http_code "${TARGET_URL}${REP_SAMPLES[$i]}" "${dir}/sub-${i}.body")
            echo "$code" > "${dir}/sub-${i}.code"
        done
    fi
}

# --- backup ----------------------------------------------------------------

backup_db() { # name
    if [[ "$XUI_DB_TYPE" == "sqlite" ]]; then
        migrate_cli backup -src "$XUI_DB" -dst "${BK}/$1.db" > /dev/null || return 1
    else
        pg_dump_dsn -Fc -f "${BK}/$1.pgdump" || return 1
        pg_restore --list "${BK}/$1.pgdump" > /dev/null || return 1
    fi
}

# libpq reads the password from PGPASSWORD, so it never shows in ps.
pg_split_dsn() {
    PG_DSN_NOPASS="$XUI_DB_DSN"
    PG_PASS=""
    if [[ "$XUI_DB_DSN" =~ ^(postgres(ql)?://[^:/@]+):([^@]*)@(.*)$ ]]; then
        PG_DSN_NOPASS="${BASH_REMATCH[1]}@${BASH_REMATCH[4]}"
        PG_PASS=$(printf '%b' "${BASH_REMATCH[3]//%/\\x}")
    elif [[ "$XUI_DB_DSN" =~ (^|[[:space:]])password=([^[:space:]]*) ]]; then
        PG_PASS="${BASH_REMATCH[2]}"
        PG_DSN_NOPASS="${XUI_DB_DSN/password=${PG_PASS}/}"
    fi
}

pg_dump_dsn() {
    pg_split_dsn
    PGPASSWORD="$PG_PASS" pg_dump --no-owner -d "$PG_DSN_NOPASS" "$@"
}

pg_restore_dsn() {
    pg_split_dsn
    PGPASSWORD="$PG_PASS" pg_restore --no-owner --clean --if-exists --single-transaction -d "$PG_DSN_NOPASS" "$@"
}

# make_backup: everything a rollback needs, before anything changes.
make_backup() {
    local f d avail need
    avail=$(df -Pk "$BACKUP_PARENT" | awk 'NR==2 {print $4}')
    need=$(du -sk "$XUI_DIR" 2> /dev/null | awk '{print $1}')
    [[ "$XUI_DB_TYPE" == "sqlite" ]] && need=$((need + 3 * $(du -sk "$XUI_DB" | awk '{print $1}')))
    if [[ "$avail" =~ ^[0-9]+$ && "$need" =~ ^[0-9]+$ ]] && ((avail < need + 102400)); then
        die "Not enough free space in ${BACKUP_PARENT}: need about $(((need + 102400) / 1024)) MiB, have $((avail / 1024)) MiB."
    fi

    BK="${BACKUP_PARENT}/still-ui-migrate-$(date -u +%Y%m%dT%H%M%SZ)"
    (umask 077 && mkdir "$BK") || die "Could not create ${BK}."
    chmod 700 "$BK"
    info "Backing up 3x-ui to ${BK} ..."

    backup_db x-ui || die "Database backup failed; nothing was changed."
    # 3x-ui is running and rewrites bin/config.json (and logs) at will: GNU
    # tar exits 1 for "file changed as we read it", which is fine here. The
    # program files a rollback has to put back do not change.
    local tar_rc=0
    tar --warning=no-file-changed -C "$(dirname "$XUI_DIR")" -czf "${BK}/usr-local-x-ui.tar.gz" "$(basename "$XUI_DIR")" || tar_rc=$?
    ((tar_rc <= 1)) || die "Could not archive ${XUI_DIR}; nothing was changed."
    tar -tzf "${BK}/usr-local-x-ui.tar.gz" > /dev/null || die "The archive of ${XUI_DIR} is unreadable; nothing was changed."
    # Checksums of the program files only: runtime files would always differ.
    (cd / && find "${XUI_DIR#/}" -type f ! -name config.json ! -name '*.log' ! -path '*/mtproto/*' ! -path '*/tuic/*' -print0 |
        xargs -0 sha256sum) > "${BK}/usr-local-x-ui.sha256"

    mkdir -p "${BK}/files"
    save_file() {
        [[ -e "$1" ]] || return 0
        mkdir -p "${BK}/files$(dirname "$1")"
        cp -a "$1" "${BK}/files$1" || die "Could not back up $1; nothing was changed."
        echo "$1" >> "${BK}/files.list"
    }
    : > "${BK}/files.list"
    save_file "$XUI_UNIT"
    for d in /etc/systemd/system/x-ui.service.d /etc/systemd/system.control/x-ui.service.d $XUI_DROPINS; do
        save_file "$d"
    done
    save_file /usr/bin/x-ui
    [[ -n "$XUI_ENV_FILE" ]] && save_file "$XUI_ENV_FILE"
    for f in "${FAIL2BAN_FILES[@]}"; do
        save_file "$f"
    done
    # The rest of 3x-ui's data folder (node token key, install record); the
    # database itself is the consistent copy above.
    if [[ "$XUI_DB_TYPE" == "sqlite" ]]; then
        for f in "$(dirname "$XUI_DB")"/*; do
            case "$f" in "$XUI_DB" | "$XUI_DB"-wal | "$XUI_DB"-shm) continue ;; esac
            save_file "$f"
        done
    fi
    sort -u -o "${BK}/files.list" "${BK}/files.list"

    # A copy of this script, so the rollback works offline.
    if [[ -f "$0" && -s "$0" ]]; then
        cp "$0" "${BK}/migrate.sh"
    else
        curl -fsSL -o "${BK}/migrate.sh" "https://raw.githubusercontent.com/${REPO}/main/migrate.sh" ||
            warn "Could not keep a copy of migrate.sh in the backup; 'still-ui migrate-rollback' downloads it when needed."
    fi
    chmod 700 "${BK}/migrate.sh" 2> /dev/null

    {
        echo "# still-ui migration backup; read by migrate.sh migrate-rollback."
        echo "CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        echo "XUI_VERSION=${XUI_VERSION}"
        echo "XUI_FORK=${XUI_FORK}"
        echo "XUI_DIR=${XUI_DIR}"
        echo "XUI_UNIT=${XUI_UNIT}"
        echo "XUI_ENV_FILE=${XUI_ENV_FILE}"
        echo "XUI_DB_TYPE=${XUI_DB_TYPE}"
        echo "XUI_DB=${XUI_DB}"
        echo "XUI_ACTIVE=${XUI_ACTIVE}"
        echo "XUI_ENABLED=${XUI_ENABLED}"
        echo "XUI_XRAY=${XUI_XRAY}"
        echo "STILL_UPSTREAM=${STILL_UPSTREAM}"
    } > "${BK}/manifest.env"
    for f in "${FAIL2BAN_FILES[@]}"; do
        [[ -e "$f" ]] || echo "$f" >> "${BK}/fail2ban.absent"
    done
    write_sums
    echo "backed-up" > "${BK}/STATE"
}

write_sums() {
    (cd "$BK" && find . -type f ! -name manifest.sha256 ! -name STATE ! -path './after-rollback/*' ! -path './check/*' \
        ! -name '*.log' -print0 | sort -z | xargs -0 sha256sum) > "${BK}/manifest.sha256"
}

# --- migrate ---------------------------------------------------------------

stop_xui() {
    systemctl stop x-ui || return 1
    STOPPED_XUI=1
    local i
    for i in $(seq 1 30); do
        xray_running "${XUI_DIR}/bin/" || break
        sleep 1
    done
    # Sidecars 3x-ui starts outside its own cgroup must not keep the ports.
    local pid exe
    for pid in $(pgrep -f "${XUI_DIR}/bin/|bin/(xray|mtg|tuic)" 2> /dev/null); do
        exe=$(readlink -f "/proc/${pid}/exe" 2> /dev/null) || continue
        [[ "$exe" == "${XUI_DIR}/"* ]] && kill "$pid" 2> /dev/null
    done
    sleep 1
    ! xray_running "${XUI_DIR}/bin/"
}

hand_db_to_still_ui() {
    local f dst_env
    if [[ "$XUI_DB_TYPE" == "sqlite" ]]; then
        install -d -m 700 "$STILL_DB_DIR" || return 1
        cp "${BK}/x-ui.at-stop.db" "${STILL_DB_DIR}/x-ui.db" || return 1
        chmod 600 "${STILL_DB_DIR}/x-ui.db"
        f="$(dirname "$XUI_DB")/node_token_key.json"
        [[ -f "$f" ]] && cp -a "$f" "${STILL_DB_DIR}/"
    fi
    if [[ -n "$XUI_ENV_FILE" ]]; then
        dst_env="${XUI_ENV_FILE%/x-ui}/still-ui"
        # 3x-ui's folders must not follow: still-ui keeps its own, and an
        # XUI_DB_FOLDER pointing at /etc/x-ui would open 3x-ui's database.
        grep -Ev '^[[:space:]]*(export[[:space:]]+)?XUI_(DB_FOLDER|BIN_FOLDER|LOG_FOLDER|MAIN_FOLDER)=' "$XUI_ENV_FILE" > "$dst_env" || true
        chmod 600 "$dst_env"
    fi
    return 0
}

run_install() {
    local fail2ban=false f
    for f in "${FAIL2BAN_FILES[@]}"; do
        [[ -e "$f" ]] && fail2ban=true
    done
    info "Installing still-ui (log: ${BK}/install.log) ..."
    (cd "$WORK" && env NONINTERACTIVE=1 XUI_NONINTERACTIVE=1 STILL_UI_MIGRATING=1 \
        STILL_UI_ARCHIVE="$STILL_ARCHIVE" XUI_ENABLE_FAIL2BAN="$fail2ban" \
        bash "${WORK}/install.sh" < /dev/null > "${BK}/install.log" 2>&1)
}

# check_still_ui: everything clients and the admin rely on is there.
check_still_ui() {
    local deadline=$((SECONDS + TIMEOUT)) want got i rc note
    want=$(cat "${BK}/before/panel.code")
    panel_target
    info "Waiting for still-ui (up to ${TIMEOUT}s) ..."
    while ((SECONDS < deadline)); do
        if systemctl is-active --quiet still-ui && [[ "$(http_code "$TARGET_URL")" == "$want" ]]; then
            break
        fi
        sleep 2
    done
    systemctl is-active --quiet still-ui || { err "still-ui is not running."; return 1; }
    got=$(http_code "$TARGET_URL")
    [[ "$got" == "$want" ]] || { err "The panel answers ${got} at its address (3x-ui answered ${want})."; return 1; }
    info "ok   panel answers ${got} on the same port and base path"

    if [[ "$XUI_XRAY" == "1" ]]; then
        while ((SECONDS < deadline)) && ! xray_running "${STILL_DIR}/bin/"; do
            sleep 2
        done
        xray_running "${STILL_DIR}/bin/" || { err "Xray is not running under still-ui."; return 1; }
        info "ok   Xray is running"
    fi

    if [[ "$XUI_DB_TYPE" == "sqlite" ]]; then
        migrate_cli verify -pre "${BK}/before/report.json" -db "${STILL_DB_DIR}/x-ui.db" > "${BK}/verify.log" 2>&1
    else
        migrate_cli verify -pre "${BK}/before/report.json" -postgres > "${BK}/verify.log" 2>&1
    fi
    rc=$?
    sed 's/^/     /' "${BK}/verify.log"
    ((rc == 0)) || { err "The database does not match what 3x-ui had."; return 1; }

    if [[ "$(setting subEnable true)" == "true" ]]; then
        sub_target
        mkdir -p "${BK}/after"
        for i in "${!REP_SAMPLES[@]}"; do
            [[ -f "${BK}/before/sub-${i}.code" ]] || continue
            want=$(cat "${BK}/before/sub-${i}.code")
            got=$(http_code "${TARGET_URL}${REP_SAMPLES[$i]}" "${BK}/after/sub-${i}.body")
            if [[ "$want" != "200" ]]; then
                # Nothing to compare against: 3x-ui itself failed this one
                # (some 2.x releases answer 500 for valid clients).
                warn "--   subscription sample $((i + 1)): 3x-ui answered ${want}, still-ui answers ${got}; not compared"
                continue
            fi
            if [[ "$got" != "$want" ]]; then
                err "Subscription sample $((i + 1)) answers ${got} (3x-ui answered ${want})."
                return 1
            fi
            note=$(migrate_cli subcompare -a "${BK}/before/sub-${i}.body" -b "${BK}/after/sub-${i}.body")
            case $? in
                0) info "ok   subscription sample $((i + 1)) is byte-identical" ;;
                3) warn "ok   subscription sample $((i + 1)): the same links; only remark text differs (traffic/expiry figures)" ;;
                4) warn "ok   subscription sample $((i + 1)): every link keeps all its settings; newer link format${note:+ (${note})}" ;;
                *)
                    err "Subscription sample $((i + 1)) differs from what 3x-ui served${note:+: ${note}} (${BK}/before and ${BK}/after)."
                    return 1
                    ;;
            esac
        done
    fi

    if [[ "${STILL_UI_MIGRATE_TEST_FAIL:-}" == "health" ]]; then
        err "STILL_UI_MIGRATE_TEST_FAIL=health: failing the check on purpose."
        return 1
    fi
    return 0
}

# Keeps 3x-ui from starting next to still-ui: its unit is in the backup.
retire_xui() {
    local d
    systemctl disable x-ui > /dev/null 2>&1
    rm -f "$XUI_UNIT"
    for d in /etc/systemd/system/x-ui.service.d /etc/systemd/system.control/x-ui.service.d; do
        rm -rf "$d"
    done
    systemctl daemon-reload
    systemctl reset-failed x-ui > /dev/null 2>&1
    # Muscle memory: "x-ui" opens still-ui's menu from now on.
    cat > /usr/bin/x-ui << 'EOF'
#!/bin/sh
# Installed by still-ui's migrate.sh: this host now runs still-ui.
# 'still-ui migrate-rollback' brings 3x-ui (and the original /usr/bin/x-ui) back.
exec /usr/bin/still-ui "$@"
EOF
    chmod 755 /usr/bin/x-ui
}

on_exit() {
    local rc=$?
    if [[ "$STOPPED_XUI" == "1" && "$MIGRATION_DONE" != "1" ]]; then
        err "The migration did not finish; restoring 3x-ui from ${BK} ..."
        do_rollback "$BK" auto
        rc=1
    fi
    if [[ "$STOPPED_XUI" != "1" && -n "$BK" && -f "${BK}/STATE" && "$(cat "${BK}/STATE")" == "backed-up" ]]; then
        echo "aborted" > "${BK}/STATE"
    fi
    [[ -n "$WORK" && -d "$WORK" ]] && rm -rf "$WORK"
    exit "$rc"
}

cmd_migrate() {
    detect_3xui
    info "Found 3x-ui ${XUI_VERSION} (${XUI_FORK}), database: ${XUI_DB_TYPE}${XUI_DB:+ ${XUI_DB}}, x-ui is ${XUI_ACTIVE}, Xray $([[ $XUI_XRAY == 1 ]] && echo running || echo 'not running')."

    WORK=$(mktemp -d "${BACKUP_PARENT}/.still-ui-migrate-work.XXXXXX") || die "Could not create a work folder."
    trap on_exit EXIT
    trap 'exit 130' INT TERM
    fetch_still_ui
    if [[ "${STILL_UI_MIGRATE_ALLOW_NEWER:-}" != "1" ]] && ! version_ge "$(minor_of "$STILL_UPSTREAM").999" "$XUI_VERSION"; then
        die "3x-ui ${XUI_VERSION} is newer than the 3x-ui ${STILL_UPSTREAM} this still-ui is based on; its database may hold data still-ui does not know. Wait for a still-ui release based on it, or set STILL_UI_MIGRATE_ALLOW_NEWER=1."
    fi

    # What has to survive, read while 3x-ui serves clients.
    local before="${WORK}/before"
    mkdir -p "$before"
    migrate_cli inspect "${XUI_DB_ARGS[@]}" > "${before}/report.json" 2> "${before}/inspect.err" ||
        die "Could not read the 3x-ui database: $(cat "${before}/inspect.err")"
    migrate_cli inspect "${XUI_DB_ARGS[@]}" -format env > "${before}/report.env" || die "Could not read the 3x-ui database."
    load_report "${before}/report.env"
    record_client_view "$before"
    panel_target
    info "3x-ui has ${REP_INBOUNDS} inbound(s) and ${REP_CLIENTS} client(s); its panel answers $(cat "${before}/panel.code") at ${PROBE_URL}/…; ${#REP_SAMPLES[@]} subscription(s) sampled."
    [[ "$(cat "${before}/panel.code")" =~ ^[23] ]] ||
        die "The 3x-ui panel does not answer at its own address ($(cat "${before}/panel.code")); fix that first so the result can be checked."

    echo
    echo "The migration will:"
    echo "  - back up 3x-ui to ${BACKUP_PARENT}/still-ui-migrate-<time>/ (database, ${XUI_DIR}, unit, env file)"
    echo "  - stop 3x-ui (clients reconnect within seconds) and install still-ui with a copy of its database"
    echo "  - keep ports, certificates, base path, subscription URLs, client links and the panel login"
    echo "  - roll back on its own if still-ui does not come up identical"
    echo
    confirm "Migrate 3x-ui ${XUI_VERSION} to still-ui now?" || die "Cancelled; nothing was changed."

    make_backup
    mv "$before" "${BK}/before"

    info "Stopping 3x-ui ..."
    stop_xui || die "Could not stop x-ui cleanly."
    systemctl disable x-ui > /dev/null 2>&1
    # The copy still-ui gets: the database as 3x-ui left it on shutdown.
    backup_db x-ui.at-stop || die "Could not copy the database after stopping 3x-ui."
    [[ "$XUI_DB_TYPE" == "sqlite" ]] && migrate_cli inspect -db "${BK}/x-ui.at-stop.db" > "${BK}/before/report.json"
    write_sums
    echo "stopped" > "${BK}/STATE"

    hand_db_to_still_ui || die "Could not give still-ui the database."
    run_install || die "The still-ui installer failed (see ${BK}/install.log)."
    check_still_ui || die "still-ui did not pass the checks."

    retire_xui
    MIGRATION_DONE=1
    echo "migrated" > "${BK}/STATE"
    echo
    info "Done: this host runs still-ui with 3x-ui's inbounds, clients and login."
    info "Backup: ${BK}"
    info "Undo:   still-ui migrate-rollback   (or: bash ${BK}/migrate.sh migrate-rollback ${BK})"
    info "Manage the panel with 'still-ui' (the 'x-ui' command opens the same menu)."
}

# --- rollback --------------------------------------------------------------

newest_backup() {
    local d
    # Names carry a UTC timestamp, so a reverse sort is newest first.
    for d in $(find "$BACKUP_PARENT" -maxdepth 1 -type d -name 'still-ui-migrate-*' 2> /dev/null | sort -r); do
        d="${d}/"
        [[ -f "${d}manifest.env" ]] || continue
        # Backups of an attempt that never stopped 3x-ui, or one already
        # rolled back, are not what "undo the migration" means.
        case "$(cat "${d}STATE" 2> /dev/null)" in
            stopped | migrated | rollback-failed)
                echo "${d%/}"
                return 0
                ;;
        esac
    done
    return 1
}

# do_rollback DIR [auto]
do_rollback() {
    local dir="$1" mode="${2:-}" f d rc=0 state
    [[ -f "${dir}/manifest.env" ]] || die "${dir} is not a migration backup (no manifest.env)."
    if ! (cd "$dir" && sha256sum --quiet -c manifest.sha256); then
        err "The backup in ${dir} does not match its checksums; refusing to restore from it."
        return 1
    fi
    local M_XUI_DIR M_XUI_UNIT M_XUI_DB_TYPE M_XUI_DB M_XUI_ACTIVE M_XUI_ENABLED M_XUI_XRAY M_XUI_ENV_FILE
    M_XUI_DIR=$(env_value "${dir}/manifest.env" XUI_DIR)
    M_XUI_UNIT=$(env_value "${dir}/manifest.env" XUI_UNIT)
    M_XUI_DB_TYPE=$(env_value "${dir}/manifest.env" XUI_DB_TYPE)
    M_XUI_DB=$(env_value "${dir}/manifest.env" XUI_DB)
    M_XUI_ACTIVE=$(env_value "${dir}/manifest.env" XUI_ACTIVE)
    M_XUI_ENABLED=$(env_value "${dir}/manifest.env" XUI_ENABLED)
    M_XUI_XRAY=$(env_value "${dir}/manifest.env" XUI_XRAY)
    M_XUI_ENV_FILE=$(env_value "${dir}/manifest.env" XUI_ENV_FILE)
    [[ -n "$M_XUI_DIR" && -n "$M_XUI_UNIT" ]] || {
        err "Incomplete manifest in ${dir}."
        return 1
    }
    state=$(cat "${dir}/STATE" 2> /dev/null)
    if [[ "$mode" != "auto" ]]; then
        warn "This restores 3x-ui as it was on $(env_value "${dir}/manifest.env" CREATED) (backup state: ${state})."
        warn "Changes made in still-ui since then are not carried back; still-ui's database is kept in ${dir}/after-rollback/."
        confirm "Roll back to 3x-ui $(env_value "${dir}/manifest.env" XUI_VERSION)?" || die "Cancelled; nothing was changed."
    fi
    local after
    after="${dir}/after-rollback/$(date -u +%Y%m%dT%H%M%SZ)"
    mkdir -p "$after"

    # 1. still-ui off and out of the way; its data is kept.
    info "Stopping still-ui ..."
    systemctl disable --now still-ui > /dev/null 2>&1
    systemctl disable --now still-xray > /dev/null 2>&1
    local pid exe
    for pid in $(pgrep -f 'bin/(xray|mtg|tuic)' 2> /dev/null); do
        exe=$(readlink -f "/proc/${pid}/exe" 2> /dev/null) || continue
        [[ "$exe" == "${STILL_DIR}/"* ]] && kill "$pid" 2> /dev/null
    done
    if [[ "$M_XUI_DB_TYPE" == "postgres" ]]; then
        XUI_DB_DSN=$(env_value "${dir}/files${M_XUI_ENV_FILE}" XUI_DB_DSN)
        pg_dump_dsn -Fc -f "${after}/still-ui.pgdump" || warn "Could not dump still-ui's PostgreSQL data before restoring."
    fi
    [[ -d "$STILL_DB_DIR" ]] && mv "$STILL_DB_DIR" "${after}/etc-still-ui"
    for f in /etc/default/still-ui /etc/conf.d/still-ui /etc/sysconfig/still-ui /etc/x-ui/install-result.env.still-ui; do
        [[ -e "$f" ]] && mv "$f" "${after}/"
    done
    rm -rf "$STILL_DIR" /usr/bin/still-ui /etc/systemd/system/still-ui.service /etc/systemd/system/still-ui.service.d \
        /etc/systemd/system/still-xray.service /etc/systemd/system/still-xray.service.d
    [[ -d /var/log/still-ui ]] && mv /var/log/still-ui "${after}/var-log-still-ui"

    # 2. 3x-ui's files, exactly as backed up.
    if ! (cd / && sha256sum --quiet -c "${dir}/usr-local-x-ui.sha256" > /dev/null 2>&1); then
        info "Restoring ${M_XUI_DIR} ..."
        [[ -e "$M_XUI_DIR" ]] && mv "$M_XUI_DIR" "${after}/usr-local-x-ui"
        tar -C "$(dirname "$M_XUI_DIR")" -xzf "${dir}/usr-local-x-ui.tar.gz" || rc=1
    fi
    while IFS= read -r f; do
        [[ -n "$f" ]] || continue
        rm -rf "$f"
        mkdir -p "$(dirname "$f")"
        cp -a "${dir}/files${f}" "$f" || rc=1
    done < "${dir}/files.list"
    if [[ -f "${dir}/fail2ban.absent" ]]; then
        while IFS= read -r f; do
            [[ -n "$f" ]] && rm -f "$f"
        done < "${dir}/fail2ban.absent"
    fi

    # 3. The database as 3x-ui left it.
    info "Restoring the 3x-ui database ..."
    if [[ "$M_XUI_DB_TYPE" == "sqlite" ]]; then
        local src="${dir}/x-ui.at-stop.db"
        [[ -f "$src" ]] || src="${dir}/x-ui.db"
        install -d -m 700 "$(dirname "$M_XUI_DB")"
        rm -f "${M_XUI_DB}-wal" "${M_XUI_DB}-shm"
        cp "$src" "${M_XUI_DB}.restore.$$" && chmod 600 "${M_XUI_DB}.restore.$$" && mv -f "${M_XUI_DB}.restore.$$" "$M_XUI_DB" || rc=1
    else
        local src="${dir}/x-ui.at-stop.pgdump"
        [[ -f "$src" ]] || src="${dir}/x-ui.pgdump"
        pg_restore_dsn "$src" || rc=1
    fi

    # 4. 3x-ui back on.
    systemctl daemon-reload
    systemctl reset-failed x-ui still-ui > /dev/null 2>&1
    [[ "$M_XUI_ENABLED" == "enabled" ]] && systemctl enable x-ui > /dev/null 2>&1
    if systemctl is-active --quiet fail2ban 2> /dev/null; then
        systemctl restart fail2ban > /dev/null 2>&1
    fi
    if [[ "$M_XUI_ACTIVE" == "active" ]]; then
        systemctl start x-ui || rc=1
        local deadline=$((SECONDS + TIMEOUT)) want="" url=""
        if [[ -f "${dir}/before/report.env" ]]; then
            load_report "${dir}/before/report.env"
            want=$(cat "${dir}/before/panel.code" 2> /dev/null)
            panel_target
            url="$TARGET_URL"
        fi
        while ((SECONDS < deadline)); do
            if systemctl is-active --quiet x-ui && { [[ -z "$want" ]] || [[ "$(http_code "$url")" == "$want" ]]; } &&
                { [[ "$M_XUI_XRAY" != "1" ]] || xray_running "${M_XUI_DIR}/bin/"; }; then
                break
            fi
            sleep 2
        done
        if ! systemctl is-active --quiet x-ui; then
            err "x-ui did not start; see 'journalctl -u x-ui'."
            rc=1
        elif [[ -n "$want" && "$(http_code "$url")" != "$want" ]]; then
            err "x-ui runs, but the panel does not answer ${want} at its address."
            rc=1
        elif [[ "$M_XUI_XRAY" == "1" ]] && ! xray_running "${M_XUI_DIR}/bin/"; then
            err "x-ui runs, but Xray does not."
            rc=1
        else
            info "3x-ui is running again$([[ $M_XUI_XRAY == 1 ]] && echo ', Xray included')."
            rollback_compare_subs "$dir" || rc=1
        fi
    fi
    if ((rc == 0)); then
        echo "rolled-back" > "${dir}/STATE"
        info "Rollback finished. still-ui's data is kept in ${after}."
    else
        echo "rollback-failed" > "${dir}/STATE"
        err "The rollback did not finish cleanly. Everything needed is in ${dir} (files/, usr-local-x-ui.tar.gz, the database copy)."
    fi
    return "$rc"
}

# sub_normalized FILE: a subscription body decoded, with the spiderX path
# 3x-ui 2.x draws at random for every request blanked out.
sub_normalized() {
    local text
    text=$(base64 -d "$1" 2> /dev/null) || text=$(cat "$1")
    [[ "$text" == *"://"* ]] || text=$(cat "$1")
    printf '%s\n' "$text" | sed -E 's/([?&])spx=[^&#]*/\1spx=/g'
}

# rollback_compare_subs DIR: 3x-ui serves the sampled subscriptions as before
# the migration (the restored database is the one it stopped with).
rollback_compare_subs() {
    local dir="$1" i code bad=0
    [[ -f "${dir}/before/report.env" && "$(setting subEnable true)" == "true" ]] || return 0
    sub_target
    mkdir -p "${dir}/check"
    for i in "${!REP_SAMPLES[@]}"; do
        [[ -f "${dir}/before/sub-${i}.code" ]] || continue
        code=$(http_code "${TARGET_URL}${REP_SAMPLES[$i]}" "${dir}/check/sub-${i}.body")
        if [[ "$code" == "$(cat "${dir}/before/sub-${i}.code")" ]] &&
            [[ "$(sub_normalized "${dir}/before/sub-${i}.body")" == "$(sub_normalized "${dir}/check/sub-${i}.body")" ]]; then
            info "ok   subscription sample $((i + 1)) is the same as before the migration"
        else
            err "Subscription sample $((i + 1)) differs from before the migration (${dir}/before, ${dir}/check)."
            bad=1
        fi
    done
    return "$bad"
}

cmd_rollback() {
    [[ $EUID -eq 0 ]] || die "Run this as root."
    local dir="${1:-}"
    if [[ -z "$dir" ]]; then
        dir=$(newest_backup) || die "No migration backup found in ${BACKUP_PARENT}/still-ui-migrate-*."
    fi
    dir="${dir%/}"
    # Run the copy of the script the backup was made with: it knows its layout.
    if [[ -z "${STILL_UI_MIGRATE_NESTED:-}" && -f "${dir}/migrate.sh" && "$(readlink -f "$0")" != "$(readlink -f "${dir}/migrate.sh")" ]]; then
        STILL_UI_MIGRATE_NESTED=1 exec bash "${dir}/migrate.sh" migrate-rollback "$dir"
    fi
    STILL_BIN="${dir}/.no-binary" # rollback needs no still-ui binary
    do_rollback "$dir"
}

usage() {
    cat << EOF
usage: $0 migrate | migrate-rollback [BACKUP_DIR]
  migrate            move this host's 3x-ui to still-ui (backup, checks, automatic rollback)
  migrate-rollback   restore 3x-ui from the newest (or the given) /root/still-ui-migrate-* backup
EOF
}

main() {
    case "${1:-}" in
        migrate) cmd_migrate ;;
        migrate-rollback | rollback)
            shift
            cmd_rollback "$@"
            ;;
        *)
            usage
            exit 2
            ;;
    esac
}

# Sourced by the tests for its functions; run otherwise.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
