#!/usr/bin/env bash
# Checks the checks: breaks one behaviour of install.sh, update.sh, x-ui.sh or
# a unit at a time, in a scratch copy, and requires deploy/test/installer-unit.sh
# to fail on that copy. A mutation the suite does not notice means a test is
# missing or too weak. No root, no network.
#
#   bash deploy/test/installer-mutations.sh [-v] [-j JOBS]
#
# -v also prints the tests that failed for each mutation; -j runs that many
# mutations at once (default: the number of CPUs).
#
# Each mutation is a literal text replacement, optionally limited to one shell
# function; a replacement that matches nothing is itself an error, so the list
# cannot silently go stale when the scripts change.
# The replacements are literal shell text, hence the single quotes.
# shellcheck disable=SC2016
set -uo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
verbose=0 max_jobs=$(nproc 2> /dev/null || echo 2)
while (($#)); do
    case "$1" in
        -v) verbose=1; shift ;;
        -j) max_jobs="$2"; shift 2 ;;
        *) echo "usage: $0 [-v] [-j JOBS]" >&2; exit 2 ;;
    esac
done
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
n=0

# mutate NAME FILE FUNCTION FROM TO: FUNCTION may be empty (whole file). Runs
# in the background; the verdict goes to $work/<n>.res.
mutate() {
    n=$((n + 1))
    while (($(jobs -rp | wc -l) >= max_jobs)); do wait -n; done
    mutate_one "$@" > "$work/$n.res" 2>&1 &
}
mutate_one() {
    local name="$1" file="$2" func="$3" from="$4" to="$5" m out rc
    m=$(mktemp -d "$work/m.XXXXXX")
    cp "$repo_root"/{install.sh,update.sh,x-ui.sh,still-xray.service,x-ui.service.debian,x-ui.service.rhel,x-ui.service.arch} "$m/"
    if ! F="$func" A="$from" B="$to" perl -0777 -i -pe '
        BEGIN { $f = $ENV{F}; $x = $ENV{A}; $y = $ENV{B}; $n = 0 }
        if ($f eq "") {
            $n += s/\Q$x\E/$y/g;
        } else {
            s{^(\Q$f\E\(\) \{\n.*?\n\}\n)}{ my $blk = $1; $n += ($blk =~ s/\Q$x\E/$y/g); $blk }mse;
        }
        END { exit($n ? 0 : 3) }' "$m/$file"; then
        echo "BROKEN  $name: '$from' not found in ${func:+$func() of }$file"
        return
    fi
    out=$(INSTALL_SH="$m/install.sh" UPDATE_SH="$m/update.sh" XUI_SH="$m/x-ui.sh" UNIT_DIR="$m" \
        bash "$repo_root/deploy/test/installer-unit.sh" 2>&1)
    rc=$?
    if [[ $rc != 0 ]]; then
        echo "caught  $name ($(grep -c '^FAIL' <<< "$out") failing)"
        [[ $verbose == 0 ]] || grep '^FAIL' <<< "$out" | sed 's/^/          /'
    else
        echo "MISSED  $name"
    fi
    rm -rf "$m"
}

# --- checksums and what is refused before anything changes ----------------------
mutate "install: accept a mismatching archive" install.sh verify_release_checksum \
    'if [[ ! "${expected}" =~ ^[0-9a-f]{64}$ || "${expected}" != "${actual}" ]]; then' 'if false; then'
mutate "install: accept any 64-char sum" install.sh verify_release_checksum \
    '|| "${expected}" != "${actual}"' ''
mutate "install: accept a release without .sha256" install.sh verify_release_checksum \
    'if [[ "${code}" != "200" ]]; then' 'if false; then'
mutate "install: skip the checksum of a tagged release" install.sh install_x-ui \
    'verify_release_checksum "${url}"' ': verify_release_checksum "${url}"'
