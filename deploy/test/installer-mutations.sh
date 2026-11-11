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

# mutate NAME FILES FUNCTION FROM TO EXPECT: FILES is one file or several
# joined with "+" (helpers that install.sh and update.sh both carry, and that
# the unit suite requires to stay identical, are changed in both, so only a
# behaviour test can catch the mutant); FUNCTION may be empty (whole file). The
# mutant counts as caught only when the unit test whose name contains EXPECT
# fails. Runs in the background; the verdict goes to $work/<n>.res.
mutate() {
    n=$((n + 1))
    while (($(jobs -rp | wc -l) >= max_jobs)); do wait -n; done
    mutate_one "$@" > "$work/$n.res" 2>&1 &
}
mutate_one() {
    local name="$1" files="$2" func="$3" from="$4" to="$5" expect="${6:-}" m out file failed
    m=$(mktemp -d "$work/m.XXXXXX")
    cp "$repo_root"/{install.sh,update.sh,still-setup.sh,x-ui.sh,still-xray.service,x-ui.service.debian,x-ui.service.rhel,x-ui.service.arch} "$m/"
    IFS=+ read -ra files <<< "$files"
    for file in "${files[@]}"; do
        if ! F="$func" A="$from" B="$to" perl -0777 -i -pe '
            BEGIN { $f = $ENV{F}; $x = $ENV{A}; $y = $ENV{B}; $n = 0 }
            if ($f eq "") {
                $n += s/\Q$x\E/$y/g;
            } else {
                s{^(\Q$f\E\(\) \{\n.*?\n\}\n)}{ my $blk = $1; $n += ($blk =~ s/\Q$x\E/$y/g); $blk }mse;
            }
            END { exit($n ? 0 : 3) }' "$m/$file"; then
            echo "BROKEN  $name: '$from' not found in ${func:+$func() of }$file"
            rm -rf "$m"
            return
        fi
    done
    out=$(INSTALL_SH="$m/install.sh" UPDATE_SH="$m/update.sh" SETUP_SH="$m/still-setup.sh" XUI_SH="$m/x-ui.sh" UNIT_DIR="$m" \
        bash "$repo_root/deploy/test/installer-unit.sh" 2>&1)
    failed=$(grep '^FAIL ' <<< "$out" | sed 's/^FAIL //')
    if [[ -z "$expect" ]]; then
        echo "BROKEN  $name: no expected test given"
    elif grep -qF -- "$expect" <<< "$failed"; then
        echo "caught  $name ($(grep -c . <<< "$failed") failing)"
    elif [[ -n "$failed" ]]; then
        echo "MISSED  $name: '$expect' passed; other tests failed instead"
    else
        echo "MISSED  $name"
    fi
    [[ $verbose == 0 || -z "$failed" ]] || printf '          %s\n' "${failed//$'\n'/$'\n'          }"
    rm -rf "$m"
}

# --- checksums and what is refused before anything changes ----------------------
mutate "install: accept a mismatching archive" install.sh verify_release_checksum \
    'if [[ ! "${expected}" =~ ^[0-9a-f]{64}$ || "${expected}" != "${actual}" ]]; then' 'if false; then' \
    'install.sh: a tampered archive is refused before anything changes'
mutate "install: accept any 64-char sum" install.sh verify_release_checksum \
    '|| "${expected}" != "${actual}"' '' \
    'install.sh: a tampered archive is refused before anything changes'
mutate "install: accept a release without .sha256" install.sh verify_release_checksum \
    'if [[ "${code}" != "200" ]]; then' 'if false; then' \
    'install.sh: a release without .sha256 is refused'
mutate "install: skip the checksum of a tagged release" install.sh install_x-ui \
    'verify_release_checksum "${url}"' ': verify_release_checksum "${url}"' \
    'install.sh: a tampered archive is refused before anything changes'
mutate "install: skip the checksum of the latest release" install.sh install_x-ui \
    'verify_release_checksum "https://github.com' ': verify_release_checksum "https://github.com' \
    'install.sh: an upper-case (non-sha256sum) sum is refused'
