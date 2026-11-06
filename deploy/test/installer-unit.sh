#!/usr/bin/env bash
# Unit tests of the installer and lifecycle scripts (install.sh, update.sh,
# x-ui.sh) and of the systemd units they install. No root, no network, no
# systemd: each test loads single functions exactly as the script defines them,
# with every absolute system path (/etc/..., /proc/meminfo, /swapfile,
# /run/systemd/system, /usr/bin/still-ui, /var/log/still-*) rewritten into a
# private scratch root, and system commands (systemctl, useradd, mkswap, ...)
# replaced by stubs that only record their arguments. A function that still
# names a system path after the rewrite is refused, so a test can never touch
# the machine it runs on.
#
#   bash deploy/test/installer-unit.sh
#
# INSTALL_SH, UPDATE_SH, XUI_SH and UNIT_DIR point the tests at other copies
# (deploy/test/installer-mutations.sh uses that to check that every test fails
# when the behaviour it guards is broken).
#
# The end-to-end counterpart on real systems is deploy/test/installer-e2e.sh.
# Variables assigned below are read by the functions under test (and some are
# set by them), the stubs are only called from them, and single quotes hold
# literal shell text on purpose.
# shellcheck disable=SC2016,SC2034,SC2154,SC2317,SC2329
set -uo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
INSTALL_SH=${INSTALL_SH:-$repo_root/install.sh}
UPDATE_SH=${UPDATE_SH:-$repo_root/update.sh}
XUI_SH=${XUI_SH:-$repo_root/x-ui.sh}
UNIT_DIR=${UNIT_DIR:-$repo_root}
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
failures=0
passes=0

pass() {
    echo "ok   $1"
    passes=$((passes + 1))
}
fail() {
    echo "FAIL $1"
    [[ -z "${2:-}" ]] || printf '     %s\n' "${2//$'\n'/$'\n'     }"
    failures=$((failures + 1))
}
# t DESCRIPTION COMMAND...: the command (a test function, run in a subshell so
# its state does not leak) must succeed and no assertion in it may fail. The
# functions under test do not run under errexit (the scripts do not use it), so
# a failed assertion is recorded in a file rather than stopping the test.
assert_failed="$work/assert-failed"
t() {
    local what="$1" out rc
    shift
    rm -f "$assert_failed"
    out=$("$@" 2>&1)
    rc=$?
    [[ ! -e "$assert_failed" ]] || rc=1
    if [[ $rc == 0 ]]; then pass "$what"; else fail "$what" "$out"; fi
}
# Assertions used inside test functions; each prints why it failed.
afail() { echo "$*"; echo x >> "$assert_failed"; return 1; }
eq() { [[ "$1" == "$2" ]] || afail "got '$1', want '$2' ${3:-}"; }
has() { grep -qF -- "$2" "$1" || afail "$1 lacks '$2':" "$(cat "$1" 2> /dev/null)"; }
hasnt() { ! grep -qF -- "$2" "$1" 2> /dev/null || afail "$1 has '$2':" "$(cat "$1")"; }
count() { local n; n=$(grep -cF -- "$2" "$1" 2> /dev/null); eq "${n:-0}" "$3" "(lines with '$2' in $1)"; }
called() { has "$R/calls" "$1"; }
not_called() { hasnt "$R/calls" "$1"; }

# load FILE NAME: defines function NAME from FILE, with its system paths moved
# under $R (a new scratch root per test, see fresh_root).
load() {
    local file="$1" name="$2" def left
    def=$(awk -v name="$name" '
        $0 == name "() {" {copy = 1}
        copy {print}
        copy && /^}$/ {exit}
    ' "$file")
    [[ -n "$def" ]] || { echo "missing function $name in $file"; return 1; }
    def=$(printf '%s\n' "$def" | sed -E \
        "s#(^|[^A-Za-z0-9_.}/])(/proc/meminfo|/swapfile|/etc/[A-Za-z0-9._-]+|/run/systemd/system|/usr/bin|/var/log/still-[a-z]+|/root/still-ui-migrate|/root/still-ui-3x-ui-retired)#\1${R}\2#g")
    # Anything else absolute that is not a stubbed program path is a test bug.
    left=$(printf '%s\n' "${def//"$R"/@ROOT@}" | grep -v '^[[:space:]]*#' |
        grep -oE '(^|[^A-Za-z0-9_.}/$@])/(etc|proc|run|var|usr|root|swapfile|sys|boot|opt|home|srv)(/[A-Za-z0-9._-]*)*' |
        grep -vE '/(usr/sbin/nologin|sbin/nologin|usr/local/still-ui|usr/local/x-ui)$' || true)
    [[ -z "$left" ]] || { echo "$name in $file still names system paths: $left"; return 1; }
    eval "$def"
}

# fresh_root: a new scratch root with the folders the functions expect.
fresh_root() {
    R=$(mktemp -d "$work/root.XXXXXX")
    mkdir -p "$R/proc" "$R/etc/default" "$R/etc/sysctl.d" "$R/etc/systemd/system" "$R/usr/local/still-ui/bin" "$R/usr/bin"
    : > "$R/calls"
    : > "$R/etc/fstab"
    xui_folder="$R/usr/local/still-ui"
    xui_service="$R/etc/systemd/system"
    release=debian
    NONINTERACTIVE=1
    red='' green='' blue='' yellow='' plain=''
    meminfo $((2 * 1024 * 1024)) 0
}
meminfo() { printf 'MemTotal:       %s kB\nMemFree:  1000 kB\nSwapTotal:      %s kB\n' "$1" "$2" > "$R/proc/meminfo"; }

# --- stubs: record the call, change nothing outside $R -------------------------
log_call() { printf '%s\n' "$*" >> "$R/calls"; }
systemctl() {
    log_call "systemctl $*"
    case "$1" in
        --version) printf 'systemd %s (%s)\nfeatures\n' "${SYSTEMD_VERSION:-255}" "${SYSTEMD_VERSION:-255}" ;;
        is-enabled) [[ "${SYSTEMCTL_ENABLED:-no}" == yes ]] ;;
        *) return 0 ;;
    esac
}
useradd() { log_call "useradd $*"; touch "$R/user-still-xray"; }
userdel() { log_call "userdel $*"; rm -f "$R/user-still-xray"; }
id() { [[ "$1" == still-xray && -e "$R/user-still-xray" ]]; }
chgrp() { log_call "chgrp $*"; }
chown() { log_call "chown $*"; }
sysctl() { log_call "sysctl $*"; }
mkswap() { log_call "mkswap $*"; [[ "${MKSWAP_FAIL:-0}" == 0 ]]; }
swapon() { log_call "swapon $*"; [[ "${SWAPON_FAIL:-0}" == 0 ]]; }
swapoff() { log_call "swapoff $*"; }
# Both leave a partial file behind when they fail, as a full disk does.
fallocate() {
    log_call "fallocate $*"
    printf 'swap' > "${*: -1}"
    [[ "${FALLOCATE_FAIL:-0}" == 0 ]]
}
dd() {
    log_call "dd $*"
    local a
    for a in "$@"; do [[ "$a" != of=* ]] || printf 'swap' > "${a#of=}"; done
    [[ "${DD_FAIL:-0}" == 0 ]]
}
systemd-detect-virt() { log_call "systemd-detect-virt $*"; [[ "${IN_CONTAINER:-0}" == 1 ]]; }
rc-service() { log_call "rc-service $*"; }
rc-update() { log_call "rc-update $*"; }
pkill() { log_call "pkill $*"; return 0; }
nft() { log_call "nft $*"; }
# install(1) without the ownership flags: the tests do not run as root.
install() {
    local args=()
    while (($#)); do
        case "$1" in
            -o | -g) shift 2 ;;
            *) args+=("$1"); shift ;;
        esac
    done
    command install "${args[@]}"
}

# A panel binary stub: x-ui setting / xray-unit-check / migrate / -v.
panel_stub() {
    cat > "$xui_folder/x-ui" << 'EOF'
#!/usr/bin/env bash
printf 'x-ui %s\n' "$*" >> "$STUB_CALLS"
case "$1" in
    -v) echo "${STUB_VERSION:-1.5.1}" ;;
    xray-unit-check)
        if [[ "${2:-}" == -h ]]; then
            [[ "${STUB_UNIT_CHECK:-new}" == old ]] && { echo "unknown command"; exit 2; }
            echo "  -net-admin   the unit grants CAP_NET_ADMIN"
            [[ "${STUB_UNIT_CHECK:-new}" == no-devices ]] || echo "  -devices     PrivateDevices=no"
            exit 0
        fi
        [[ "${STUB_UNIT_CHECK_RC:-0}" == 0 ]] || echo "inbound 3 needs TPROXY"
        exit "${STUB_UNIT_CHECK_RC:-0}" ;;
esac
exit 0
EOF
    chmod +x "$xui_folder/x-ui"
    export STUB_CALLS="$R/calls"
}

# ==============================================================================
# install.sh / update.sh: helpers that both scripts carry
# ==============================================================================

# The shared helpers are copied into both scripts; they must stay identical,
# so the tests below exercise each copy.
for f in _still_env_set _still_size_mib _still_xray_unit _still_memory_dropin \
    _xui_systemd_major_version _xui_warn_unsupported_hardening gen_random_string setup_fail2ban; do
    a=$(awk -v n="$f" '$0 == n "() {" {c = 1} c {print} c && /^}$/ {exit}' "$INSTALL_SH")
    b=$(awk -v n="$f" '$0 == n "() {" {c = 1} c {print} c && /^}$/ {exit}' "$UPDATE_SH")
    if [[ -n "$a" && "$a" == "$b" ]]; then
        pass "install.sh and update.sh carry the same $f"
    else
        fail "install.sh and update.sh carry the same $f" "$(diff <(printf '%s\n' "$a") <(printf '%s\n' "$b") | head -20)"
    fi