mutate "install: skip the checksum of the latest release" install.sh install_x-ui \
    'verify_release_checksum "https://github.com' ': verify_release_checksum "https://github.com'
mutate "install: skip the archive content check" install.sh install_x-ui \
    'require_archive_files "${xui_folder}' ': require_archive_files "${xui_folder}'
mutate "install: accept any tag" install.sh install_x-ui \
    'if [[ ! "$tag_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then' 'if false; then'
mutate "install: accept the dev channel" install.sh install_x-ui \
    'if [[ "$tag_version" == "dev" || "$tag_version" == "dev-latest" ]]; then' 'if false; then'
mutate "install: drop the v1.0.0 floor" install.sh install_x-ui 'min_version="1.0.0"' 'min_version="0.0.1"'
mutate "install: stop the panel before the download" install.sh install_x-ui \
    '    local local_archive="${STILL_UI_ARCHIVE:-}"' '    systemctl stop still-ui; local local_archive="${STILL_UI_ARCHIVE:-}"'
mutate "install: lose custom bin/ files" install.sh install_x-ui 'if [[ ! -e "bin/${rel}" ]]; then' 'if false; then'
mutate "install: accept a foreign STILL_UI_ARCHIVE" install.sh install_x-ui \
    'still-ui/x-ui still-ui/x-ui.sh > /dev/null' '--wildcards "*" > /dev/null'

# --- memory ceilings ------------------------------------------------------------
mutate "install: panel MemoryMax 40% of RAM" install.sh _still_memory_dropin 'mem_kb * 35 / 100' 'mem_kb * 40 / 100'
mutate "install: panel MemoryMax floor 128 MiB" install.sh _still_memory_dropin \
    '((max_mb < 256)) && max_mb=256' '((max_mb < 128)) && max_mb=128'
mutate "install: panel MemoryMax cap 2 GiB" install.sh _still_memory_dropin \
    '((max_mb > 1024)) && max_mb=1024' '((max_mb > 2048)) && max_mb=2048'
mutate "install: panel MemoryHigh 90%" install.sh _still_memory_dropin 'max_mb * 85 / 100' 'max_mb * 90 / 100'
mutate "install: panel may swap" install.sh _still_memory_dropin 'echo "MemorySwapMax=0"' ':'
mutate "install: panel override ignored" install.sh _still_memory_dropin 'max="$setting"' 'max="${max_mb}M"'
mutate "install: panel off keeps the drop-in" install.sh _still_memory_dropin '        rm -f "$dropin"' '        :'
mutate "update: panel MemoryMax 40% of RAM (copy drifts)" update.sh _still_memory_dropin 'mem_kb * 35 / 100' 'mem_kb * 40 / 100'
mutate "install: still-xray MemoryMax a third of RAM" install.sh _still_xray_unit 'mem_kb / 2 / 1024' 'mem_kb / 3 / 1024'
mutate "install: still-xray MemoryMax cap 8 GiB" install.sh _still_xray_unit \
    '((max_mb > 4096)) && max_mb=4096' '((max_mb > 8192)) && max_mb=8192'
mutate "install: still-xray GOMEMLIMIT 90%" install.sh _still_xray_unit 'max_mb * 85 / 100' 'max_mb * 90 / 100'

# --- still-xray unit decisions ----------------------------------------------------
mutate "install: still-xray despite a refusing check" install.sh _still_xray_unit '                setting=off
                ;;' '                setting=on
                ;;'
mutate "install: still-xray without systemd" install.sh _still_xray_unit \
    '[[ -d /run/systemd/system ]] || return 0' ':'
mutate "install: CAP_NET_ADMIN drop-in not passed to the check" install.sh _still_xray_unit 'net_admin="-net-admin"' 'net_admin=""'
mutate "install: still-xray assets placeholder left" install.sh _still_xray_unit '-e "s#@XRAY_ASSETS@#${bin_dir}#g"' ''
mutate "install: still-xray not disabled when refused" install.sh _still_xray_unit \
    'systemctl disable --now still-xray > /dev/null 2>&1' ':'