mutate "install: skip the archive content check" install.sh install_x-ui \
    'require_archive_files "${xui_folder}' ': require_archive_files "${xui_folder}' \
    'install.sh: an archive without the systemd unit is refused'
mutate "install: accept any tag" install.sh install_x-ui \
    'if [[ ! "$tag_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then' 'if false; then' \
    'install.sh: a branch name is refused'
mutate "install: accept the dev channel" install.sh install_x-ui \
    'if [[ "$tag_version" == "dev" || "$tag_version" == "dev-latest" ]]; then' 'if false; then' \
    'install.sh: the dev channel is refused'
mutate "install: drop the v1.0.0 floor" install.sh install_x-ui 'min_version="1.0.0"' 'min_version="0.0.1"' \
    'install.sh: releases older than v1.0.0 are refused'
mutate "install: stop the panel before the download" install.sh install_x-ui \
    '    local local_archive="${STILL_UI_ARCHIVE:-}"' '    systemctl stop still-ui; local local_archive="${STILL_UI_ARCHIVE:-}"' \
    'install.sh: a tampered archive is refused before anything changes'
mutate "install: lose custom bin/ files" install.sh install_x-ui 'if [[ ! -e "bin/${rel}" ]]; then' 'if false; then' \
    'install.sh: a verified install replaces binary, menu and xray, keeps custom bin/ files'
mutate "install: accept a foreign STILL_UI_ARCHIVE" install.sh install_x-ui \
    'still-ui/x-ui still-ui/x-ui.sh > /dev/null' '--wildcards "*" > /dev/null' \
    'install.sh: a STILL_UI_ARCHIVE that is not a still-ui archive is refused'

# --- memory ceilings ------------------------------------------------------------
mutate "install: panel MemoryMax 40% of RAM" still-setup.sh _still_memory_dropin 'mem_kb * 35 / 100' 'mem_kb * 40 / 100' \
    'still-setup.sh: panel MemoryMax is 35% of RAM (2 GiB RAM -> 716M, high 85%)'
mutate "install: panel MemoryMax floor 128 MiB" still-setup.sh _still_memory_dropin \
    '((max_mb < 256)) && max_mb=256' '((max_mb < 128)) && max_mb=128' \
    'still-setup.sh: panel MemoryMax floors at 256 MiB (512 MiB RAM)'
mutate "install: panel MemoryMax cap 2 GiB" still-setup.sh _still_memory_dropin \
    '((max_mb > 1024)) && max_mb=1024' '((max_mb > 2048)) && max_mb=2048' \
    'still-setup.sh: panel MemoryMax caps at 1 GiB (4 GiB RAM)'
mutate "install: panel MemoryHigh 90%" still-setup.sh _still_memory_dropin 'max_mb * 85 / 100' 'max_mb * 90 / 100' \
    'panel MemoryMax is 35% of RAM (2 GiB RAM -> 716M, high 85%)'
mutate "install: panel may swap" still-setup.sh _still_memory_dropin 'echo "MemorySwapMax=0"' ':' \
    'panel MemoryMax is 35% of RAM (2 GiB RAM -> 716M, high 85%)'
mutate "install: panel override ignored" still-setup.sh _still_memory_dropin 'max="$setting"' 'max="${max_mb}M"' \
    'still-setup.sh: STILL_UI_MEMORY_MAX from env file or environment, invalid ignored, off removes the drop-in'
mutate "install: panel off keeps the drop-in" still-setup.sh _still_memory_dropin '        rm -f "$dropin"' '        :' \
    'still-setup.sh: STILL_UI_MEMORY_MAX from env file or environment, invalid ignored, off removes the drop-in'
mutate "install: still-xray MemoryMax a third of RAM" still-setup.sh _still_xray_unit 'mem_kb / 2 / 1024' 'mem_kb / 3 / 1024' \
    'still-xray MemoryMax is half the RAM (4 GiB -> 2048M)'
mutate "install: still-xray MemoryMax cap 8 GiB" still-setup.sh _still_xray_unit \
    '((max_mb > 4096)) && max_mb=4096' '((max_mb > 8192)) && max_mb=8192' \
    'still-setup.sh: still-xray MemoryMax caps at 4 GiB'