done

for script in "$INSTALL_SH" "$UPDATE_SH"; do
    s=$(basename "$script")

    # --- _still_size_mib ---------------------------------------------------
    size_cases() {
        fresh_root
        load "$script" _still_size_mib || return 1
        eq "$(_still_size_mib 800M)" 800
        eq "$(_still_size_mib 2G)" 2048
        eq "$(_still_size_mib 1T)" 1048576
        eq "$(_still_size_mib 524288K)" 512
        eq "$(_still_size_mib 1048576)" 1
        local bad
        for bad in '' 1.5G 1g -1M 'lots' '800 M' 1GiB; do eq "$(_still_size_mib "$bad")" "" "for '$bad'"; done
    }
    t "$s: _still_size_mib reads K/M/G/T and bytes, nothing else" size_cases

    # --- _still_env_set ----------------------------------------------------
    env_set_new() {
        fresh_root
        load "$script" _still_env_set || return 1
        _still_env_set "$R/etc/default/still-ui" XUI_XRAY_UNIT still-xray || return 1
        eq "$(cat "$R/etc/default/still-ui")" "XUI_XRAY_UNIT=still-xray"
        eq "$(stat -c %a "$R/etc/default/still-ui")" 600
    }
    t "$s: _still_env_set creates a missing env file 0600" env_set_new
    env_set_edit() {
        fresh_root
        load "$script" _still_env_set || return 1
        local f="$R/etc/default/still-ui"
        printf '# kept comment\nXUI_DB_TYPE=postgres\nexport XUI_XRAY_UNIT=old\n  XUI_XRAY_UNIT=older\nXUI_XRAY_UNIT_EXTRA=keep\n' > "$f"
        chmod 640 "$f"
        _still_env_set "$f" XUI_XRAY_UNIT still-xray || return 1
        _still_env_set "$f" XUI_XRAY_UNIT still-xray || return 1
        count "$f" XUI_XRAY_UNIT=still-xray 1
        hasnt "$f" =old
        hasnt "$f" =older
        has "$f" '# kept comment'
        has "$f" XUI_DB_TYPE=postgres
        has "$f" XUI_XRAY_UNIT_EXTRA=keep
        eq "$(stat -c %a "$f")" 640 "(mode kept)"
        _still_env_set "$f" XUI_XRAY_UNIT "" || return 1
        hasnt "$f" XUI_XRAY_UNIT=
        has "$f" XUI_XRAY_UNIT_EXTRA=keep
        [[ -z "$(find "$R/etc/default" -name '*.tmp.*')" ]] || { echo "temp file left"; return 1; }
    }
    t "$s: _still_env_set replaces the key (also with export/indent), keeps the rest, idempotent, empty drops it" env_set_edit

    # --- _still_memory_dropin: the panel's memory ceiling --------------------
    # mem_case RAM_KB WANT_MAX WANT_HIGH
    mem_case() {
        fresh_root
        load "$script" _still_memory_dropin || return 1
        meminfo "$1" 0
        _still_memory_dropin "$R/etc/default/still-ui" > /dev/null || return 1
        local d="$xui_service/still-ui.service.d/15-still-memory.conf"
        has "$d" "MemoryMax=$2"
        has "$d" "MemoryHigh=$3"
        has "$d" "MemorySwapMax=0"
        count "$d" '[Service]' 1
        called "systemctl daemon-reload"
    }
    t "$s: panel MemoryMax floors at 256 MiB (512 MiB RAM)" mem_case 524288 256M 217M
    t "$s: panel MemoryMax is 35% of RAM (2 GiB RAM -> 716M, high 85%)" mem_case 2097152 716M 608M
    t "$s: panel MemoryMax is 35% of RAM (1 GiB RAM -> 358M)" mem_case 1048576 358M 304M
    t "$s: panel MemoryMax caps at 1 GiB (4 GiB RAM)" mem_case 4194304 1024M 870M
    t "$s: panel MemoryMax caps at 1 GiB (64 GiB RAM)" mem_case 67108864 1024M 870M
    mem_override() {
        fresh_root
        load "$script" _still_memory_dropin || return 1
        local d="$xui_service/still-ui.service.d/15-still-memory.conf" env="$R/etc/default/still-ui"
        echo 'STILL_UI_MEMORY_MAX="800M"' > "$env"
        _still_memory_dropin "$env" > /dev/null || return 1
        has "$d" "MemoryMax=800M"
        hasnt "$d" "MemoryHigh="
        STILL_UI_MEMORY_MAX=2G _still_memory_dropin "$env" > /dev/null || return 1
        has "$d" "MemoryMax=2G"
        STILL_UI_MEMORY_MAX=lots _still_memory_dropin "$env" > "$R/out" || return 1
        has "$R/out" "Ignoring STILL_UI_MEMORY_MAX=lots"
        has "$d" "MemoryMax=716M"
        STILL_UI_MEMORY_MAX=off _still_memory_dropin "$env" > /dev/null || return 1
        [[ ! -e "$d" ]] || { echo "drop-in kept with off"; return 1; }
    }
    t "$s: STILL_UI_MEMORY_MAX from env file or environment, invalid ignored, off removes the drop-in" mem_override
    mem_idempotent() {
        fresh_root
        load "$script" _still_memory_dropin || return 1
        local d="$xui_service/still-ui.service.d/15-still-memory.conf"
        _still_memory_dropin "$R/etc/default/still-ui" > /dev/null || return 1
        cp "$d" "$R/first"
        _still_memory_dropin "$R/etc/default/still-ui" > /dev/null || return 1
        cmp "$R/first" "$d" || return 1
        eq "$(find "$xui_service/still-ui.service.d" -type f | wc -l)" 1 "(files in the drop-in folder)"
    }
    t "$s: panel memory drop-in is rewritten identically on a second run" mem_idempotent
    mem_no_meminfo() {
        fresh_root
        load "$script" _still_memory_dropin || return 1
        echo garbage > "$R/proc/meminfo"
        _still_memory_dropin "$R/etc/default/still-ui" > /dev/null || return 1
        [[ ! -e "$xui_service/still-ui.service.d/15-still-memory.conf" ]] || { echo "drop-in written without MemTotal"; return 1; }
    }
    t "$s: no memory drop-in when MemTotal cannot be read" mem_no_meminfo

    # --- _still_xray_unit: Xray in its own unit --------------------------------
    xray_setup() {
        fresh_root
        load "$script" _still_xray_unit || return 1
        load "$script" _still_env_set || return 1
        load "$script" _still_size_mib || return 1
        mkdir -p "$R/run/systemd/system"
        cp "$UNIT_DIR/still-xray.service" "$xui_folder/still-xray.service"
        printf '#!/bin/sh\n' > "$xui_folder/bin/xray-linux-amd64"
        chmod +x "$xui_folder/bin/xray-linux-amd64"
        printf 'geo' > "$xui_folder/bin/geoip.dat"
        chmod 600 "$xui_folder/bin/geoip.dat"
        panel_stub
    }
    xray_on() {
        xray_setup || return 1
        meminfo 1048576 0
        _still_xray_unit > "$R/out" || { cat "$R/out"; return 1; }
        local u="$xui_service/still-xray.service" d="$xui_service/still-xray.service.d/15-still-memory.conf"
        has "$u" "ExecStart=$xui_folder/bin/xray-linux-amd64 run -c "
        has "$u" "Environment=XRAY_LOCATION_ASSET=$xui_folder/bin"
        hasnt "$u" "@XRAY_"
        has "$u" "User=still-xray"
        eq "$(stat -c %a "$u")" 644
        has "$d" "MemoryMax=512M"
        has "$d" "MemorySwapMax=0"
        has "$d" "Environment=GOMEMLIMIT=435MiB"
        has "$R/etc/default/still-ui" "XUI_XRAY_UNIT=still-xray"
        called "systemctl enable still-xray"
        called "useradd "
        called "x-ui xray-unit-check"
        [[ -d "$R/etc/still-xray/certs" ]] || { echo "no /etc/still-xray/certs"; return 1; }
        eq "$(stat -c %a "$R/etc/still-xray")" 2750
        eq "$(stat -c %a "$xui_folder/bin/geoip.dat")" 644 "(geo files readable by the core)"
    }
    t "$s: auto mode moves Xray to still-xray when xray-unit-check passes" xray_on
    # xray_mem RAM_KB WANT_MAX_MB WANT_GOMEM_MIB [STILL_XRAY_MEMORY_MAX]
    xray_mem() {
        xray_setup || return 1
        meminfo "$1" 0
        STILL_XRAY_MEMORY_MAX="${4:-}" _still_xray_unit > /dev/null || return 1
        local d="$xui_service/still-xray.service.d/15-still-memory.conf"
        has "$d" "MemoryMax=${2}M"
        has "$d" "GOMEMLIMIT=${3}MiB"
    }
    t "$s: still-xray MemoryMax is half the RAM (4 GiB -> 2048M)" xray_mem 4194304 2048 1740
    t "$s: still-xray MemoryMax floors at 256 MiB" xray_mem 262144 256 217
    t "$s: still-xray MemoryMax caps at 4 GiB" xray_mem 33554432 4096 3481
    t "$s: STILL_XRAY_MEMORY_MAX=1G sets 1024M" xray_mem 4194304 1024 870 1G
    t "$s: invalid STILL_XRAY_MEMORY_MAX falls back to half the RAM" xray_mem 4194304 2048 1740 huge
    xray_mem_off() {
        xray_setup || return 1
        STILL_XRAY_MEMORY_MAX=off _still_xray_unit > /dev/null || return 1
        [[ ! -e "$xui_service/still-xray.service.d/15-still-memory.conf" ]] || { echo "memory drop-in kept with off"; return 1; }
        [[ -e "$xui_service/still-xray.service" ]]
    }
    t "$s: STILL_XRAY_MEMORY_MAX=off writes no still-xray memory drop-in" xray_mem_off
    xray_refused() {
        xray_setup || return 1
        echo 'XUI_XRAY_UNIT=still-xray' > "$R/etc/default/still-ui"
        SYSTEMCTL_ENABLED=yes STUB_UNIT_CHECK_RC=3 _still_xray_unit > "$R/out" || return 1
        has "$R/out" "Xray stays inside the panel process"
        has "$R/out" "inbound 3 needs TPROXY"
        [[ ! -e "$xui_service/still-xray.service" ]] || { echo "unit written although the check refused"; return 1; }
        hasnt "$R/etc/default/still-ui" XUI_XRAY_UNIT=
        called "systemctl disable --now still-xray"
    }
    t "$s: a config the unit cannot run keeps Xray in the panel and disables still-xray" xray_refused
    # xray_check_error CURRENT_MODE WANT(on|off): the check itself fails.
    xray_check_error() {
        xray_setup || return 1
        [[ "$1" == on ]] && echo 'XUI_XRAY_UNIT=still-xray' > "$R/etc/default/still-ui"
        STUB_UNIT_CHECK_RC=1 _still_xray_unit > "$R/out" || return 1
        has "$R/out" "keeping the current mode"
        if [[ "$2" == on ]]; then
            [[ -e "$xui_service/still-xray.service" ]] || { echo "still-xray dropped"; return 1; }
            has "$R/etc/default/still-ui" XUI_XRAY_UNIT=still-xray
        else
            [[ ! -e "$xui_service/still-xray.service" ]] || { echo "still-xray turned on"; return 1; }
        fi
    }
    t "$s: a failing xray-unit-check keeps still-xray on where it ran" xray_check_error on on
    t "$s: a failing xray-unit-check keeps Xray in the panel where it ran" xray_check_error off off
    xray_old_release() {
        xray_setup || return 1
        STUB_UNIT_CHECK=old _still_xray_unit > /dev/null || return 1
        [[ ! -e "$xui_service/still-xray.service" ]] || { echo "unit written for a release without the check"; return 1; }
    }
    t "$s: a release without xray-unit-check keeps Xray in the panel" xray_old_release
    xray_forced() {
        xray_setup || return 1
        STILL_XRAY_UNIT=off _still_xray_unit > /dev/null || return 1
        [[ ! -e "$xui_service/still-xray.service" ]] || { echo "STILL_XRAY_UNIT=off ignored"; return 1; }
        not_called "xray-unit-check"
        echo 'STILL_XRAY_UNIT="on"' > "$R/etc/default/still-ui"
        STUB_UNIT_CHECK_RC=3 _still_xray_unit > /dev/null || return 1
        [[ -e "$xui_service/still-xray.service" ]] || { echo "STILL_XRAY_UNIT=on from the env file ignored"; return 1; }
    }
    t "$s: STILL_XRAY_UNIT=off/on (environment or env file) override the check" xray_forced
    xray_bogus() {
        xray_setup || return 1
        STILL_XRAY_UNIT=maybe _still_xray_unit > "$R/out" || return 1
        has "$R/out" "Ignoring STILL_XRAY_UNIT=maybe"
        [[ -e "$xui_service/still-xray.service" ]] || { echo "bogus value did not fall back to auto"; return 1; }
    }
    t "$s: an unknown STILL_XRAY_UNIT value is treated as auto" xray_bogus
    xray_net_admin() {
        xray_setup || return 1
        _still_xray_unit > /dev/null || return 1
        count "$R/calls" "x-ui xray-unit-check -net-admin" 1
        # A template without the capability is checked without it.
        sed -i 's/ CAP_NET_ADMIN$//' "$xui_folder/still-xray.service"
        : > "$R/calls"
        _still_xray_unit > /dev/null || return 1
        called "x-ui xray-unit-check"
        not_called "-net-admin"
        mkdir -p "$xui_service/still-xray.service.d"
        printf '[Service]\nAmbientCapabilities=CAP_NET_ADMIN\n' > "$xui_service/still-xray.service.d/30-local.conf"
        _still_xray_unit > /dev/null || return 1
        called "x-ui xray-unit-check -net-admin"
    }
    t "$s: the CAP_NET_ADMIN of the unit or a local drop-in is passed to xray-unit-check" xray_net_admin
    xray_devices() {
        xray_setup || return 1
        mkdir -p "$xui_service/still-xray.service.d"
        printf '[Service]\nPrivateDevices=no\n' > "$xui_service/still-xray.service.d/30-tun.conf"
        _still_xray_unit > /dev/null || return 1
        called "x-ui xray-unit-check -net-admin -devices"
        : > "$R/calls"
        STUB_UNIT_CHECK=no-devices _still_xray_unit > /dev/null || return 1
        called "x-ui xray-unit-check -net-admin"
        not_called "-devices"
    }
    t "$s: a PrivateDevices=no drop-in is passed as -devices when the check knows it" xray_devices
    xray_no_systemd() {
        xray_setup || return 1
        rmdir "$R/run/systemd/system"
        _still_xray_unit > /dev/null || return 1
        [[ ! -e "$xui_service/still-xray.service" ]] || { echo "unit written without systemd"; return 1; }
        not_called "systemctl"
        [[ ! -e "$R/etc/default/still-ui" ]] || { echo "env file touched without systemd"; return 1; }
    }
    t "$s: without systemd as PID 1 nothing is written" xray_no_systemd
    xray_idempotent() {
        xray_setup || return 1
        _still_xray_unit > /dev/null || return 1
        cp "$xui_service/still-xray.service" "$R/u1"
        cp "$xui_service/still-xray.service.d/15-still-memory.conf" "$R/m1"
        _still_xray_unit > /dev/null || return 1
        cmp "$R/u1" "$xui_service/still-xray.service" || return 1
        cmp "$R/m1" "$xui_service/still-xray.service.d/15-still-memory.conf" || return 1
        count "$R/etc/default/still-ui" XUI_XRAY_UNIT= 1
        count "$R/calls" "useradd " 1
        [[ -z "$(find "$xui_service" -name '*.tmp.*')" ]] || { echo "temp files left"; return 1; }
    }
    t "$s: a second run leaves the unit, its drop-in and the env file the same" xray_idempotent
    xray_env_paths() {
        xray_setup || return 1
        release=rocky
        _still_xray_unit > /dev/null || return 1
        has "$R/etc/sysconfig/still-ui" XUI_XRAY_UNIT=still-xray
        release=arch
        _still_xray_unit > /dev/null || return 1
        has "$R/etc/conf.d/still-ui" XUI_XRAY_UNIT=still-xray
    }
    t "$s: the env file is /etc/sysconfig on RHEL and /etc/conf.d on Arch" xray_env_paths

    # --- systemd hardening helpers --------------------------------------------
    hardening_warn() {
        fresh_root
        load "$script" _xui_systemd_major_version || return 1
        load "$script" _xui_warn_unsupported_hardening || return 1
        eq "$(SYSTEMD_VERSION=255 _xui_warn_unsupported_hardening)" ""
        SYSTEMD_VERSION=219 _xui_warn_unsupported_hardening > "$R/out"
        has "$R/out" "systemd 219 ignores part of the hardening"
        has "$R/out" "ProtectClock (245)"
        has "$R/out" "ReadWriteDirectories="
        SYSTEMD_VERSION=240 _xui_warn_unsupported_hardening > "$R/out"
        has "$R/out" "ProtectHostname (242)"
        hasnt "$R/out" "SystemCallFilter"
        eq "$(SYSTEMD_VERSION=garbage _xui_systemd_major_version)" 0
    }
    t "$s: older systemd versions are named with the hardening they ignore" hardening_warn