mutate "install: STILL_XRAY_UNIT=off ignored" install.sh _still_xray_unit 'on | off | auto) ;;' 'on | auto) ;;
        off) setting=auto ;;'

# --- env file, swap, import, result file ------------------------------------------
mutate "install: env file edit misses 'export KEY='" install.sh _still_env_set '\(export[[:space:]]\+\)\?' ''
mutate "install: new env file world-readable" install.sh _still_env_set 'chmod 600 "$file"' 'chmod 644 "$file"'
mutate "install: empty value keeps the key" install.sh _still_env_set '[[ -n "$value" ]] && printf' 'printf'
mutate "install: swap offered up to 4 GiB RAM" install.sh _still_swap_check '2 * 1024 * 1024 + 262144' '4 * 1024 * 1024'
mutate "install: swap created by default" install.sh _still_swap_check 'STILL_UI_CREATE_SWAP:-no' 'STILL_UI_CREATE_SWAP:-yes'
mutate "install: swap inside containers" install.sh _still_swap_check 'systemd-detect-virt -cq' 'false'
mutate "install: swap over an existing /swapfile" install.sh _still_swap_check 'if [[ -e /swapfile ]]; then' 'if false; then'
mutate "install: duplicate fstab line" install.sh _still_swap_check \
    "grep -qE '^[[:space:]]*/swapfile[[:space:]]' /etc/fstab ||" ''
mutate "install: half-allocated swap kept" install.sh _still_swap_check '        rm -f /swapfile
        echo -e "${yellow}Could not allocate' '        echo -e "${yellow}Could not allocate'
mutate "install: 3x-ui import over an existing database" install.sh _still_import_3xui '&& ! -e /etc/still-ui/x-ui.db ]]' ']]'
mutate "install: 3x-ui import by default" install.sh _still_import_3xui 'STILL_UI_IMPORT_3XUI:-no' 'STILL_UI_IMPORT_3XUI:-yes'
mutate "install: install-result.env not shell-escaped" install.sh write_install_result "printf 'XUI_PASSWORD=%q\n'" "printf 'XUI_PASSWORD=%s\n'"
mutate "install: install-result.env readable by all" install.sh write_install_result 'chmod 600 "$result_file"' 'chmod 644 "$result_file"'

# --- service units and their drop-ins ----------------------------------------------
mutate "install: unit installed from an empty file" install.sh _install_xui_service_unit '    if [[ ! -s "$temp_file" ]]; then' '    if false; then'
mutate "install: whitespace folder written into ReadWritePaths" install.sh _xui_service_write_paths_dropin \
    'if [[ "$path" != "${path//[[:space:]]/}" ]]; then' 'if false; then'
mutate "install: % not escaped in ReadWritePaths" install.sh _xui_service_write_paths_dropin 'escaped_path="${path//%/%%}"' 'escaped_path="$path"'
mutate "install: seccomp filter on old systemd" install.sh _xui_service_write_paths_dropin '-ge 239 ]]' '-ge 200 ]]'
mutate "update: env file ignored by the sandbox drop-in" update.sh _xui_service_write_paths_dropin '        source "$env_file"' '        :'
mutate "units: still-xray gets CAP_NET_ADMIN" still-xray.service "" \
    'CapabilityBoundingSet=CAP_NET_BIND_SERVICE' 'CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_ADMIN'
mutate "units: still-xray runs as root" still-xray.service "" 'User=still-xray' 'User=root'
mutate "units: still-xray stops with the panel" still-xray.service "" '[Unit]' '[Unit]
PartOf=still-ui.service'
mutate "units: panel unit pulls still-xray down" x-ui.service.debian "" '[Unit]' '[Unit]
BindsTo=still-xray.service'
mutate "units: rhel unit reads the debian env file" x-ui.service.rhel "" '/etc/sysconfig/still-ui' '/etc/default/still-ui'
mutate "units: panel unit loses NoNewPrivileges" x-ui.service.arch "" 'NoNewPrivileges=yes' 'NoNewPrivileges=no'