mutate "install: still-xray GOMEMLIMIT 90%" still-setup.sh _still_xray_unit 'max_mb * 85 / 100' 'max_mb * 90 / 100' \
    'STILL_XRAY_MEMORY_MAX=1G sets 1024M'

# --- still-xray unit decisions ----------------------------------------------------
mutate "install: still-xray despite a refusing check" still-setup.sh _still_xray_unit '                setting=off
                ;;' '                setting=on
                ;;' \
    'still-setup.sh: a config the unit cannot run keeps Xray in the panel and disables still-xray'
mutate "install: still-xray without systemd" still-setup.sh _still_xray_unit \
    '[[ -d /run/systemd/system ]] || return 0' ':' \
    'still-setup.sh: without systemd as PID 1 nothing is written'
mutate "install: CAP_NET_ADMIN not passed to the check" still-setup.sh _still_xray_unit '&& check_flags+=(-net-admin)' '&& :' \
    'still-setup.sh: the CAP_NET_ADMIN of the unit or a local drop-in is passed to xray-unit-check'
mutate "install: the template's CAP_NET_ADMIN ignored" still-setup.sh _still_xray_unit \
    'grep -qs '"'"'^AmbientCapabilities=.*CAP_NET_ADMIN'"'"' "$template" "${xui_service}"' 'grep -qs '"'"'^AmbientCapabilities=.*CAP_NET_ADMIN'"'"' "${xui_service}"' \
    'still-setup.sh: the CAP_NET_ADMIN of the unit or a local drop-in is passed to xray-unit-check'
mutate "install: PrivateDevices=no not passed to the check" still-setup.sh _still_xray_unit '            check_flags+=(-devices)' '            :' \
    'still-setup.sh: a PrivateDevices=no drop-in is passed as -devices when the check knows it'
mutate "install: still-xray assets placeholder left" still-setup.sh _still_xray_unit '-e "s#@XRAY_ASSETS@#${bin_dir}#g"' '' \
    'still-setup.sh: auto mode moves Xray to still-xray when xray-unit-check passes'
mutate "install: still-xray not disabled when refused" still-setup.sh _still_xray_unit \
    'systemctl disable --now still-xray > /dev/null 2>&1' ':' \
    'still-setup.sh: a config the unit cannot run keeps Xray in the panel and disables still-xray'
mutate "install: STILL_XRAY_UNIT=off ignored" still-setup.sh _still_xray_unit 'on | off | auto) ;;' 'on | auto) ;;
        off) setting=auto ;;' \
    'still-setup.sh: STILL_XRAY_UNIT=off/on (environment or env file) override the check'

# --- env file, swap, import, result file ------------------------------------------
mutate "install: env file edit misses 'export KEY='" still-setup.sh _still_env_set '\(export[[:space:]]\+\)\?' '' \
    'still-setup.sh: _still_env_set replaces the key (also with export/indent), keeps the rest, idempotent, empty drops it'
mutate "install: new env file world-readable" still-setup.sh _still_env_set 'chmod 600 "$file"' 'chmod 644 "$file"' \
    'still-setup.sh: _still_env_set creates a missing env file 0600'
mutate "install: empty value keeps the key" still-setup.sh _still_env_set '[[ -n "$value" ]] && printf' 'printf' \
    'still-setup.sh: _still_env_set replaces the key (also with export/indent), keeps the rest, idempotent, empty drops it'
mutate "install: swap offered up to 4 GiB RAM" install.sh _still_swap_check '2 * 1024 * 1024 + 262144' '4 * 1024 * 1024' \
    'install.sh: no swap offer above 2 GiB RAM (+256 MiB slack)'
mutate "install: swap created by default" install.sh _still_swap_check 'STILL_UI_CREATE_SWAP:-no' 'STILL_UI_CREATE_SWAP:-yes' \
    'install.sh: non-interactive install only warns about missing swap'
mutate "install: swap inside containers" install.sh _still_swap_check 'systemd-detect-virt -cq' 'false' \
    'install.sh: no swap file inside a container'