done

# --- _xui_service_write_paths_dropin (install.sh and update.sh resolve the env
# file differently, the behaviour is the same) ---------------------------------
for script in "$INSTALL_SH" "$UPDATE_SH"; do
    s=$(basename "$script")
    write_paths() {
        fresh_root
        load "$script" _xui_service_write_paths_dropin || return 1
        load "$script" _xui_systemd_major_version || return 1
        load "$UPDATE_SH" xui_env_file_path || return 1
        local d="$xui_service/still-ui.service.d/10-xui-sandbox.conf"
        (_xui_service_write_paths_dropin) || return 1
        has "$d" "ReadWritePaths=-$R/etc/still-ui -$R/var/log/still-ui -$xui_folder/bin -$xui_folder -$R/etc/still-xray"
        has "$d" "SystemCallFilter=@system-service"
        printf 'XUI_DB_FOLDER="/srv/panel db"\nXUI_LOG_FOLDER=/srv/log%%s\nXUI_BIN_FOLDER=/srv/bin\n' > "$R/etc/default/still-ui"
        (_xui_service_write_paths_dropin) 2> "$R/err" || return 1
        has "$d" "-/srv/log%%s"
        has "$d" "-/srv/bin"
        hasnt "$d" "panel db"
        has "$R/err" "/srv/panel db"
        (SYSTEMD_VERSION=238 _xui_service_write_paths_dropin) 2> /dev/null || return 1
        hasnt "$d" SystemCallFilter
        count "$d" "ReadWritePaths=" 1
    }
    t "$s: the sandbox drop-in lists the folders from the env file (escaped, no whitespace paths, filter only on systemd >= 239)" write_paths
    unit_atomic() {
        fresh_root
        load "$script" _install_xui_service_unit || return 1
        _xui_service_write_paths_dropin() { log_call "paths-dropin"; }
        _still_memory_dropin() { log_call "memory-dropin"; }
        _xui_warn_unsupported_hardening() { :; }
        curl_bin=false
        local dest="$xui_service/still-ui.service"
        echo old > "$dest"
        : > "$R/empty"
        _install_xui_service_unit "$R/empty" false && { echo "an empty unit was installed"; return 1; }
        _install_xui_service_unit "$R/missing" false && { echo "a missing unit was installed"; return 1; }
        eq "$(cat "$dest")" old "(unit replaced by a failed copy)"
        not_called paths-dropin
        cp "$UNIT_DIR/x-ui.service.debian" "$R/new.service"
        _install_xui_service_unit "$R/new.service" false || return 1
        cmp "$UNIT_DIR/x-ui.service.debian" "$dest" || return 1
        called paths-dropin
        called memory-dropin
        [[ -z "$(find "$xui_service" -name '*.tmp.*')" ]] || { echo "temp file left"; return 1; }
    }
    t "$s: the panel unit is replaced atomically, never by an empty or missing file" unit_atomic