# --- update.sh ---------------------------------------------------------------------
mutate "update: archive without the unit accepted" update.sh _stage_update_archive \
    'if [[ ! -s "${update_stage_dir}/still-ui/${f}" && ! -s "${update_stage_dir}/still-ui/still-ui.service" ]]; then' 'if false; then'
mutate "update: archive without x-ui.sh accepted" update.sh _stage_update_archive 'for f in x-ui x-ui.sh; do' 'for f in x-ui; do'
mutate "update: corrupt archive kept" update.sh _stage_update_archive '        rm -f "${archive}"' '        :'
mutate "update: rollback forgets the CLI" update.sh _update_restore_and_start \
    '[[ -s "${update_backup_dir}/still-ui.cli" ]] && cp -a' '[[ -s "${update_backup_dir}/still-ui.cli" ]] && :'
mutate "update: rollback forgets the unit" update.sh _update_restore_and_start 'cp -a "${update_backup_dir}/service-unit" "${unit}"' ':'
mutate "update: rollback does not start the panel" update.sh _update_restore_and_start '        systemctl start still-ui > /dev/null 2>&1' '        :'
mutate "update: rollback after the database migration" update.sh _update_restore_and_start \
    'if [[ "${update_rollback_files}" == "1" && -n' 'if [[ -n'
mutate "update: backup skips the xray binary" update.sh _backup_current_install '"bin/xray-linux-$(arch)" ' ''
mutate "update: read-only folder not detected" update.sh require_writable_update_paths 'if ! : > "$probe" 2> /dev/null; then' 'if false; then'

# --- x-ui.sh -------------------------------------------------------------------------
mutate "menu: uninstall keeps the still-xray user" x-ui.sh uninstall 'id still-xray > /dev/null 2>&1 && userdel still-xray' ':'
mutate "menu: uninstall keeps the still-xray unit" x-ui.sh uninstall 'rm -rf -- "${xui_service}/still-xray.service" "${xui_service}/still-xray.service.d"' ':'
mutate "menu: uninstall keeps the panel drop-ins" x-ui.sh uninstall 'rm -rf -- "${xui_service}/still-ui.service.d"' ':'
mutate "menu: uninstall defaults to yes" x-ui.sh uninstall 'xray will also uninstalled!" "n"' 'xray will also uninstalled!" "y"'
mutate "menu: uninstall purges PostgreSQL for SQLite panels" x-ui.sh uninstall \
    'if [[ "$panel_used_postgres" == "true" ]] && postgresql_installed; then' 'if postgresql_installed; then'
mutate "menu: legacy accepts any text" x-ui.sh legacy_version \
    'if [[ ! "$tag_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then' 'if false; then'
mutate "menu: settings asks for the IP forever without a terminal" x-ui.sh check_config \
    'if ! read -rp "Please enter your server'"'"'s public IPv4 address: " server_ip; then' 'read -rp "Please enter your server'"'"'s public IPv4 address: " server_ip; if false; then'
mutate "menu: short sums accepted in SHA256SUMS" x-ui.sh still_sha256_of ' && length($1) == 64' ''
mutate "menu: no unit reads as running" x-ui.sh check_status '            return 2
        fi
        temp=$(systemctl' '            :
        fi
        temp=$(systemctl'

wait
for i in $(seq 1 "$n"); do cat "$work/$i.res"; done
caught=$(cat "$work"/*.res | grep -c '^caught')
missed=$(cat "$work"/*.res | grep -c '^MISSED')
broken=$(cat "$work"/*.res | grep -c '^BROKEN')
echo
echo "$caught caught, $missed missed, $broken broken (of $n)"
[[ $missed == 0 && $broken == 0 && $caught == "$n" ]]