mutate "install: swap over an existing /swapfile" install.sh _still_swap_check 'if [[ -e /swapfile ]]; then' 'if false; then' \
    'install.sh: an existing inactive /swapfile is left alone'
mutate "install: duplicate fstab line" install.sh _still_swap_check \
    "grep -qE '^[[:space:]]*/swapfile[[:space:]]' /etc/fstab ||" '' \
    'install.sh: STILL_UI_CREATE_SWAP=yes creates a 2 GiB swap file (<= 2 GiB RAM, no swap), fstab once'
mutate "install: half-allocated swap kept" install.sh _still_swap_check '        rm -f /swapfile
        echo -e "${yellow}Could not allocate' '        echo -e "${yellow}Could not allocate' \
    'install.sh: swap falls back to dd, cleans up when allocation or swapon fails'
mutate "install: 3x-ui import over an existing database" install.sh _still_import_3xui '&& ! -e /etc/still-ui/x-ui.db ]]' ']]' \
    'install.sh: no 3x-ui import without consent, never over an existing still-ui database'
mutate "install: 3x-ui import by default" install.sh _still_import_3xui 'STILL_UI_IMPORT_3XUI:-no' 'STILL_UI_IMPORT_3XUI:-yes' \
    'install.sh: no 3x-ui import without consent, never over an existing still-ui database'
mutate "install: 3x-ui import keeps XUI_DB_FOLDER" install.sh _still_import_3xui '|BIN_FOLDER|LOG_FOLDER|MAIN_FOLDER)=' '|BIN_FOLDER|MAIN_FOLDER)=' \
    'install.sh: STILL_UI_IMPORT_3XUI=yes copies the 3x-ui database and stops x-ui'
mutate "install: 3x-ui import leaves x-ui's unit in place" install.sh _still_import_3xui 'mv "$unit" "${retired}/files${unit}" ||' 'false ||' \
    'install.sh: the 3x-ui import retires x-ui'"'"'s unit and points acme.sh renewals at still-ui'
mutate "install: 3x-ui import leaves acme.sh restarting x-ui" install.sh _still_import_3xui '&& retarget_acme_hooks "${retired}/acme-hooks.tsv"' '&& :' \
    'install.sh: the 3x-ui import retires x-ui'"'"'s unit and points acme.sh renewals at still-ui'
mutate "install: retired drop-in folders collide" install.sh _still_import_3xui 'mv "$unit" "${retired}/files${unit}"' 'rm -rf "${retired}/files/$(basename "$unit")"; mv "$unit" "${retired}/files/$(basename "$unit")"' \
    'install.sh: the 3x-ui import retires x-ui'"'"'s unit and points acme.sh renewals at still-ui'
mutate "menu: uninstall --purge leaves 3x-ui retired" x-ui.sh uninstall '"$data" == "purge" && -f "${still_retired_3xui}/migrate.sh"' '"$data" == "never" && -f "${still_retired_3xui}/migrate.sh"' \
    'x-ui.sh: uninstall --purge puts back what a 3x-ui import retired'
mutate "menu: kept-data uninstall brings 3x-ui back" x-ui.sh uninstall '"$data" == "purge" && -f "${still_retired_3xui}/migrate.sh"' '-f "${still_retired_3xui}/migrate.sh"' \
    'x-ui.sh: uninstall keeping the data leaves an imported 3x-ui retired'
mutate "install: 3x-ui import keeps no migrate.sh to undo it" install.sh _still_import_3xui 'cp "${xui_folder}/migrate.sh" "${retired}/migrate.sh" &&' 'false &&' \
    'install.sh: the retired migrate.sh puts back'
mutate "menu: uninstall keeps the torrent block" x-ui.sh uninstall '        nft delete table inet still_torrent_block > /dev/null 2>&1' '        :' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: uninstall keeps the port hopping redirect" x-ui.sh uninstall '        nft delete table inet still_port_hop > /dev/null 2>&1' '        :' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: reinstall hint from a branch" x-ui.sh uninstall 'https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh' 'https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "install: install-result.env not shell-escaped" install.sh write_install_result "printf 'XUI_PASSWORD=%q\n'" "printf 'XUI_PASSWORD=%s\n'" \
    'install.sh: install-result.env is 0600 and safely source-able with any password'