done

# ==============================================================================
# install.sh
# ==============================================================================

swap_setup() {
    fresh_root
    load "$INSTALL_SH" _still_swap_check || return 1
}
# swap_none RAM_KB SWAP_KB: nothing is offered or created.
swap_none() {
    swap_setup || return 1
    meminfo "$1" "$2"
    STILL_UI_CREATE_SWAP=yes _still_swap_check > "$R/out"
    [[ ! -e "$R/swapfile" ]] || { echo "swap file created"; return 1; }
    not_called fallocate
    eq "$(cat "$R/out")" "" "(output)"
}
t "install.sh: no swap offer with swap already on" swap_none 1048576 1024
t "install.sh: no swap offer above 2 GiB RAM (+256 MiB slack)" swap_none $((2 * 1024 * 1024 + 262145)) 0
swap_yes() {
    swap_setup || return 1
    meminfo $((2 * 1024 * 1024)) 0
    STILL_UI_CREATE_SWAP=yes _still_swap_check > "$R/out"
    [[ -e "$R/swapfile" ]] || { echo "no swap file"; cat "$R/out"; return 1; }
    eq "$(stat -c %a "$R/swapfile")" 600
    called "fallocate -l 2G $R/swapfile"
    called "mkswap $R/swapfile"
    called "swapon $R/swapfile"
    count "$R/etc/fstab" "$R/swapfile none swap sw 0 0" 1
    has "$R/etc/sysctl.d/60-still-ui-swappiness.conf" "vm.swappiness=10"
    has "$R/out" "Swap enabled"
    # Already in fstab: no second line.
    rm -f "$R/swapfile"
    STILL_UI_CREATE_SWAP=yes _still_swap_check > /dev/null
    count "$R/etc/fstab" "$R/swapfile" 1
}
t "install.sh: STILL_UI_CREATE_SWAP=yes creates a 2 GiB swap file (<= 2 GiB RAM, no swap), fstab once" swap_yes
swap_default_no() {
    swap_setup || return 1
    _still_swap_check > "$R/out"
    has "$R/out" "no swap"
    has "$R/out" "Swap not created"
    [[ ! -e "$R/swapfile" ]] || { echo "swap created without consent"; return 1; }
}
t "install.sh: non-interactive install only warns about missing swap" swap_default_no
swap_interactive() {
    swap_setup || return 1
    NONINTERACTIVE=0
    _still_swap_check <<< "n" > /dev/null
    [[ ! -e "$R/swapfile" ]] || { echo "'n' created swap"; return 1; }
    _still_swap_check <<< "y" > /dev/null
    [[ -e "$R/swapfile" ]] || { echo "'y' created no swap"; return 1; }
}
t "install.sh: the swap prompt creates swap on y only" swap_interactive
swap_container() {
    swap_setup || return 1
    IN_CONTAINER=1 STILL_UI_CREATE_SWAP=yes _still_swap_check > "$R/out"
    has "$R/out" "this is a container"
    [[ ! -e "$R/swapfile" ]] || { echo "swap created in a container"; return 1; }
}
t "install.sh: no swap file inside a container" swap_container
swap_existing() {
    swap_setup || return 1
    echo keep > "$R/swapfile"
    STILL_UI_CREATE_SWAP=yes _still_swap_check > "$R/out"
    eq "$(cat "$R/swapfile")" keep "(an inactive /swapfile was overwritten)"
    not_called mkswap
}
t "install.sh: an existing inactive /swapfile is left alone" swap_existing
swap_failures() {
    swap_setup || return 1
    FALLOCATE_FAIL=1 STILL_UI_CREATE_SWAP=yes _still_swap_check > /dev/null
    called "dd if=/dev/zero of=$R/swapfile bs=1M count=2048"
    [[ -e "$R/swapfile" ]] || { echo "dd fallback did not create the file"; return 1; }
    swap_setup || return 1
    FALLOCATE_FAIL=1 DD_FAIL=1 STILL_UI_CREATE_SWAP=yes _still_swap_check > "$R/out"
    has "$R/out" "Could not allocate"
    [[ ! -e "$R/swapfile" ]] || { echo "half-written swap file left"; return 1; }
    swap_setup || return 1
    SWAPON_FAIL=1 STILL_UI_CREATE_SWAP=yes _still_swap_check > "$R/out"
    has "$R/out" "Could not enable"
    [[ ! -e "$R/swapfile" ]] || { echo "unusable swap file left"; return 1; }
    hasnt "$R/etc/fstab" swapfile
}
t "install.sh: swap falls back to dd, cleans up when allocation or swapon fails" swap_failures

import_setup() {
    fresh_root
    load "$INSTALL_SH" _still_import_3xui || return 1
    mkdir -p "$R/etc/x-ui"
    echo db > "$R/etc/x-ui/x-ui.db"
    echo wal > "$R/etc/x-ui/x-ui.db-wal"
    echo key > "$R/etc/x-ui/node_token_key.json"
    printf 'XUI_DB_TYPE=sqlite\nXUI_DB_FOLDER=/etc/x-ui\nexport XUI_LOG_FOLDER=/var/log/x-ui\n' > "$R/etc/default/x-ui"
    printf '[Service]\nExecStart=/usr/local/x-ui/x-ui\n' > "$xui_service/x-ui.service"
    mkdir -p "$xui_service/x-ui.service.d"
    printf '[Service]\nMemoryMax=450M\n' > "$xui_service/x-ui.service.d/50-local.conf"
    mkdir -p "$R/etc/systemd/system.control/x-ui.service.d"
    printf '[Service]\nMemoryMax=650M\n' > "$R/etc/systemd/system.control/x-ui.service.d/50-MemoryMax.conf"
    # 3x-ui's acme.sh certificate for the server IP, reloadcmd base64-encoded as acme.sh 3.x writes it.
    export STILL_UI_ACME_HOME="$R/acme"
    mkdir -p "$R/acme/203.0.113.5_ecc"
    printf "Le_Domain='203.0.113.5'\nLe_ReloadCmd='__ACME_BASE64__START_%s__ACME_BASE64__END_'\nLe_NextRenewTime='1'\n" \
        "$(printf '%s' 'systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true' | base64 -w0)" \
        > "$R/acme/203.0.113.5_ecc/203.0.113.5.conf"
    cp "$repo_root/migrate.sh" "$xui_folder/migrate.sh"
}
import_yes() {
    import_setup || return 1
    STILL_UI_IMPORT_3XUI=yes _still_import_3xui > /dev/null
    eq "$(cat "$R/etc/still-ui/x-ui.db")" db
    eq "$(cat "$R/etc/still-ui/x-ui.db-wal")" wal
    eq "$(cat "$R/etc/still-ui/node_token_key.json")" key
    has "$R/etc/default/still-ui" XUI_DB_TYPE=sqlite
    hasnt "$R/etc/default/still-ui" XUI_DB_FOLDER
    hasnt "$R/etc/default/still-ui" XUI_LOG_FOLDER
    eq "$(stat -c %a "$R/etc/default/still-ui")" 600 "(env file mode)"
    eq "$(cat "$R/etc/x-ui/x-ui.db")" db "(3x-ui's database changed)"
    called "systemctl stop x-ui"
    called "systemctl disable x-ui"
}
t "install.sh: STILL_UI_IMPORT_3XUI=yes copies the 3x-ui database and stops x-ui" import_yes
# After the import 3x-ui's acme.sh renewal ran "systemctl restart x-ui": 3x-ui came up next to
# still-ui on the same ports, while still-ui's own certificate expired.
import_retires_xui() {
    import_setup || return 1
    STILL_UI_IMPORT_3XUI=yes _still_import_3xui > "$R/out"
    [[ ! -e "$xui_service/x-ui.service" && ! -e "$xui_service/x-ui.service.d" ]] || afail "x-ui's unit can still start"
    local kept="$R/root/still-ui-3x-ui-retired/files"
    has "$kept$xui_service/x-ui.service" "ExecStart=/usr/local/x-ui/x-ui"
    has "$kept$xui_service/x-ui.service.d/50-local.conf" "MemoryMax=450M"
    # Two drop-in folders of the same name (an operator's and systemctl set-property's) both survive.
    has "$kept$R/etc/systemd/system.control/x-ui.service.d/50-MemoryMax.conf" "MemoryMax=650M"
    called "systemctl daemon-reload"
    local conf="$R/acme/203.0.113.5_ecc/203.0.113.5.conf" cmd
    cmd=$(sed -n "s/^Le_ReloadCmd='__ACME_BASE64__START_\(.*\)__ACME_BASE64__END_'$/\1/p" "$conf" | base64 -d)
    eq "$cmd" "systemctl restart still-ui 2>/dev/null || rc-service still-ui restart 2>/dev/null || true" "(reload command)"
    has "$conf" "Le_NextRenewTime='1'"
    count "$R/root/still-ui-3x-ui-retired/acme-hooks.tsv" "Le_ReloadCmd" 1
}
t "install.sh: the 3x-ui import retires x-ui's unit and points acme.sh renewals at still-ui" import_retires_xui
# The way back from an import: with the hooks left on still-ui, a renewal started still-ui next
# to 3x-ui, or (still-ui gone) never restarted 3x-ui on a 6-day certificate.
import_way_back() {
    import_setup || return 1
    STILL_UI_IMPORT_3XUI=yes _still_import_3xui > "$R/out"
    local kept="$R/root/still-ui-3x-ui-retired"
    has "$R/out" "bash $kept/migrate.sh restore-import $kept"
    # The retired copy of migrate.sh runs on its own (still-ui may be gone); systemctl is the stub.
    rm -f "$xui_folder/migrate.sh"
    export R
    export -f systemctl log_call
    bash "$kept/migrate.sh" restore-import "$kept" > "$R/out" 2>&1 || afail "restore-import failed: $(cat "$R/out")"
    has "$xui_service/x-ui.service" "ExecStart=/usr/local/x-ui/x-ui"
    has "$xui_service/x-ui.service.d/50-local.conf" "MemoryMax=450M"
    has "$R/etc/systemd/system.control/x-ui.service.d/50-MemoryMax.conf" "MemoryMax=650M"
    local conf="$R/acme/203.0.113.5_ecc/203.0.113.5.conf" cmd
    cmd=$(sed -n "s/^Le_ReloadCmd='__ACME_BASE64__START_\(.*\)__ACME_BASE64__END_'$/\1/p" "$conf" | base64 -d)
    eq "$cmd" "systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true" "(reload command back)"
    called "systemctl daemon-reload"
}
t "install.sh: the retired migrate.sh puts back x-ui's unit, drop-ins and acme.sh hooks of a 3x-ui import" import_way_back
import_no() {
    import_setup || return 1
    _still_import_3xui > /dev/null
    [[ ! -e "$R/etc/still-ui/x-ui.db" ]] || { echo "imported without consent"; return 1; }
    not_called "systemctl stop x-ui"
    mkdir -p "$R/etc/still-ui"
    echo mine > "$R/etc/still-ui/x-ui.db"
    STILL_UI_IMPORT_3XUI=yes _still_import_3xui > /dev/null
    eq "$(cat "$R/etc/still-ui/x-ui.db")" mine "(existing still-ui database overwritten)"
}
t "install.sh: no 3x-ui import without consent, never over an existing still-ui database" import_no