mutate "install: install-result.env readable by all" install.sh write_install_result 'chmod 600 "$result_file"' 'chmod 644 "$result_file"' \
    'install.sh: install-result.env is 0600 and safely source-able with any password'

# --- service units and their drop-ins ----------------------------------------------
mutate "install: unit installed from an empty file" install.sh _install_xui_service_unit '    if [[ ! -s "$temp_file" ]]; then' '    if false; then' \
    'install.sh: the panel unit is replaced atomically, never by an empty or missing file'
mutate "install: whitespace folder written into ReadWritePaths" install.sh _xui_service_write_paths_dropin \
    'if [[ "$path" != "${path//[[:space:]]/}" ]]; then' 'if false; then' \
    'install.sh: the sandbox drop-in lists the folders from the env file (escaped, no whitespace paths, filter only on systemd >= 239)'
mutate "install: % not escaped in ReadWritePaths" install.sh _xui_service_write_paths_dropin 'escaped_path="${path//%/%%}"' 'escaped_path="$path"' \
    'install.sh: the sandbox drop-in lists the folders from the env file (escaped, no whitespace paths, filter only on systemd >= 239)'
mutate "install: seccomp filter on old systemd" install.sh _xui_service_write_paths_dropin '-ge 239 ]]' '-ge 200 ]]' \
    'install.sh: the sandbox drop-in lists the folders from the env file (escaped, no whitespace paths, filter only on systemd >= 239)'
mutate "update: env file ignored by the sandbox drop-in" update.sh _xui_service_write_paths_dropin '        source "$env_file"' '        :' \
    'update.sh: the sandbox drop-in lists the folders from the env file (escaped, no whitespace paths, filter only on systemd >= 239)'
mutate "units: still-xray loses CAP_NET_ADMIN" still-xray.service "" \
    'AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_ADMIN' 'AmbientCapabilities=CAP_NET_BIND_SERVICE' \
    'units: still-xray runs unprivileged with CAP_NET_BIND_SERVICE and CAP_NET_ADMIN only and outlives the panel'
mutate "units: still-xray gets CAP_SYS_ADMIN" still-xray.service "" \
    'CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_ADMIN' 'CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_ADMIN CAP_SYS_ADMIN' \
    'units: still-xray runs unprivileged with CAP_NET_BIND_SERVICE and CAP_NET_ADMIN only and outlives the panel'
mutate "units: still-xray sees the host's /dev" still-xray.service "" 'PrivateDevices=yes' 'PrivateDevices=no' \
    'units: still-xray runs unprivileged with CAP_NET_BIND_SERVICE and CAP_NET_ADMIN only and outlives the panel'
mutate "units: still-xray runs as root" still-xray.service "" 'User=still-xray' 'User=root' \
    'units: still-xray runs unprivileged with CAP_NET_BIND_SERVICE and CAP_NET_ADMIN only'
mutate "units: still-xray stops with the panel" still-xray.service "" '[Unit]' '[Unit]
PartOf=still-ui.service' \
    'units: still-xray runs unprivileged with CAP_NET_BIND_SERVICE and CAP_NET_ADMIN only and outlives the panel'
mutate "units: panel unit pulls still-xray down" x-ui.service.debian "" '[Unit]' '[Unit]
BindsTo=still-xray.service' \
    'units: panel unit hardening, and no lifecycle tie to still-xray'
mutate "units: rhel unit reads the debian env file" x-ui.service.rhel "" '/etc/sysconfig/still-ui' '/etc/default/still-ui' \
    'units: debian/rhel/arch panel units differ only in the env file, which matches install.sh'
mutate "units: panel unit loses NoNewPrivileges" x-ui.service.arch "" 'NoNewPrivileges=yes' 'NoNewPrivileges=no' \
    'units: debian/rhel/arch panel units differ only in the env file, which matches install.sh'