result_file() {
    fresh_root
    load "$INSTALL_SH" write_install_result || return 1
    local u="o'neil" p='pa ss$(touch '"$R"'/pwned)`id`"x' f="$R/etc/still-ui/install-result.env"
    write_install_result "$u" "$p" 2053 base/path https 192.0.2.1 tok sqlite > /dev/null || return 1
    eq "$(stat -c %a "$f")" 600
    eq "$(stat -c %a "$R/etc/still-ui")" 700
    (
        # shellcheck disable=SC1090
        . "$f"
        eq "$XUI_USERNAME" "$u"
        eq "$XUI_PASSWORD" "$p"
        eq "$XUI_PANEL_PORT" 2053
        eq "$XUI_ACCESS_URL" "https://192.0.2.1:2053/base/path"
        eq "$XUI_API_TOKEN" tok
        eq "$XUI_DB_TYPE" sqlite
    ) || return 1
    [[ ! -e "$R/pwned" ]] || { echo "sourcing the result file ran a command"; return 1; }
}
t "install.sh: install-result.env is 0600 and safely source-able with any password" result_file

# --- install_x-ui: what it refuses before it changes anything ------------------
# A fake existing install, a release archive and a curl stub that serves it.
make_archive() { # DIR [omit-file]
    local pkg="$1/pkg/still-ui"
    mkdir -p "$pkg/bin"
    printf '#!/bin/sh\necho 9.9.9\n' > "$pkg/x-ui"
    printf 'menu new\n' > "$pkg/x-ui.sh"
    printf 'xray new\n' > "$pkg/bin/xray-linux-amd64"
    cp "$UNIT_DIR/x-ui.service.debian" "$pkg/"
    [[ -z "${2:-}" ]] || rm -f "$pkg/$2"
    tar -C "$1/pkg" -czf "$1/still-ui-linux-amd64.tar.gz" still-ui
}
install_setup() {
    fresh_root
    load "$INSTALL_SH" install_x-ui || return 1
    load "$INSTALL_SH" verify_release_checksum || return 1
    load "$INSTALL_SH" require_archive_files || return 1
    arch() { echo amd64; }
    resolve_latest_tag() { echo v9.9.9; }
    _still_import_3xui() { log_call import; }
    config_after_install() { log_call config_after_install; }
    _install_xui_service_unit() { log_call install-unit; }
    _still_xray_unit() { log_call xray-unit; }
    setup_fail2ban() { :; }
    # The existing installation that must survive a refused install.
    echo "old panel" > "$xui_folder/x-ui"
    echo "old xray" > "$xui_folder/bin/xray-linux-amd64"
    echo "custom" > "$xui_folder/bin/custom.dat"
    echo "old menu" > "$R/usr/bin/still-ui"
    mkdir -p "$R/srv"
    make_archive "$R/srv" "${OMIT:-}"
    SERVED="$R/srv/still-ui-linux-amd64.tar.gz"
}
# curl -o FILE URL (archive) and -o FILE -w '%{http_code}' URL.sha256.
curl() {
    local out="" url="" prev=""
    for a in "$@"; do
        [[ "$prev" == -o ]] && out="$a"
        prev="$a"
        url="$a"
    done
    log_call "curl $url"
    if [[ "$url" == *.sha256 ]]; then
        case "${SIDECAR:-ok}" in
            ok) printf '%s  still-ui-linux-amd64.tar.gz\n' "$(sha256sum "$SERVED" | awk '{print $1}')" > "$out"; printf 200 ;;
            wrong) printf '%064d  still-ui-linux-amd64.tar.gz\n' 0 > "$out"; printf 200 ;;
            upper) sha256sum "$SERVED" | awk '{print toupper($1)}' > "$out"; printf 200 ;;
            empty) : > "$out"; printf 200 ;;
            missing) printf 'Not Found' > "$out"; printf 404 ;;
        esac
        return 0
    fi
    cp "$SERVED" "$out"
    [[ "${TAMPER:-0}" == 0 ]] || printf 'x' >> "$out"
}
unchanged() {
    eq "$(cat "$xui_folder/x-ui")" "old panel" "(panel binary)" &&
        eq "$(cat "$xui_folder/bin/xray-linux-amd64")" "old xray" "(xray)" &&
        eq "$(cat "$xui_folder/bin/custom.dat")" "custom" "(custom bin file)" &&
        eq "$(cat "$R/usr/bin/still-ui")" "old menu" "(menu)" &&
        not_called "systemctl stop" && not_called "pkill" && not_called config_after_install &&
        [[ -z "$(find "$R/usr/bin" -name 'still-ui-temp.*')" ]]
}
# refused WHY [VAR=VALUE...] -- [ARGS]: install_x-ui exits non-zero and changes nothing.
refused() {
    local why="$1" a
    shift
    while (($#)) && [[ "$1" != -- ]]; do export "${1?}"; shift; done
    shift
    install_setup || return 1
    (install_x-ui "$@") > "$R/out" 2>&1 && { echo "install_x-ui succeeded:"; cat "$R/out"; return 1; }
    has "$R/out" "$why"
    unchanged
}
t "install.sh: a tampered archive is refused before anything changes" refused "Checksum mismatch" TAMPER=1 -- v9.9.9
t "install.sh: a wrong .sha256 is refused before anything changes" refused "Checksum mismatch" SIDECAR=wrong -- v9.9.9
t "install.sh: an empty .sha256 is refused" refused "Checksum mismatch" SIDECAR=empty -- v9.9.9
t "install.sh: an upper-case (non-sha256sum) sum is refused" refused "Checksum mismatch" SIDECAR=upper --
t "install.sh: a release without .sha256 is refused" refused "No checksum is published" SIDECAR=missing --
t "install.sh: an archive without the systemd unit is refused" refused "has no still-ui/x-ui.service.debian" OMIT=x-ui.service.debian -- v9.9.9
t "install.sh: an archive without x-ui.sh is refused" refused "has no still-ui/x-ui.sh" OMIT=x-ui.sh -- v9.9.9
t "install.sh: the dev channel is refused" refused "no dev build" -- dev
t "install.sh: a branch name is refused" refused "is not a release tag" -- main
t "install.sh: a tag with a shell payload is refused" refused "is not a release tag" -- 'v1.0.0;id'
t "install.sh: releases older than v1.0.0 are refused" refused "at least v1.0.0" -- v0.9.9
t "install.sh: a missing STILL_UI_ARCHIVE is refused" refused "does not exist or is empty" "STILL_UI_ARCHIVE=/nonexistent/a.tar.gz" --
not_still_ui_archive() {
    install_setup || return 1
    mkdir -p "$R/other/foo"
    echo x > "$R/other/foo/x-ui"
    tar -C "$R/other" -czf "$R/other.tar.gz" foo
    (STILL_UI_ARCHIVE="$R/other.tar.gz" install_x-ui) > "$R/out" 2>&1 && return 1
    has "$R/out" "is not a still-ui release archive"
    unchanged
}
t "install.sh: a STILL_UI_ARCHIVE that is not a still-ui archive is refused" not_still_ui_archive
bad_tag_no_download() {
    install_setup || return 1
    (install_x-ui main) > /dev/null 2>&1
    not_called "curl"
}
t "install.sh: an invalid tag downloads nothing" bad_tag_no_download
install_ok() {
    install_setup || return 1
    (install_x-ui v9.9.9) > "$R/out" 2>&1 || { cat "$R/out"; return 1; }
    has "$R/out" "Checksum verified"
    eq "$(cat "$R/usr/bin/still-ui")" "menu new" "(menu from the archive)"
    eq "$(cat "$xui_folder/bin/xray-linux-amd64")" "xray new"
    eq "$(cat "$xui_folder/bin/custom.dat")" custom "(custom bin/ file kept)"
    [[ -x "$xui_folder/x-ui" ]] || { echo "panel not executable"; return 1; }
    called "systemctl stop still-ui"
    called "curl https://github.com/Dv2Corn/still-ui/releases/download/v9.9.9/still-ui-linux-amd64.tar.gz"
    called install-unit
    called xray-unit
    called "systemctl enable still-ui"
    called "systemctl start still-ui"
    # Order: verified download, then stop, then config.
    local n_sum n_stop n_cfg
    n_sum=$(grep -n 'curl .*\.sha256' "$R/calls" | head -1 | cut -d: -f1)
    n_stop=$(grep -n 'systemctl stop still-ui' "$R/calls" | head -1 | cut -d: -f1)
    n_cfg=$(grep -n config_after_install "$R/calls" | head -1 | cut -d: -f1)
    ((n_sum < n_stop && n_stop < n_cfg)) || { echo "wrong order"; cat "$R/calls"; return 1; }
    [[ -z "$(find "$R/usr/local" -maxdepth 1 -name 'x-ui-bin-backup.*')" ]] || { echo "bin backup left"; return 1; }
}
t "install.sh: a verified install replaces binary, menu and xray, keeps custom bin/ files" install_ok

# ==============================================================================
# update.sh
# ==============================================================================

stage_case() { # OMIT WANT(ok|fail)
    fresh_root
    load "$UPDATE_SH" _stage_update_archive || return 1
    load "$UPDATE_SH" _fail || return 1
    make_archive "$R/srv" "$1"
    (_stage_update_archive "$R/srv/still-ui-linux-amd64.tar.gz"; echo "stage=${update_stage_dir}") > "$R/out" 2>&1
    local rc=$?
    if [[ "$2" == ok ]]; then
        eq "$rc" 0 || { cat "$R/out"; return 1; }
        has "$R/out" "stage="
        [[ -z "$(find "$R/usr/local" -maxdepth 1 -name '.still-ui-update.*')" ]] || { echo "stage folder left"; return 1; }
    else
        eq "$rc" 2 "(exit status)" || return 1
        has "$R/out" "the running panel was not touched"
    fi
}
t "update.sh: a complete archive stages cleanly" stage_case "" ok
t "update.sh: an archive without x-ui is refused before the stop" stage_case x-ui fail
t "update.sh: an archive without x-ui.sh is refused before the stop" stage_case x-ui.sh fail
t "update.sh: an archive without the unit is refused before the stop" stage_case x-ui.service.debian fail
stage_garbage() {
    fresh_root
    load "$UPDATE_SH" _stage_update_archive || return 1
    load "$UPDATE_SH" _fail || return 1
    echo "not gzip" > "$R/a.tar.gz"
    (_stage_update_archive "$R/a.tar.gz") > "$R/out" 2>&1 && return 1
    has "$R/out" "Failed to extract"
    [[ ! -e "$R/a.tar.gz" ]] || { echo "broken archive kept"; return 1; }
}
t "update.sh: a corrupt archive is refused and removed" stage_garbage
rollback() {
    fresh_root
    for f in _backup_current_install _update_restore_and_start _fail; do load "$UPDATE_SH" "$f" || return 1; done
    arch() { echo amd64; }
    local f
    echo "panel 1.5.0" > "$xui_folder/x-ui"
    echo "menu 1.5.0" > "$xui_folder/x-ui.sh"
    echo "xray 1.5.0" > "$xui_folder/bin/xray-linux-amd64"
    echo "cli 1.5.0" > "$R/usr/bin/still-ui"
    echo "unit 1.5.0" > "$xui_service/still-ui.service"
    echo "geo" > "$xui_folder/bin/geoip.dat"
    update_backup_paths=()
    update_backup_dir=""
    _backup_current_install || return 1
    # The update then removes and replaces files, and fails.
    rm -f "$xui_folder/x-ui"
    echo "menu broken" > "$xui_folder/x-ui.sh"
    echo "xray broken" > "$xui_folder/bin/xray-linux-amd64"
    echo "cli broken" > "$R/usr/bin/still-ui"
    : > "$xui_service/still-ui.service"
    update_stopped=1 update_rollback_files=1
    _update_restore_and_start > /dev/null
    eq "$(cat "$xui_folder/x-ui")" "panel 1.5.0"
    eq "$(cat "$xui_folder/x-ui.sh")" "menu 1.5.0"
    eq "$(cat "$xui_folder/bin/xray-linux-amd64")" "xray 1.5.0"
    eq "$(cat "$R/usr/bin/still-ui")" "cli 1.5.0"
    eq "$(cat "$xui_service/still-ui.service")" "unit 1.5.0"
    eq "$(cat "$xui_folder/bin/geoip.dat")" geo
    called "systemctl start still-ui"
}
t "update.sh: a failed update puts back binary, menu, xray, CLI and unit and starts the panel" rollback
rollback_after_migrate() {
    fresh_root
    for f in _backup_current_install _update_restore_and_start _fail; do load "$UPDATE_SH" "$f" || return 1; done
    arch() { echo amd64; }
    echo "panel 1.5.0" > "$xui_folder/x-ui"
    echo "unit 1.5.0" > "$xui_service/still-ui.service"
    update_backup_paths=() update_backup_dir=""
    _backup_current_install || return 1
    echo "panel 1.5.1" > "$xui_folder/x-ui"
    echo "unit 1.5.1" > "$xui_service/still-ui.service"
    # The database is migrated: the new files stay, the panel starts again.
    update_stopped=1 update_rollback_files=0
    _update_restore_and_start > /dev/null
    eq "$(cat "$xui_folder/x-ui")" "panel 1.5.1"
    eq "$(cat "$xui_service/still-ui.service")" "unit 1.5.1"
    called "systemctl start still-ui"
    : > "$R/calls"
    update_stopped=0
    _update_restore_and_start > /dev/null
    not_called systemctl
}
t "update.sh: after the database migration a failure only restarts the panel; before the stop nothing runs" rollback_after_migrate
update_env_path() {
    fresh_root
    load "$UPDATE_SH" xui_env_file_path || return 1
    local r want
    for r in ubuntu:/etc/default debian:/etc/default armbian:/etc/default arch:/etc/conf.d alpine:/etc/conf.d \
        manjaro:/etc/conf.d rocky:/etc/sysconfig almalinux:/etc/sysconfig fedora:/etc/sysconfig; do
        release=${r%%:*} want="$R${r#*:}/still-ui"
        eq "$(xui_env_file_path)" "$want" "($release)" || return 1
    done
}
t "update.sh: the env file path per distribution" update_env_path
writable() {
    fresh_root
    load "$UPDATE_SH" require_writable_update_paths || return 1
    load "$UPDATE_SH" _fail || return 1
    (require_writable_update_paths) > /dev/null || return 1
    [[ -z "$(find "$R/usr" -name '.x-ui-write-test.*')" ]] || { echo "probe left"; return 1; }
    chmod 555 "$R/usr/local"
    (require_writable_update_paths) > "$R/out" 2>&1
    local rc=$?
    chmod 755 "$R/usr/local"
    if [[ $(command id -u) == 0 ]]; then return 0; fi # root writes anyway
    eq "$rc" 2 || return 1
    has "$R/out" "is not writable"
}
t "update.sh: a read-only install folder stops the update before anything changes" writable

# ==============================================================================
# x-ui.sh (the still-ui menu)
# ==============================================================================

xui_setup() {
    fresh_root
    for f in uninstall delete_script xui_env_file_path confirm postgresql_installed _still_stdin_is_tty; do load "$XUI_SH" "$f" || return 1; done
    still_retired_3xui="$R/root/still-ui-3x-ui-retired"
    purge_postgresql() { log_call purge_postgresql; }
    show_menu() { log_call show_menu; }
    LOGE() { echo "[ERR] $*"; }
    mkdir -p "$R/etc/still-ui/backups" "$xui_service/still-ui.service.d" "$xui_service/still-xray.service.d" "$R/etc/still-xray" "$R/var/log/still-xray"
    echo db > "$R/etc/still-ui/x-ui.db"
    echo creds > "$R/etc/still-ui/install-result.env"
    echo dump > "$R/etc/still-ui/backups/still-ui-1.tar.gz"
    touch "$xui_service/still-ui.service" "$xui_service/still-xray.service" "$xui_service/still-ui.service.d/10-xui-sandbox.conf"
    touch "$R/user-still-xray"
    echo 'XUI_XRAY_UNIT=still-xray' > "$R/etc/default/still-ui"
    # uninstall ends in delete_script, which deletes "$0" (the installed menu):
    # here that would be this test, so it deletes the scratch copy instead
    # (delete_script itself is tested on its own below).
    grep -qx '    rm "\$0" # Remove the script file itself' "$XUI_SH" || { echo "delete_script no longer removes \$0"; return 1; }
    cp "$XUI_SH" "$R/usr/bin/still-ui"
    delete_script() { rm -f "$R/usr/bin/still-ui"; exit "${1:-0}"; }
    TTY=yes
    _still_stdin_is_tty() { [[ $TTY == yes ]]; }
}
# un STDIN ARGS...: runs the uninstall, sets st to its exit status.
un() {
    local input="$1"
    shift
    (uninstall "$@" <<< "$input") > "$R/out" 2>&1
    st=$?
}
program_gone() {
    called "systemctl stop still-ui"
    called "systemctl disable still-ui"
    called "systemctl disable --now still-xray"
    called "userdel still-xray"
    called "systemctl daemon-reload"
    called "nft delete table inet still_egress_filter"
    called "nft delete table inet still_torrent_block"
    has "$R/out" "releases/latest/download/install.sh"
    hasnt "$R/out" "raw.githubusercontent.com"
    local p
    for p in "$xui_service/still-ui.service" "$xui_service/still-ui.service.d" "$xui_service/still-xray.service" \
        "$xui_service/still-xray.service.d" "$R/etc/still-xray" "$R/var/log/still-xray" "$xui_folder" "$R/usr/bin/still-ui"; do
        [[ ! -e "$p" ]] || afail "left behind: $p"
    done
}
data_kept() {
    eq "$(cat "$R/etc/still-ui/x-ui.db" 2> /dev/null)" db "(database)"
    eq "$(cat "$R/etc/still-ui/install-result.env" 2> /dev/null)" creds "(install-result.env)"
    eq "$(cat "$R/etc/still-ui/backups/still-ui-1.tar.gz" 2> /dev/null)" dump "(backups)"
    has "$R/etc/default/still-ui" XUI_XRAY_UNIT=still-xray
    has "$R/out" "The data is kept"
    has "$R/out" "rm -rf $R/etc/still-ui $R/etc/default/still-ui"
}
data_gone() {
    [[ ! -e "$R/etc/still-ui" ]] || afail "data kept: $(find "$R/etc/still-ui")"
    [[ ! -e "$R/etc/default/still-ui" ]] || afail "env file kept"
    has "$R/out" "the data is deleted too"
}
uninstall_default_keeps() {
    xui_setup || return 1
    un $'y\n' 0 # yes to the uninstall, Enter at the data question
    eq "$st" 0 "(exit status)"
    program_gone
    data_kept
    # read -p shows the question only on a terminal; the text is the agreed one.
    has "$XUI_SH" 'confirm "Also delete all data (database /etc/still-ui, clients, settings)?" "n"'
    not_called purge_postgresql
}
t "x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)" uninstall_default_keeps
# retired_setup: what a 3x-ui import left, with a migrate.sh that records being run.
retired_setup() {
    mkdir -p "$still_retired_3xui/files$xui_service"
    printf '[Service]\n' > "$still_retired_3xui/files$xui_service/x-ui.service"
    printf '#!/bin/bash\necho "$*" > "%s/restore-run"\n' "$R" > "$still_retired_3xui/migrate.sh"
}
# Kept data is for a reinstall, which never imports 3x-ui again: putting 3x-ui's unit and hooks
# back here would start it next to the reinstalled still-ui at the next renewal.
uninstall_keep_leaves_3xui_retired() {
    xui_setup || return 1
    retired_setup
    un $'y\n' 0
    eq "$st" 0 "(exit status)"
    [[ ! -e "$R/restore-run" ]] || afail "3x-ui restored although still-ui's data is kept"
    has "$R/out" "bash $still_retired_3xui/migrate.sh restore-import $still_retired_3xui"
}
t "x-ui.sh: uninstall keeping the data leaves an imported 3x-ui retired and says how to go back" uninstall_keep_leaves_3xui_retired
uninstall_unretires() {
    xui_setup || return 1
    retired_setup
    un '' 0 --purge
    eq "$st" 0 "(exit status)"
    eq "$(cat "$R/restore-run" 2> /dev/null)" "restore-import $still_retired_3xui" "(migrate.sh call)"
}
t "x-ui.sh: uninstall --purge puts back what a 3x-ui import retired" uninstall_unretires
uninstall_answer_no_keeps() {
    xui_setup || return 1
    un $'y\nn\n' 0
    eq "$st" 0 "(exit status)"
    program_gone
    data_kept
}
t "x-ui.sh: 'n' at the data question keeps the data" uninstall_answer_no_keeps
uninstall_answer_yes_purges() {
    xui_setup || return 1
    un $'y\ny\n' 0
    eq "$st" 0 "(exit status)"
    program_gone
    data_gone
}
t "x-ui.sh: 'y' at the data question deletes the data too (exit 0)" uninstall_answer_yes_purges
uninstall_no_tty_keeps() {
    xui_setup || return 1
    TTY=no
    un $'y\ny\ny\n' 0 # e.g. `yes | still-ui uninstall`
    eq "$st" 0 "(exit status)"
    program_gone
    data_kept
}
t "x-ui.sh: without a terminal and without a flag uninstall never deletes the data" uninstall_no_tty_keeps
uninstall_flags() {
    xui_setup || return 1
    TTY=no
    un '' 0 --keep-data
    eq "$st" 0 "(--keep-data exit status)"
    program_gone
    data_kept
    hasnt "$R/out" "Are you sure"
    xui_setup || return 1
    TTY=no
    un '' 0 --purge
    eq "$st" 0 "(--purge exit status)"
    program_gone
    data_gone
    hasnt "$R/out" "Are you sure"
}
t "x-ui.sh: 'uninstall --keep-data' and '--purge' run without questions" uninstall_flags
uninstall_bad_flag() {
    xui_setup || return 1
    un $'y\ny\n' 0 --all
    eq "$st" 2 "(exit status)"
    has "$R/out" "Unknown option --all"
    not_called systemctl
    [[ -e "$R/etc/still-ui/x-ui.db" && -e "$xui_folder" && -e "$R/usr/bin/still-ui" ]] || afail "removed with an unknown option"
}
t "x-ui.sh: an unknown uninstall option changes nothing (exit 2)" uninstall_bad_flag
uninstall_no() {
    xui_setup || return 1
    un 'n' 0
    eq "$st" 0
    not_called systemctl
    [[ -e "$R/etc/still-ui/x-ui.db" && -e "$xui_service/still-ui.service" && -e "$R/usr/bin/still-ui" ]] || { echo "removed after no"; return 1; }
    (uninstall 0 < /dev/null) > /dev/null 2>&1 || return 1
    [[ -e "$R/etc/still-ui/x-ui.db" && -e "$xui_folder" ]] || { echo "removed on EOF"; return 1; }
    (uninstall <<< "n") > /dev/null 2>&1
    called show_menu
}
t "x-ui.sh: uninstall defaults to no (answer n or no input) and then changes nothing" uninstall_no
uninstall_pg() {
    xui_setup || return 1
    printf 'XUI_DB_TYPE=postgres\nXUI_DB_DSN=postgres://u:p@127.0.0.1/db\n' > "$R/etc/default/still-ui"
    postgresql_installed() { return 0; }
    un '' 0 --keep-data
    not_called purge_postgresql
    has "$R/etc/default/still-ui" XUI_DB_DSN=
    has "$R/out" "PostgreSQL connection"
    xui_setup || return 1
    printf 'XUI_DB_TYPE=postgres\nXUI_DB_DSN=postgres://u:p@127.0.0.1/db\n' > "$R/etc/default/still-ui"
    postgresql_installed() { return 0; }
    un '' 0 --purge
    called purge_postgresql
    xui_setup || return 1
    postgresql_installed() { return 0; } # installed for something else
    un '' 0 --purge
    not_called purge_postgresql
}
t "x-ui.sh: the PostgreSQL purge is offered only when deleting the data of a PostgreSQL panel" uninstall_pg
uninstall_db_folder() {
    xui_setup || return 1
    mkdir -p "$R/srv/still-db"
    echo db > "$R/srv/still-db/x-ui.db"
    printf 'XUI_XRAY_UNIT=still-xray\nXUI_DB_FOLDER="%s"\n' "$R/srv/still-db" > "$R/etc/default/still-ui"
    un '' 0 --purge
    eq "$st" 0 "(exit status)"
    eq "$(cat "$R/srv/still-db/x-ui.db" 2> /dev/null)" db "(a folder XUI_DB_FOLDER names is never deleted)"
    hasnt "$R/out" "the data is deleted too"
    has "$R/out" "$R/srv/still-db"
    xui_setup || return 1
    mkdir -p "$R/srv/still-db"
    printf 'XUI_DB_FOLDER=%s\n' "$R/srv/still-db" > "$R/etc/default/still-ui"
    un '' 0 --keep-data
    has "$R/out" "$R/srv/still-db"
}
t "x-ui.sh: uninstall names the database folder XUI_DB_FOLDER moved, and --purge never claims it is gone" uninstall_db_folder
uninstall_xui_wrapper() {
    xui_setup || return 1
    printf "#!/bin/sh\n# Installed by still-ui's migrate.sh: this host now runs still-ui.\nexec /usr/bin/still-ui \"\$@\"\n" > "$R/usr/bin/x-ui"
    un '' 0 --keep-data
    [[ ! -e "$R/usr/bin/x-ui" ]] || afail "the x-ui wrapper of migrate.sh was left pointing at nothing"
    xui_setup || return 1
    echo "3x-ui's own menu" > "$R/usr/bin/x-ui"
    un '' 0 --keep-data
    eq "$(cat "$R/usr/bin/x-ui")" "3x-ui's own menu" "(another x-ui command was removed)"
}
t "x-ui.sh: uninstall removes migrate.sh's x-ui wrapper and nothing else named x-ui" uninstall_xui_wrapper
uninstall_failure() {
    [[ $(command id -u) != 0 ]] || return 0 # root removes it anyway
    xui_setup || return 1
    chmod 555 "$R/usr/local"
    un '' 0 --purge
    chmod 755 "$R/usr/local"
    eq "$st" 1 "(exit status when the panel folder stays)"
    has "$R/out" "Could not remove $xui_folder"
    hasnt "$R/out" "Uninstalled Successfully"
}
t "x-ui.sh: uninstall exits 1 when something could not be removed" uninstall_failure
delete_script_status() {
    fresh_root
    load "$XUI_SH" delete_script || return 1
    echo menu > "$R/menu"
    bash -c "$(declare -f delete_script); delete_script" "$R/menu"
    eq "$?" 0 "(default status)"
    [[ ! -e "$R/menu" ]] || afail "menu not removed"
    echo menu > "$R/menu"
    bash -c "$(declare -f delete_script); delete_script 1" "$R/menu"
    eq "$?" 1 "(status passed on)"
}
t "x-ui.sh: delete_script removes the menu and exits with the uninstall's status" delete_script_status

legacy_tags() {
    fresh_root
    load "$XUI_SH" legacy_version || return 1
    still_run_release_script() { log_call "release-script $*"; }
    local bad
    for bad in '' 'main' '1.5' '1.5.0;id' '../../x'; do
        (legacy_version <<< "$bad") > /dev/null 2>&1 && { echo "accepted '$bad'"; return 1; }
    done
    not_called release-script
    (legacy_version <<< "1.4.0") > /dev/null 2>&1 || return 1
    called "release-script  install.sh v1.4.0"
}
t "x-ui.sh: legacy accepts only a version number and runs the latest installer with it" legacy_tags
update_dev_refused() {
    fresh_root
    load "$XUI_SH" update_dev || return 1
    LOGE() { echo "$*"; }
    out=$(update_dev 0) && return 1
    [[ "$out" == *"no dev build"* ]]
}
t "x-ui.sh: update-dev refuses" update_dev_refused
xui_status_codes() {
    fresh_root
    load "$XUI_SH" check_status || return 1
    running_in_docker=false
    check_status
    eq "$?" 2 "(no unit: not installed)" || return 1
    touch "$xui_service/still-ui.service"
    systemctl() { log_call "systemctl $*"; echo "SubState=${SUBSTATE:-dead}"; }
    check_status
    eq "$?" 1 "(dead: not running)" || return 1
    SUBSTATE=running check_status
    eq "$?" 0 "(running)"
}
t "x-ui.sh: check_status tells installed, running and not running apart" xui_status_codes
dispatch_table() {
    # Every subcommand the usage text and the installer banner name has a case.
    local c
    for c in start stop restart restart-xray status settings enable disable log banlog update update-dev \
        update-all-geofiles migrateDB backup restore pgclient migrate-rollback legacy install uninstall; do
        grep -qE "^        \"$c\"( \||\))" "$XUI_SH" || grep -qE "^        \"[a-z-]+\" \| \"$c\"\)" "$XUI_SH" || { echo "no case for $c"; return 1; }
    done
    for c in start stop restart status settings enable disable log banlog update legacy install uninstall; do
        grep -qF "still-ui $c" "$INSTALL_SH" || { echo "install.sh banner lacks $c"; return 1; }
    done
}
t "x-ui.sh: every documented subcommand is dispatched" dispatch_table
settings_offline() {
    fresh_root
    load "$XUI_SH" check_config || return 1
    load "$XUI_SH" xui_env_file_path || return 1
    LOGI() { echo "$*"; }
    LOGE() { echo "$*"; }
    show_menu() { log_call show_menu; }
    curl() { log_call "curl $*"; return 7; } # no internet
    printf '#!/bin/sh\ncase "$2" in\n    -show) printf "hasDefaultCredential: false\\nport: 2053\\nwebBasePath: /base/\\n" ;;\n    -getCert) echo "cert: " ;;\nesac\n' > "$xui_folder/x-ui"
    chmod +x "$xui_folder/x-ui"
    # settings_run INPUT: still-ui settings with INPUT as its standard input.
    settings_run() {
        printf '%s' "$1" | timeout 20 bash -c "$(declare -f check_config xui_env_file_path LOGI LOGE show_menu curl log_call); R='$R' xui_folder='$xui_folder' release=debian; check_config" > "$R/out" 2>&1
        eq "$?" 0 "(still-ui settings without a terminal and without internet; 124 = it hung)" || { tail -n 5 "$R/out"; return 1; }
    }
    settings_run '' || return 1
    has "$R/out" "port: 2053"
    has "$R/out" "http://SERVER_IP:2053/base/"
    # An address on a last line without a newline is used, not dropped.
    settings_run '192.0.2.7' || return 1
    has "$R/out" "http://192.0.2.7:2053/base/"
    # An invalid one is refused, then the end of input gives the placeholder.
    settings_run 'not-an-ip' || return 1
    has "$R/out" "Invalid IPv4 address"
    has "$R/out" "http://SERVER_IP:2053/base/"
}
t "x-ui.sh: 'still-ui settings' without internet and without a terminal finishes" settings_offline
menu_helpers() {
    fresh_root
    load "$XUI_SH" still_sha256_of || return 1
    printf '%064d  update.sh\n%s  *x-ui.sh\nbad  install.sh\nabc123  migrate.sh\n%065d  update.sh\n' 1 "$(printf '%064d' 2)" 3 > "$R/sums"
    eq "$(still_sha256_of update.sh "$R/sums")" "$(printf '%064d' 1)"
    eq "$(still_sha256_of x-ui.sh "$R/sums")" "$(printf '%064d' 2)"
    eq "$(still_sha256_of install.sh "$R/sums")" "" "(malformed sum accepted)"
    eq "$(still_sha256_of migrate.sh "$R/sums")" ""
}
t "x-ui.sh: SHA256SUMS lookup takes only well-formed sums (text and binary mode)" menu_helpers

# ==============================================================================
# The systemd units
# ==============================================================================

units_same() {
    local base
    base=$(grep -v '^EnvironmentFile=' "$UNIT_DIR/x-ui.service.debian")
    for f in x-ui.service.rhel x-ui.service.arch; do
        [[ "$base" == "$(grep -v '^EnvironmentFile=' "$UNIT_DIR/$f")" ]] || { echo "$f differs beyond EnvironmentFile"; diff <(printf '%s\n' "$base") <(grep -v '^EnvironmentFile=' "$UNIT_DIR/$f"); return 1; }
    done
    has "$UNIT_DIR/x-ui.service.debian" "EnvironmentFile=-/etc/default/still-ui"
    has "$UNIT_DIR/x-ui.service.rhel" "EnvironmentFile=-/etc/sysconfig/still-ui"
    has "$UNIT_DIR/x-ui.service.arch" "EnvironmentFile=-/etc/conf.d/still-ui"
}
t "units: debian/rhel/arch panel units differ only in the env file, which matches install.sh" units_same
panel_unit() {
    local u="$UNIT_DIR/x-ui.service.debian"
    has "$u" "ExecStart=/usr/local/still-ui/x-ui"
    has "$u" "Restart=on-failure"
    has "$u" "KillMode=mixed"
    has "$u" "NoNewPrivileges=yes"
    has "$u" "ProtectSystem=full"
    has "$u" "UMask=0077"
    has "$u" "CapabilityBoundingSet=CAP_DAC_OVERRIDE CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW"
    has "$u" "ReadWritePaths=-/etc/still-ui -/usr/local/still-ui -/var/log/still-ui -/etc/still-xray"
    # The panel must not pull still-xray down with it.
    if grep -qE '^(BindsTo|PartOf|Requires|Conflicts)=.*still-xray' "$u"; then echo "panel unit ties still-xray to its lifecycle"; return 1; fi
    if grep -q '^SystemCallFilter=' "$u"; then echo "the unit sets a seccomp filter (old systemd cannot start it)"; return 1; fi
}
t "units: panel unit hardening, and no lifecycle tie to still-xray" panel_unit
xray_unit() {
    local u="$UNIT_DIR/still-xray.service"
    has "$u" "User=still-xray"
    has "$u" "Group=still-xray"
    eq "$(sed -n 's/^AmbientCapabilities=//p' "$u")" "CAP_NET_BIND_SERVICE CAP_NET_ADMIN"
    eq "$(sed -n 's/^CapabilityBoundingSet=//p' "$u")" "CAP_NET_BIND_SERVICE CAP_NET_ADMIN"
    has "$u" "PrivateDevices=yes"
    has "$u" "NoNewPrivileges=yes"
    has "$u" "ProtectSystem=strict"
    has "$u" "Restart=always"
    has "$u" "StartLimitIntervalSec=0"
    has "$u" "ExecStart=@XRAY_BIN@ run -c /etc/still-xray/config.json"
    has "$u" "Environment=XRAY_LOCATION_ASSET=@XRAY_ASSETS@"
    if grep -qE '^(BindsTo|PartOf|Requires)=.*still-ui' "$u"; then echo "still-xray tied to the panel"; return 1; fi
}
t "units: still-xray runs unprivileged with CAP_NET_BIND_SERVICE and CAP_NET_ADMIN only and outlives the panel" xray_unit

echo
echo "$passes passed, $failures failed"
[[ $failures == 0 ]]