# --- update.sh ---------------------------------------------------------------------
mutate "update: archive without the unit accepted" update.sh _stage_update_archive \
    'if [[ ! -s "${update_stage_dir}/still-ui/${f}" && ! -s "${update_stage_dir}/still-ui/still-ui.service" ]]; then' 'if false; then' \
    'update.sh: an archive without the unit is refused before the stop'
mutate "update: archive without x-ui.sh accepted" update.sh _stage_update_archive 'for f in x-ui x-ui.sh; do' 'for f in x-ui; do' \
    'update.sh: an archive without x-ui.sh is refused before the stop'
mutate "update: corrupt archive kept" update.sh _stage_update_archive '        rm -f "${archive}"' '        :' \
    'update.sh: a corrupt archive is refused and removed'
mutate "update: rollback forgets the CLI" update.sh _update_restore_and_start \
    '[[ -s "${update_backup_dir}/still-ui.cli" ]] && cp -a' '[[ -s "${update_backup_dir}/still-ui.cli" ]] && :' \
    'update.sh: a failed update puts back binary, menu, xray, CLI and unit and starts the panel'
mutate "update: rollback forgets the unit" update.sh _update_restore_and_start 'cp -a "${update_backup_dir}/service-unit" "${unit}"' ':' \
    'update.sh: a failed update puts back binary, menu, xray, CLI and unit and starts the panel'
mutate "update: rollback does not start the panel" update.sh _update_restore_and_start '        systemctl start still-ui > /dev/null 2>&1' '        :' \
    'update.sh: a failed update puts back binary, menu, xray, CLI and unit and starts the panel'
mutate "update: rollback after the database migration" update.sh _update_restore_and_start \
    'if [[ "${update_rollback_files}" == "1" && -n' 'if [[ -n' \
    'update.sh: after the database migration a failure only restarts the panel; before the stop nothing runs'
mutate "update: backup skips the xray binary" update.sh _backup_current_install '"bin/xray-linux-$(arch)" ' '' \
    'update.sh: a failed update puts back binary, menu, xray, CLI and unit and starts the panel'
mutate "update: read-only folder not detected" update.sh require_writable_update_paths 'if ! : > "$probe" 2> /dev/null; then' 'if false; then' \
    'update.sh: a read-only install folder stops the update before anything changes'

# --- x-ui.sh -------------------------------------------------------------------------
mutate "menu: uninstall keeps the still-xray user" x-ui.sh uninstall 'id still-xray > /dev/null 2>&1 && userdel still-xray' ':' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: uninstall keeps the still-xray unit" x-ui.sh uninstall 'rm -rf -- "${xui_service}/still-xray.service" "${xui_service}/still-xray.service.d"' ':' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: uninstall keeps the panel drop-ins" x-ui.sh uninstall 'rm -rf -- "${xui_service}/still-ui.service.d"' ':' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: uninstall defaults to yes" x-ui.sh uninstall 'xray will also uninstalled!" "n"' 'xray will also uninstalled!" "y"' \
    'x-ui.sh: uninstall defaults to no (answer n or no input) and then changes nothing'
mutate "menu: uninstall deletes the data by default" x-ui.sh uninstall '        data=keep
        if _still_stdin_is_tty' '        data=purge
        if _still_stdin_is_tty' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: data question defaults to yes" x-ui.sh uninstall 'clients, settings)?" "n"' 'clients, settings)?" "y"' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: data question asked without a terminal" x-ui.sh uninstall 'if _still_stdin_is_tty; then' 'if true; then' \
    'x-ui.sh: without a terminal and without a flag uninstall never deletes the data'
mutate "menu: --purge keeps the data" x-ui.sh uninstall '--purge) data=purge ;;' '--purge) data=keep ;;' \
    'x-ui.sh: '"'"'uninstall --keep-data'"'"' and '"'"'--purge'"'"' run without questions'
mutate "menu: --keep-data deletes the data" x-ui.sh uninstall '--keep-data) data=keep ;;' '--keep-data) data=purge ;;' \
    'x-ui.sh: '"'"'uninstall --keep-data'"'"' and '"'"'--purge'"'"' run without questions'
mutate "menu: kept data deleted anyway" x-ui.sh uninstall '    if [[ "$data" == "purge" ]]; then
        rm /etc/still-ui/ -rf' '    if true; then
        rm /etc/still-ui/ -rf' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: env file deleted with kept data" x-ui.sh uninstall '    rm ${xui_folder}/ -rf
' '    rm ${xui_folder}/ -rf
    rm -f "$db_env_file"
' \
    'x-ui.sh: uninstall removes the program and, by default, keeps the data (exit 0)'
mutate "menu: unknown uninstall option ignored" x-ui.sh uninstall '                return 2' '                continue' \
    'x-ui.sh: an unknown uninstall option changes nothing (exit 2)'
mutate "menu: uninstall exits 1 on success" x-ui.sh delete_script 'exit "${1:-0}"' 'exit 1' \
    'x-ui.sh: delete_script removes the menu and exits with the uninstall'"'"'s status'
mutate "menu: a failed removal reads as success" x-ui.sh uninstall 'LOGE "Could not remove ${p}"
            rc=1' 'LOGE "Could not remove ${p}"' \
    'x-ui.sh: uninstall exits 1 when something could not be removed'
mutate "menu: migrate.sh's x-ui wrapper left behind" x-ui.sh uninstall "if grep -qs \"Installed by still-ui's migrate.sh\" /usr/bin/x-ui; then" 'if false; then' \
    'x-ui.sh: uninstall removes migrate.sh'"'"'s x-ui wrapper and nothing else named x-ui'
mutate "menu: any x-ui command removed" x-ui.sh uninstall "if grep -qs \"Installed by still-ui's migrate.sh\" /usr/bin/x-ui; then" 'if [[ -e /usr/bin/x-ui ]]; then' \
    'x-ui.sh: uninstall removes migrate.sh'"'"'s x-ui wrapper and nothing else named x-ui'
mutate "menu: uninstall purges PostgreSQL for SQLite panels" x-ui.sh uninstall \
    'if [[ "$panel_used_postgres" == "true" ]] && postgresql_installed; then' 'if postgresql_installed; then' \
    'x-ui.sh: the PostgreSQL purge is offered only when deleting the data of a PostgreSQL panel'
mutate "menu: legacy accepts any text" x-ui.sh legacy_version \
    'if [[ ! "$tag_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then' 'if false; then' \
    'x-ui.sh: legacy accepts only a version number and runs the latest installer with it'
mutate "menu: settings asks for the IP forever without a terminal" x-ui.sh check_config \
    'if ! read -rp "Please enter your server'"'"'s public IPv4 address: " server_ip && [[ -z "$server_ip" ]]; then' 'read -rp "Please enter your server'"'"'s public IPv4 address: " server_ip; if false; then' \
    'x-ui.sh: '"'"'still-ui settings'"'"' without internet and without a terminal finishes'
mutate "menu: settings drops an address on a last line without newline" x-ui.sh check_config \
    'server_ip && [[ -z "$server_ip" ]]; then' 'server_ip; then' \
    'x-ui.sh: '"'"'still-ui settings'"'"' without internet and without a terminal finishes'
mutate "menu: short sums accepted in SHA256SUMS" x-ui.sh still_sha256_of ' && length($1) == 64' '' \
    'x-ui.sh: SHA256SUMS lookup takes only well-formed sums (text and binary mode)'
mutate "menu: no unit reads as running" x-ui.sh check_status '            return 2
        fi
        temp=$(systemctl' '            :
        fi
        temp=$(systemctl' \
    'x-ui.sh: check_status tells installed, running and not running apart'

wait
for i in $(seq 1 "$n"); do cat "$work/$i.res"; done
caught=$(cat "$work"/*.res | grep -c '^caught')
missed=$(cat "$work"/*.res | grep -c '^MISSED')
broken=$(cat "$work"/*.res | grep -c '^BROKEN')
echo
echo "$caught caught, $missed missed, $broken broken (of $n)"
[[ $missed == 0 && $broken == 0 && $caught == "$n" ]]
