#!/bin/bash

red='\033[0;31m'
green='\033[0;32m'
blue='\033[0;34m'
yellow='\033[0;33m'
plain='\033[0m'

xui_folder="${XUI_MAIN_FOLDER:=/usr/local/still-ui}"
xui_service="${XUI_SERVICE:=/etc/systemd/system}"

# Don't edit this config
b_source="${BASH_SOURCE[0]}"
while [ -h "$b_source" ]; do
    b_dir="$(cd -P "$(dirname "$b_source")" > /dev/null 2>&1 && pwd || pwd -P)"
    b_source="$(readlink "$b_source")"
    [[ $b_source != /* ]] && b_source="$b_dir/$b_source"
done
cur_dir="$(cd -P "$(dirname "$b_source")" > /dev/null 2>&1 && pwd || pwd -P)"
script_name=$(basename "$0")

# Check command exist function
_command_exists() {
    type "$1" &> /dev/null
}

# Fail, log and exit script function
_fail() {
    local msg=${1}
    echo -e "${red}${msg}${plain}"
    exit 2
}

# Records this run's outcome for the panel's web updater to poll, since it
# launches this script detached and has no other way to learn whether it
# finished. Written to a fixed path outside XUI_MAIN_FOLDER so it survives
# the update regardless of what happens to that folder. The EXIT trap below
# covers every exit path in this file, including the bare `exit 1`/`exit 2`
# calls that don't go through _fail.
xui_update_run_id="${XUI_UPDATE_RUN_ID:-0}"
[[ "${xui_update_run_id}" =~ ^[0-9]+$ ]] || xui_update_run_id="0"
xui_update_status_file="${XUI_UPDATE_STATUS_FILE:-/etc/still-ui/update-status.json}"

_write_update_status() {
    local state="$1"
    local exit_code="$2"
    local status_dir
    status_dir="$(dirname "${xui_update_status_file}")"
    mkdir -p "${status_dir}" > /dev/null 2>&1
    local tmp_file="${xui_update_status_file}.tmp.$$"
    printf '{"runId":"%s","state":"%s","exitCode":%s,"finishedAt":%s}\n' \
        "${xui_update_run_id}" "${state}" "${exit_code}" "$(date +%s)" > "${tmp_file}" 2> /dev/null
    mv -f "${tmp_file}" "${xui_update_status_file}" > /dev/null 2>&1
}

# Set by update_x-ui once the running panel is stopped / its files are being
# replaced; any non-zero exit after that puts the previous install back and
# starts the panel again (see _update_restore_and_start).
update_stopped=0
update_rollback_files=0
update_backup_dir=""
update_stage_dir=""
update_backup_paths=()

_report_update_exit() {
    local code=$?
    if [[ "${code}" -ne 0 ]]; then
        _update_restore_and_start
    fi
    [[ -n "${update_stage_dir}" ]] && rm -rf "${update_stage_dir}"
    [[ -n "${update_backup_dir}" ]] && rm -rf "${update_backup_dir}"
    if [[ "${code}" -eq 0 ]]; then
        _write_update_status "success" "0"
    else
        _write_update_status "failed" "${code}"
    fi
}

# Undoes a failed update after the panel was stopped: the replaced files come
# back from the backup while the database is not yet migrated, the service
# unit or init script comes back if the new one is missing, and the panel is
# started again. Never exits, so it is safe inside the EXIT trap.
_update_restore_and_start() {
    [[ "${update_stopped}" == "1" ]] || return 0
    local p unit
    if [[ "${update_rollback_files}" == "1" && -n "${update_backup_dir}" && -n "${xui_folder}" ]]; then
        echo -e "${yellow}Update failed, restoring the previous still-ui version...${plain}"
        for p in "${update_backup_paths[@]}"; do
            rm -rf "${xui_folder:?}/${p}"
            if [[ -e "${update_backup_dir}/files/${p}" || -L "${update_backup_dir}/files/${p}" ]]; then
                mkdir -p "$(dirname "${xui_folder}/${p}")"
                cp -a "${update_backup_dir}/files/${p}" "${xui_folder}/${p}"
            fi
        done
        [[ -s "${update_backup_dir}/still-ui.cli" ]] && cp -a "${update_backup_dir}/still-ui.cli" /usr/bin/still-ui
    fi
    if [[ $release == "alpine" ]]; then
        unit="/etc/init.d/still-ui"
    else
        unit="${xui_service}/still-ui.service"
    fi
    if [[ -n "${update_backup_dir}" && -s "${update_backup_dir}/service-unit" ]] &&
        [[ "${update_rollback_files}" == "1" || ! -s "${unit}" ]]; then
        cp -a "${update_backup_dir}/service-unit" "${unit}"
    fi
    echo -e "${yellow}Starting still-ui again...${plain}"
    if [[ $release == "alpine" ]]; then
        rc-update add still-ui > /dev/null 2>&1
        rc-service still-ui start > /dev/null 2>&1
    else
        systemctl daemon-reload > /dev/null 2>&1
        systemctl enable still-ui > /dev/null 2>&1
        systemctl start still-ui > /dev/null 2>&1
    fi
}

# Extracts the release archive into a scratch folder and checks it carries
# what the update installs, before anything of the running panel is touched.
_stage_update_archive() {
    local archive="$1" f
    update_stage_dir="$(mktemp -d "${xui_folder%/*}/.still-ui-update.XXXXXX")" ||
        _fail "ERROR: Cannot create a scratch folder next to ${xui_folder}; the running panel was not touched"
    if ! tar -xzf "${archive}" -C "${update_stage_dir}" > /dev/null 2>&1; then
        rm -f "${archive}"
        _fail "ERROR: Failed to extract the still-ui release archive; the running panel was not touched, try the update again"
    fi
    for f in x-ui x-ui.sh; do
        [[ -s "${update_stage_dir}/still-ui/${f}" ]] ||
            _fail "ERROR: The still-ui release archive has no ${f}; the running panel was not touched"
    done
    if [[ $release == "alpine" && ! -s "${update_stage_dir}/still-ui/x-ui.rc" ]]; then
        _fail "ERROR: The still-ui release archive has no x-ui.rc; the running panel was not touched"
    fi
    rm -rf "${update_stage_dir}"
    update_stage_dir=""
}

# Copies every file the update removes or overwrites, so a failure after the
# stop can put the previous version back.
_backup_current_install() {
    local p
    update_backup_paths=(x-ui x-ui.sh x-ui.rc still-ui.service x-ui.service.debian x-ui.service.arch x-ui.service.rhel
        "bin/xray-linux-$(arch)" bin/xray-linux-arm32 "bin/mtg-linux-$(arch)" bin/mtg-linux-arm
        bin/README.md bin/LICENSE bin/tuic-server bin/tuic)
    update_backup_dir="$(mktemp -d "${xui_folder%/*}/.still-ui-backup.XXXXXX")" ||
        _fail "ERROR: Cannot create a backup folder next to ${xui_folder}; the running panel was not touched"
    for p in "${update_backup_paths[@]}"; do
        [[ -e "${xui_folder}/${p}" || -L "${xui_folder}/${p}" ]] || continue
        mkdir -p "$(dirname "${update_backup_dir}/files/${p}")" &&
            cp -a "${xui_folder}/${p}" "${update_backup_dir}/files/${p}" ||
            _fail "ERROR: Cannot back up ${xui_folder}/${p} (disk full?); the running panel was not touched"
    done
    if [[ -e /usr/bin/still-ui ]]; then
        cp -a /usr/bin/still-ui "${update_backup_dir}/still-ui.cli" ||
            _fail "ERROR: Cannot back up /usr/bin/still-ui; the running panel was not touched"
    fi
    if [[ $release == "alpine" ]]; then
        cp -a /etc/init.d/still-ui "${update_backup_dir}/service-unit"
    else
        cp -a "${xui_service}/still-ui.service" "${update_backup_dir}/service-unit"
    fi || _fail "ERROR: Cannot back up the still-ui service unit; the running panel was not touched"
}
trap _report_update_exit EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

# check root
[[ $EUID -ne 0 ]] && _fail "FATAL ERROR: Please run this script with root privilege."

if _command_exists curl; then
    curl_bin=$(which curl)
else
    _fail "ERROR: Command 'curl' not found."
fi
# The panel sets XUI_UPDATE_PROXY to its egress bridge's socks5h URL for the GitHub downloads.
# The IP lookups use plain curl so they still report this server's own address.
[[ -n "${XUI_UPDATE_PROXY}" ]] && curl_bin="${curl_bin} --proxy ${XUI_UPDATE_PROXY}"

# Check OS and set release variable
if [[ -f /etc/os-release ]]; then
    source /etc/os-release
    release=$ID
elif [[ -f /usr/lib/os-release ]]; then
    source /usr/lib/os-release
    release=$ID
else
    _fail "Failed to check the system OS, please contact the author!"
fi
echo "The OS release is: $release"

arch() {
    case "$(uname -m)" in
        x86_64 | x64 | amd64) echo 'amd64' ;;
        i*86 | x86) echo '386' ;;
        armv8* | armv8 | arm64 | aarch64) echo 'arm64' ;;
        armv7* | armv7 | arm) echo 'armv7' ;;
        armv6* | armv6) echo 'armv6' ;;
        armv5* | armv5) echo 'armv5' ;;
        s390x) echo 's390x' ;;
        *) echo -e "${red}Unsupported CPU architecture!${plain}" && rm -f "${cur_dir}/${script_name}" > /dev/null 2>&1 && exit 2 ;;
    esac
}

echo "Arch: $(arch)"

gen_random_string() {
    local length="$1"
    openssl rand -base64 $((length * 2)) \
        | tr -dc 'a-zA-Z0-9' \
        | head -c "$length"
}

xui_env_file_path() {
    case "${release}" in
        ubuntu | debian | armbian)
            echo "/etc/default/still-ui"
            ;;
        arch | manjaro | parch | alpine)
            echo "/etc/conf.d/still-ui"
            ;;
        *)
            echo "/etc/sysconfig/still-ui"
            ;;
    esac
}

load_xui_env() {
    local env_file
    env_file="$(xui_env_file_path)"
    if [[ -r "$env_file" ]]; then
        set -a
        # shellcheck disable=SC1090
        source "$env_file"
        set +a
    fi
}

install_base() {
    echo -e "${green}Updating and install dependency packages...${plain}"
    case "${release}" in
        ubuntu | debian | armbian)
            apt-get update > /dev/null 2>&1 && apt-get install -y -q cron curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
        fedora | amzn | virtuozzo | rhel | almalinux | rocky | ol)
            dnf makecache -y > /dev/null 2>&1 && dnf install -y -q cronie curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
        centos)
            if [[ "${VERSION_ID}" =~ ^7 ]]; then
                yum makecache -y > /dev/null 2>&1 && yum install -y -q cronie curl tar tzdata socat openssl > /dev/null 2>&1
            else
                dnf makecache -y > /dev/null 2>&1 && dnf install -y -q cronie curl tar tzdata socat openssl > /dev/null 2>&1
            fi
            ;;
        arch | manjaro | parch)
            pacman -Sy --noconfirm cronie curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
        opensuse-tumbleweed | opensuse-leap)
            zypper refresh > /dev/null 2>&1 && zypper -q install -y cron curl tar timezone socat openssl > /dev/null 2>&1
            ;;
        alpine)
            apk update > /dev/null 2>&1 && apk add dcron curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
        *)
            apt-get update > /dev/null 2>&1 && apt install -y -q cron curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
    esac
}

config_after_update() {
    local panel_needs_restart=0

    echo -e "${yellow}still-ui settings:${plain}"
    ${xui_folder}/x-ui setting -show true
    ${xui_folder}/x-ui migrate

    # Properly detect empty cert by checking if cert: line exists and has content after it
    local existing_cert=$(${xui_folder}/x-ui setting -getCert true 2> /dev/null | grep 'cert:' | awk -F': ' '{print $2}' | tr -d '[:space:]')
    local settings existing_webBasePath
    if ! settings=$("${xui_folder}/x-ui" setting -show true) ||
        ! existing_webBasePath=$(printf '%s\n' "$settings" | awk '
            /^get .* failed, error info:/ { failed = 1 }
            /^hasDefaultCredential: (true|false)$/ { credentials++ }
            /^port: [0-9]+$/ { ports++ }
            /^webBasePath:/ { paths++; sub(/^webBasePath: ?/, ""); path = $0 }
            END {
                if (failed || credentials != 1 || ports != 1 || paths != 1 || (path != "" && path !~ /^\//)) exit 1
                print path
            }'); then
        echo -e "${red}Unable to read webBasePath; leaving existing settings unchanged.${plain}" >&2
        return 1
    fi
    existing_webBasePath=${existing_webBasePath#/}
    local existing_port=$(printf '%s\n' "$settings" | awk '/^port: / {print $2}')

    # Get server IP
    local URL_lists=(
        "https://api4.ipify.org"
        "https://ipv4.icanhazip.com"
        "https://v4.api.ipinfo.io/ip"
        "https://ipv4.myexternalip.com/raw"
        "https://4.ident.me"
        "https://check-host.net/ip"
    )
    local server_ip=""
    for ip_address in "${URL_lists[@]}"; do
        local response=$(curl -s -w "\n%{http_code}" --max-time 3 "${ip_address}" 2> /dev/null)
        local http_code=$(echo "$response" | tail -n1)
        local ip_result=$(echo "$response" | head -n-1 | tr -d '[:space:]"')
        if [[ "${http_code}" == "200" && "${ip_result}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            server_ip="${ip_result}"
            break
        fi
    done

    # Only used to print the access URL; never prompt, the web updater has no TTY.
    if [[ -z "$server_ip" ]]; then
        server_ip=$(hostname -I 2> /dev/null | awk '{print $1}')
        server_ip="${server_ip:-<server-ip>}"
    fi

    # Only an unset or default root path needs initialization.
    if [[ -z "$existing_webBasePath" ]]; then
        echo -e "${yellow}WebBasePath is missing or set to root. Generating a new one...${plain}"
        local config_webBasePath=$(gen_random_string 18)
        ${xui_folder}/x-ui setting -webBasePath "${config_webBasePath}"
        existing_webBasePath="${config_webBasePath}"
        panel_needs_restart=1
        echo -e "${green}New WebBasePath: ${config_webBasePath}${plain}"
    fi

    # An update only updates the panel: TLS stays exactly as configured. Set it up
    # explicitly from the x-ui menu (SSL Certificate Management).
    local access_scheme="http" access_host="${server_ip}"
    if [[ -n "$existing_cert" ]]; then
        access_scheme="https"
        access_host=$(basename "$(dirname "$existing_cert")")
    fi
    echo ""
    echo -e "${green}═══════════════════════════════════════════${plain}"
    echo -e "${green}     Panel Access Information              ${plain}"
    echo -e "${green}═══════════════════════════════════════════${plain}"
    echo -e "${green}Access URL: ${access_scheme}://${access_host}:${existing_port}/${existing_webBasePath}${plain}"
    echo -e "${green}═══════════════════════════════════════════${plain}"
    if [[ -z "$existing_cert" ]]; then
        echo -e "${yellow}No SSL certificate is configured; the panel is served over HTTP.${plain}"
        echo -e "${yellow}Run 'still-ui' and choose SSL Certificate Management to set one up.${plain}"
    fi

    if [[ "$panel_needs_restart" -eq 1 ]]; then
        echo -e "${yellow}Restarting panel to apply the new web base path...${plain}"
        systemctl restart still-ui 2> /dev/null || rc-service still-ui restart 2> /dev/null
    fi
}

# setup_fail2ban auto-installs and configures fail2ban for the IP Limit feature
# by invoking the freshly downloaded x-ui CLI. IP Limit is load-bearing on
# fail2ban (without it the panel disables the limitIp field and zeroes existing
# limits), so updating an older install should make it work without a manual
# trip through the IP Limit menu. Non-fatal: a fail2ban failure must never abort
# the update. XUI_ENABLE_FAIL2BAN is honored (load_xui_env exports it from the
# persisted env file, so a deliberate opt-out survives updates).
setup_fail2ban() {
    if [[ -n "${XUI_ENABLE_FAIL2BAN+x}" && "${XUI_ENABLE_FAIL2BAN}" != "true" ]]; then
        echo -e "${yellow}XUI_ENABLE_FAIL2BAN=${XUI_ENABLE_FAIL2BAN}, skipping Fail2ban auto-setup.${plain}"
        return 0
    fi

    if [[ ! -x /usr/bin/still-ui ]]; then
        echo -e "${yellow}still-ui CLI not found; skipping Fail2ban auto-setup.${plain}"
        return 0
    fi

    # Scripts older than v3.4.0 have no setup-fail2ban and exit 0 from the
    # usage banner, which would read as success here.
    if ! grep -q '"setup-fail2ban")' /usr/bin/still-ui; then
        echo -e "${yellow}This x-ui.sh predates 'still-ui setup-fail2ban'; skipping Fail2ban auto-setup.${plain}"
        return 0
    fi

    echo -e "${green}Setting up Fail2ban for the IP Limit feature...${plain}"
    if /usr/bin/still-ui setup-fail2ban; then
        echo -e "${green}Fail2ban setup complete.${plain}"
    else
        echo -e "${yellow}Fail2ban setup did not finish; IP Limit stays disabled until you run 'still-ui' and open the IP Limit menu. Continuing.${plain}"
    fi
    return 0
}

# The hardened unit makes /usr, /boot, /efi and /etc read-only. The panel's own
# updater is expected to escape that sandbox by running this script through a
# transient systemd-run unit; when systemd-run is unavailable it starts this
# script as a plain child instead, and that child inherits the sandbox and then
# cannot write anything this update needs. Say so once, up front, instead of
# dying partway through with "Failed to download x-ui".
require_writable_update_paths() {
    local dir probe
    for dir in "${xui_folder%/*}" "/usr/bin"; do
        [[ -n "$dir" && -d "$dir" ]] || continue
        probe="${dir}/.x-ui-write-test.$$"
        # A real write test rather than [[ -w ]]: this runs as root, where a
        # permission bit means little and the test only reflects the file mode
        # and the mount flags, not an immutable attribute or a full filesystem.
        if ! : > "$probe" 2> /dev/null; then
            _fail "ERROR: ${dir} is not writable for this process (read-only mount, attribute or full filesystem). The panel's fallback updater cannot run inside the hardened systemd sandbox; update from the panel UI (which uses systemd-run) or run 'still-ui update' in a shell."
        fi
        rm -f "$probe"
    done
}

# Major version of the local systemd, 0 when it cannot be determined. The
# SystemCallFilter=@system-service group only exists from systemd 239 on (other
# @-named groups exist since 231); on older versions an unknown group is not
# ignored safely, the filter stays in force and leaves a whitelist the panel
# cannot run under.
_xui_systemd_major_version() {
    local version=""
    if command -v systemctl > /dev/null 2>&1; then
        version="$(systemctl --version 2>/dev/null | awk 'NR == 1 {print $2}')"
    fi
    if [[ ! "$version" =~ ^[0-9]+$ ]]; then
        echo 0
        return 0
    fi
    echo "$version"
}

# The shipped units list hardening that older systemd does not know: the
# directive is logged and ignored at load time rather than rejected, so the
# panel still starts, only without that protection. Each entry is the systemd
# release that introduced the directive (systemd.exec(5)); everything else in
# the unit predates the oldest systemd install.sh supports (CentOS 7 has 219).
# SystemCallFilter= is listed because the drop-in only writes it from 239 on.
_xui_warn_unsupported_hardening() {
    local version entry missing=""
    version="$(_xui_systemd_major_version)"
    [[ "$version" -gt 0 ]] || return 0
    for entry in RestrictRealtime:231 ReadWritePaths:231 ProtectKernelTunables:232 \
        ProtectKernelModules:232 RestrictNamespaces:233 LockPersonality:235 \
        SystemCallFilter:239 ProtectHostname:242 RestrictSUIDSGID:242 \
        ProtectKernelLogs:244 ProtectClock:245; do
        if [[ "$version" -lt "${entry##*:}" ]]; then
            missing="${missing:+$missing, }${entry%%:*} (${entry##*:})"
        fi
    done
    [[ -n "$missing" ]] || return 0
    echo -e "${yellow}Note: systemd ${version} ignores part of the hardening in still-ui.service; the panel still starts.${plain}"
    echo "      Not applied, needs a newer systemd: ${missing}."
    if [[ "$version" -lt 231 ]]; then
        echo "      The panel's folders stay writable through ReadWriteDirectories=, the alias this script installs."
    fi
    echo "      The rest of the hardening is in force. Upgrade systemd to apply the above."
    return 0
}

# Sets KEY=value in an env file (drops the line for an empty value), keeping other lines.
_still_env_set() {
    local file="$1" key="$2" value="$3" temp_file
    [[ -n "$file" ]] || return 1
    mkdir -p "$(dirname "$file")" || return 1
    [[ -e "$file" ]] || { : > "$file" && chmod 600 "$file"; } || return 1
    temp_file="${file}.tmp.$$"
    grep -v "^[[:space:]]*\(export[[:space:]]\+\)\?${key}=" "$file" > "$temp_file"
    [[ -n "$value" ]] && printf '%s=%s\n' "$key" "$value" >> "$temp_file"
    chmod --reference="$file" "$temp_file" 2> /dev/null || chmod 600 "$temp_file"
    mv -f "$temp_file" "$file"
}

# Size like 800M, 2G or 1048576 (bytes) in MiB; empty for anything else.
_still_size_mib() {
    local v="$1" n
    [[ "$v" =~ ^([0-9]+)([KMGT]?)$ ]] || return 0
    n="${BASH_REMATCH[1]}"
    case "${BASH_REMATCH[2]}" in
        K) echo $((n / 1024)) ;;
        M) echo "$n" ;;
        G) echo $((n * 1024)) ;;
        T) echo $((n * 1024 * 1024)) ;;
        *) echo $((n / 1048576)) ;;
    esac
}

# still-xray (opt-in, STILL_XRAY_UNIT=on in the env file or the environment): Xray runs as a
# systemd unit of its own, under an unprivileged user, so a restart, update or crash of the
# panel never drops a client. Without it Xray is the panel's child process, as in 3x-ui.
# STILL_XRAY_MEMORY_MAX (e.g. 1G, default half the RAM within 256M..4G) caps the core.
_still_xray_unit() {
    local env_file setting template unit temp_file xray_bin bin_dir dropin_dir dropin
    local mem_kb max_mb gomem_mb
    [[ -n "${xui_service:-}" ]] || return 0
    command -v systemctl > /dev/null 2>&1 || return 0
    case "${release}" in
        ubuntu | debian | armbian) env_file="/etc/default/still-ui" ;;
        arch | manjaro | parch | alpine) env_file="/etc/conf.d/still-ui" ;;
        *) env_file="/etc/sysconfig/still-ui" ;;
    esac
    setting="${STILL_XRAY_UNIT:-}"
    if [[ -z "$setting" && -r "$env_file" ]]; then
        setting=$(sed -n 's/^[[:space:]]*\(export[[:space:]]\+\)\?STILL_XRAY_UNIT=["'"'"']\?\([^"'"'"' ]*\).*/\2/p' "$env_file" | tail -n 1)
    fi
    template="${xui_folder}/still-xray.service"
    xray_bin=$(ls "${xui_folder}"/bin/xray-linux-* 2> /dev/null | head -n 1)
    if [[ "$setting" != "on" || ! -s "$template" || ! -x "$xray_bin" ]]; then
        _still_env_set "$env_file" XUI_XRAY_UNIT ""
        if systemctl is-enabled still-xray > /dev/null 2>&1; then
            systemctl disable --now still-xray > /dev/null 2>&1
        fi
        echo -e "${yellow}Xray runs inside the panel process, as in 3x-ui. STILL_XRAY_UNIT=on in ${env_file} runs it as its own service still-xray.${plain}"
        return 0
    fi
    _still_env_set "$env_file" STILL_XRAY_UNIT on || return 1
    bin_dir="${xui_folder}/bin"
    if ! id still-xray > /dev/null 2>&1; then
        useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin --user-group still-xray > /dev/null 2>&1 \
            || useradd -r -M -d /nonexistent -s /sbin/nologin -U still-xray > /dev/null 2>&1 \
            || { echo -e "${red}Could not create the still-xray user; Xray stays inside the panel.${plain}"; _still_env_set "$env_file" XUI_XRAY_UNIT ""; return 1; }
    fi
    # setgid folders: what the panel (root) writes there is readable by the core's group only.
    install -d -o root -g still-xray -m 2750 /etc/still-xray /etc/still-xray/certs || return 1
    chgrp -R still-xray /etc/still-xray > /dev/null 2>&1
    find /etc/still-xray -type f ! -name panel-meta.json -exec chmod 0640 {} + > /dev/null 2>&1
    # Older panels wrote geo files 0600 (UMask=0077); the core reads them as still-xray.
    chmod a+r "${bin_dir}"/*.dat > /dev/null 2>&1

    unit="${xui_service}/still-xray.service"
    temp_file="${unit}.tmp.$$"
    sed -e "s#@XRAY_BIN@#${xray_bin}#g" -e "s#@XRAY_ASSETS@#${bin_dir}#g" "$template" > "$temp_file" \
        && chmod 644 "$temp_file" && mv -f "$temp_file" "$unit" \
        || { rm -f "$temp_file"; echo -e "${red}Could not write ${unit}.${plain}"; return 1; }

    setting="${STILL_XRAY_MEMORY_MAX:-}"
    if [[ -z "$setting" && -r "$env_file" ]]; then
        setting=$(sed -n 's/^[[:space:]]*\(export[[:space:]]\+\)\?STILL_XRAY_MEMORY_MAX=["'"'"']\?\([^"'"'"' ]*\).*/\2/p' "$env_file" | tail -n 1)
    fi
    dropin_dir="${xui_service}/still-xray.service.d"
    dropin="${dropin_dir}/15-still-memory.conf"
    mkdir -p "$dropin_dir" || return 1
    if [[ "$setting" == "off" || "$setting" == "none" || "$setting" == "infinity" ]]; then
        rm -f "$dropin"
        max_mb=""
    else
        max_mb=$(_still_size_mib "$setting")
        if [[ -z "$max_mb" ]]; then
            [[ -n "$setting" ]] && echo -e "${yellow}Ignoring STILL_XRAY_MEMORY_MAX=${setting}: use a size like 800M or 1G, or off.${plain}"
            mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2> /dev/null)
            [[ "$mem_kb" =~ ^[0-9]+$ ]] || mem_kb=1048576
            max_mb=$((mem_kb / 2 / 1024))
            ((max_mb < 256)) && max_mb=256
            ((max_mb > 4096)) && max_mb=4096
        fi
        # Go collects garbage harder near GOMEMLIMIT, well before the cgroup kills the core.
        gomem_mb=$((max_mb * 85 / 100))
        temp_file="${dropin}.tmp.$$"
        {
            echo "# Written by still-ui install.sh/update.sh; edits here are lost."
            echo "# Change it with STILL_XRAY_MEMORY_MAX in ${env_file} and re-run the update."
            echo "[Service]"
            echo "MemoryMax=${max_mb}M"
            echo "MemorySwapMax=0"
            echo "Environment=GOMEMLIMIT=${gomem_mb}MiB"
        } > "$temp_file" && chmod 644 "$temp_file" && mv -f "$temp_file" "$dropin" || rm -f "$temp_file"
    fi

    _still_env_set "$env_file" XUI_XRAY_UNIT still-xray || return 1
    systemctl daemon-reload > /dev/null 2>&1 || true
    systemctl enable still-xray > /dev/null 2>&1 || true
    echo -e "${green}Xray runs as its own service still-xray${max_mb:+ (MemoryMax=${max_mb}M)}: restarting or updating the panel keeps clients connected.${plain}"
    return 0
}


# still-ui: memory ceiling for the panel service.
# The panel and the Xray it supervises share one cgroup. Without a ceiling a
# leak or a burst of clients grows it until the host thrashes swap and stops
# answering (SSH included). A drop-in caps it: under MemoryHigh the kernel
# reclaims, at MemoryMax the OOM killer stops the service and systemd restarts
# it (Restart=on-failure) a few seconds later -- the host stays reachable.
# MemorySwapMax=0 keeps the panel out of swap, where it would only thrash.
# Size: 35% of RAM, at least 256 MiB, at most 1 GiB. Override in the env file:
# STILL_UI_MEMORY_MAX=800M (any systemd size) or STILL_UI_MEMORY_MAX=off.
# 15-still-memory.conf sorts before a local 20-*.conf, so a local drop-in wins.
_still_memory_dropin() {
    local env_file="${1:-}" dropin_dir dropin temp_file mem_kb max_mb high_mb max high setting
    [[ -n "${xui_service:-}" ]] || return 0
    command -v systemctl > /dev/null 2>&1 || return 0
    if [[ -z "$env_file" ]]; then
        case "${release}" in
            ubuntu | debian | armbian) env_file="/etc/default/still-ui" ;;
            arch | manjaro | parch | alpine) env_file="/etc/conf.d/still-ui" ;;
            *) env_file="/etc/sysconfig/still-ui" ;;
        esac
    fi
    setting="${STILL_UI_MEMORY_MAX:-}"
    if [[ -z "$setting" && -r "$env_file" ]]; then
        setting=$(sed -n 's/^[[:space:]]*\(export[[:space:]]\+\)\?STILL_UI_MEMORY_MAX=["'"'"']\?\([^"'"'"' ]*\).*/\2/p' "$env_file" | tail -n 1)
    fi
    dropin_dir="${xui_service}/still-ui.service.d"
    dropin="${dropin_dir}/15-still-memory.conf"
    if [[ "$setting" == "off" || "$setting" == "none" || "$setting" == "infinity" ]]; then
        rm -f "$dropin"
        echo -e "${yellow}Panel memory ceiling disabled (STILL_UI_MEMORY_MAX=${setting}).${plain}"
        systemctl daemon-reload > /dev/null 2>&1 || true
        return 0
    fi
    if [[ -n "$setting" ]]; then
        if [[ ! "$setting" =~ ^[0-9]+[KMGT]?$ ]]; then
            echo -e "${yellow}Ignoring STILL_UI_MEMORY_MAX=${setting}: use a size like 800M or 1G, or off.${plain}"
            setting=""
        fi
    fi
    if [[ -n "$setting" ]]; then
        max="$setting"
        high=""
    else
        mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2> /dev/null)
        [[ "$mem_kb" =~ ^[0-9]+$ ]] || return 0
        max_mb=$((mem_kb * 35 / 100 / 1024))
        ((max_mb < 256)) && max_mb=256
        ((max_mb > 1024)) && max_mb=1024
        high_mb=$((max_mb * 85 / 100))
        max="${max_mb}M"
        high="${high_mb}M"
    fi
    mkdir -p "$dropin_dir" || return 1
    temp_file="${dropin}.tmp.$$"
    {
        echo "# Written by still-ui install.sh/update.sh; edits here are lost."
        echo "# Change it with STILL_UI_MEMORY_MAX in ${env_file} and re-run the"
        echo "# update, or put your own values in a 20-*.conf drop-in."
        echo "[Service]"
        [[ -n "$high" ]] && echo "MemoryHigh=${high}"
        echo "MemoryMax=${max}"
        echo "MemorySwapMax=0"
    } > "$temp_file" || { rm -f "$temp_file"; return 1; }
    chmod 644 "$temp_file"
    mv -f "$temp_file" "$dropin" || { rm -f "$temp_file"; return 1; }
    systemctl daemon-reload > /dev/null 2>&1 || true
    echo -e "${green}Panel memory ceiling: MemoryMax=${max}${high:+, MemoryHigh=${high}} (${dropin}).${plain}"
    return 0
}

# ProtectSystem=full makes /usr, /boot, /efi and /etc read-only. ProtectSystem=
# strict would make the whole hierarchy read-only (only the kernel API
# filesystems stay as they are), and that would break the panel's own use of
# /tmp. The panel's stores are configurable (XUI_DB_FOLDER, XUI_LOG_FOLDER,
# XUI_BIN_FOLDER), and XUI_MAIN_FOLDER is the folder install.sh/update.sh place
# the files in -- the unit's WorkingDirectory on a stock install, and what a
# relative XUI_BIN_FOLDER is resolved against. So a hard-coded list in the unit
# either misses a relocated store -- the panel then cannot write its own SQLite
# database and sits in a Restart=on-failure loop -- or forces the operator to
# edit a file that every install/update overwrites from the release tarball.
# install.sh and update.sh therefore regenerate the drop-in from the folders
# actually in use, and the unit's own ReadWritePaths only carry the
# plain-install defaults. A relocated store means re-running install or update:
# the drop-in is only written here.
_xui_service_write_paths_dropin() {
    # $1 is the env file to resolve the XUI_* folders from; callers pass nothing
    # and get the OS-specific path the unit itself uses.
    local env_file="${1:-}"
    local dropin_dir dropin temp_file
    local db_folder log_folder bin_folder main_folder
    local path line="" whitespace_paths="" seen_paths="" escaped_path

    if [[ -z "$env_file" ]]; then
        env_file="$(xui_env_file_path)"
    fi
    if [[ -r "$env_file" ]]; then
        set -a
        # shellcheck disable=SC1090
        source "$env_file"
        set +a
    fi

    # XUI_* wins over the script's own default: the unit hands that same env
    # file to the panel through EnvironmentFile=, so these are the folders it
    # will actually use.
    main_folder="${XUI_MAIN_FOLDER:-${xui_folder}}"
    db_folder="${XUI_DB_FOLDER:-/etc/still-ui}"
    log_folder="${XUI_LOG_FOLDER:-/var/log/still-ui}"
    # An empty XUI_BIN_FOLDER resolves to "bin" relative to the panel's working
    # directory, which the unit sets to the main folder.
    bin_folder="${XUI_BIN_FOLDER:-bin}"
    if [[ "$bin_folder" != /* ]]; then
        bin_folder="${main_folder%/}/${bin_folder#./}"
    fi

    for path in "$db_folder" "$log_folder" "$bin_folder" "$main_folder" /etc/still-xray; do
        [[ "$path" == /* ]] || continue
        # ReadWritePaths= is a whitespace-separated list, and a folder whose
        # name contains whitespace cannot be written into it without relying on
        # quoting. A wrong entry makes systemd reject the whole drop-in and the
        # panel would not start, so leave such a folder out and say so instead.
        if [[ "$path" != "${path//[[:space:]]/}" ]]; then
            whitespace_paths="${whitespace_paths:+$whitespace_paths }$path"
            continue
        fi
        case " $seen_paths " in
            *" $path "*) continue ;;
        esac
        seen_paths="${seen_paths}${seen_paths:+ }$path"
        # systemd expands %-specifiers in unit files, so a folder name carrying
        # a literal % has to be written as %%, or the entry stops naming the
        # folder systemd is meant to keep writable.
        escaped_path="${path//%/%%}"
        line="${line} -${escaped_path}"
    done
    if [[ -n "$whitespace_paths" ]]; then
        echo "Warning: these folders contain whitespace and were left out of" >&2
        echo "         10-xui-sandbox.conf: $whitespace_paths" >&2
        echo "         The panel cannot write to them under the unit's sandbox." >&2
    fi
    line="${line# }"
    [[ -n "$line" ]] || return 1

    dropin_dir="${xui_service}/still-ui.service.d"
    dropin="${dropin_dir}/10-xui-sandbox.conf"
    temp_file="${dropin}.tmp.$$"

    mkdir -p "$dropin_dir" || return 1
    cat > "$temp_file" << EOF
# Regenerated by install.sh/update.sh on every install and update: edits here
# are lost, and the list only reflects the XUI_* variables read from
# ${env_file} at that moment. Re-run install/update after moving a store.
# It lists the folders the panel writes to. Put local additions in their own
# drop-in, for example 20-x-ui-local.conf, which nothing here touches.
[Service]
ReadWritePaths=${line}
ReadWriteDirectories=${line}
EOF
    if [[ "$(_xui_systemd_major_version)" -ge 239 ]]; then
        cat >> "$temp_file" << 'EOF'
# @system-service needs systemd >= 239; on older versions the unknown group
# would leave the panel with a filter it cannot start under (x-ui.service.*).
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
EOF
    fi
    if [[ ! -s "$temp_file" ]]; then
        rm -f "$temp_file"
        return 1
    fi
    chmod 644 "$temp_file"
    mv -f "$temp_file" "$dropin" || { rm -f "$temp_file"; return 1; }
    if command -v systemctl > /dev/null 2>&1; then
        systemctl daemon-reload > /dev/null 2>&1 || true
    fi
    return 0
}

# Lands a systemd unit file at ${xui_service}/still-ui.service via a temp file +
# atomic mv, so a failed cp/curl or an interrupted mv never leaves a
# truncated unit file at the live path -- systemd would then fail to parse
# it on the next daemon-reload/start. Same pattern already used for
# /usr/bin/still-ui elsewhere in this script. source_is_url picks cp (from a
# file already extracted from the release tarball) vs curl (GitHub fallback).
_install_xui_service_unit() {
    local source="$1"
    local source_is_url="$2"
    local dest="${xui_service}/still-ui.service"
    local temp_file="${dest}.tmp.$$"

    rm -f "$temp_file"
    if [[ "$source_is_url" == "true" ]]; then
        ${curl_bin} -fLRo "$temp_file" "$source" > /dev/null 2>&1
    else
        cp -f "$source" "$temp_file" > /dev/null 2>&1
    fi
    if [[ $? -ne 0 ]]; then
        rm -f "$temp_file"
        return 1
    fi
    if [[ ! -s "$temp_file" ]]; then
        rm -f "$temp_file"
        return 1
    fi
    mv -f "$temp_file" "$dest"
    if [[ $? -ne 0 ]]; then
        rm -f "$temp_file"
        return 1
    fi
    if ! _xui_service_write_paths_dropin; then
        echo -e "${yellow}Warning: could not refresh ${xui_service}/still-ui.service.d/10-xui-sandbox.conf.${plain}"
        echo -e "${yellow}If XUI_DB_FOLDER or XUI_LOG_FOLDER points outside /etc/still-ui and /var/log/still-ui, the panel may not be able to write to it under ProtectSystem=full.${plain}"
    fi
    if ! _still_memory_dropin; then
        echo -e "${yellow}Warning: could not write the panel memory ceiling drop-in.${plain}"
    fi
    _xui_warn_unsupported_hardening
    return 0
}

update_x-ui() {
    cd ${xui_folder%/still-ui}/

    load_xui_env

    if [ -f "${xui_folder}/x-ui" ]; then
        current_xui_version=$(${xui_folder}/x-ui -v)
        echo -e "${green}Current still-ui version: ${current_xui_version}${plain}"
    else
        _fail "ERROR: Current still-ui version: unknown"
    fi

    # STILL_UI_ARCHIVE=<path to still-ui-linux-<arch>.tar.gz> updates from a local archive
    # (an offline server or a test build).
    local local_archive="${STILL_UI_ARCHIVE:-}"
    if [[ -n "${local_archive}" ]]; then
        [[ -s "${local_archive}" ]] || _fail "ERROR: STILL_UI_ARCHIVE=${local_archive} does not exist or is empty"
        cp -f "${local_archive}" "${xui_folder}-linux-$(arch).tar.gz" || _fail "ERROR: cannot copy ${local_archive}"
        tag_version="local archive"
        echo -e "${green}Updating from ${local_archive}${plain}"
    else
        echo -e "${green}Downloading new still-ui version...${plain}"

        # XUI_UPDATE_TAG lets the panel target a specific release tag (e.g. the
        # rolling dev-latest pre-release). Empty keeps the default latest-stable flow.
        if [[ -n "${XUI_UPDATE_TAG}" ]]; then
            tag_version="${XUI_UPDATE_TAG}"
            echo -e "${green}Using update tag: ${tag_version}${plain}"
        else
            tag_version=$(${curl_bin} -Ls "https://api.github.com/repos/Dv2Corn/still-ui/releases/latest" 2> /dev/null | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
            if [[ ! -n "$tag_version" ]]; then
                _fail "ERROR: Failed to fetch still-ui version, it may be due to GitHub API restrictions, please try it later"
            fi
        fi
        echo -e "Got still-ui latest version: ${tag_version}, beginning the installation..."
        # x-ui.sh, x-ui.rc and the unit files ship in every still-ui release archive, built
        # from the same commit as the binary, so nothing is fetched from the repository.
        # Only the rolling dev build tracks main for the GitHub fallback of the unit files.
        script_ref="${tag_version}"
        if [[ "${tag_version}" == "dev-latest" ]]; then
            script_ref="main"
        fi
        ${curl_bin} -fLRo ${xui_folder}-linux-$(arch).tar.gz https://github.com/Dv2Corn/still-ui/releases/download/${tag_version}/still-ui-linux-$(arch).tar.gz 2> /dev/null
        if [[ $? -ne 0 ]]; then
            _fail "ERROR: Failed to download still-ui, please be sure that your server can access GitHub"
        fi
        if [[ ! -s ${xui_folder}-linux-$(arch).tar.gz ]]; then
            rm ${xui_folder}-linux-$(arch).tar.gz -f > /dev/null 2>&1
            _fail "ERROR: Downloaded still-ui release archive is empty, please be sure that your server can access GitHub"
        fi
        # Releases publish <asset>.sha256 next to each archive. A mismatch or a
        # failed sidecar download aborts the update; only a 404 (releases
        # predating the sidecar) is tolerated with a warning.
        archive="${xui_folder}-linux-$(arch).tar.gz"
        rm -f "${archive}.sha256"
        sidecar_code=$(${curl_bin} -sL --retry 3 --retry-delay 3 --connect-timeout 15 --max-time 60 -o "${archive}.sha256" -w '%{http_code}' "https://github.com/Dv2Corn/still-ui/releases/download/${tag_version}/still-ui-linux-$(arch).tar.gz.sha256" 2> /dev/null)
        if [[ "${sidecar_code}" == "200" ]]; then
            expected_sha256=$(awk 'NR == 1 {print $1}' "${archive}.sha256")
            actual_sha256=$(sha256sum "${archive}" | awk '{print $1}')
            rm -f "${archive}.sha256"
            if [[ ! "${expected_sha256}" =~ ^[0-9a-f]{64}$ || "${expected_sha256}" != "${actual_sha256}" ]]; then
                rm -f "${archive}"
                _fail "ERROR: Checksum mismatch for $(basename "${archive}"): expected ${expected_sha256:-<none>}, got ${actual_sha256}"
            fi
            echo -e "${green}Checksum verified: ${actual_sha256}${plain}"
        elif [[ "${sidecar_code}" == "404" ]]; then
            rm -f "${archive}.sha256"
            echo -e "${yellow}No checksum published for this release, skipping verification${plain}"
        else
            rm -f "${archive}.sha256" "${archive}"
            _fail "ERROR: Failed to download the checksum for still-ui-linux-$(arch).tar.gz (HTTP ${sidecar_code})"
        fi
    fi

    if [[ -e ${xui_folder}/ ]]; then
        if [[ $release == "alpine" ]]; then
            if [ ! -f "/etc/init.d/still-ui" ]; then
                rm still-ui-linux-$(arch).tar.gz -f > /dev/null 2>&1
                _fail "ERROR: still-ui service unit not installed."
            fi
        else
            if [ ! -f "${xui_service}/still-ui.service" ]; then
                rm still-ui-linux-$(arch).tar.gz -f > /dev/null 2>&1
                _fail "ERROR: still-ui systemd unit not installed."
            fi
        fi
        # Everything that can fail without a network is checked and backed up
        # while the panel still runs. The unit file / init script stays in place
        # (it is overwritten below), so a failed update can always be retried.
        _stage_update_archive "still-ui-linux-$(arch).tar.gz"
        _backup_current_install
        echo -e "${green}Stopping still-ui...${plain}"
        update_stopped=1
        if [[ $release == "alpine" ]]; then
            rc-service still-ui stop > /dev/null 2>&1
        else
            systemctl stop still-ui > /dev/null 2>&1
        fi
        # Kill any leftover mtg (MTProto) sidecars. x-ui runs them outside its own
        # lifecycle, so on Linux a stale one can survive the stop and keep holding
        # an inbound port with an outdated secret, silently breaking new clients.
        # The new panel respawns a clean mtg per inbound on next start.
        pkill -f 'mtg-linux-[^ ]* run ' > /dev/null 2>&1 || true
        pkill -f 'tuic-server.*-c .*bin/tuic/tuic_[0-9]+\.json' > /dev/null 2>&1 || true
        echo -e "${green}Removing old still-ui version...${plain}"
        update_rollback_files=1
        rm ${xui_folder} -f > /dev/null 2>&1
        rm ${xui_folder}/still-ui.service -f > /dev/null 2>&1
        rm ${xui_folder}/x-ui.service.debian -f > /dev/null 2>&1
        rm ${xui_folder}/x-ui.service.arch -f > /dev/null 2>&1
        rm ${xui_folder}/x-ui.service.rhel -f > /dev/null 2>&1
        rm ${xui_folder}/x-ui -f > /dev/null 2>&1
        rm ${xui_folder}/x-ui.sh -f > /dev/null 2>&1
        echo -e "${green}Removing old mtg version...${plain}"
        rm ${xui_folder}/bin/mtg-linux-$(arch) -f > /dev/null 2>&1
        echo -e "${green}Removing old xray version...${plain}"
        rm ${xui_folder}/bin/xray-linux-$(arch) -f > /dev/null 2>&1
        echo -e "${green}Removing old README and LICENSE file...${plain}"
        rm ${xui_folder}/bin/README.md -f > /dev/null 2>&1
        rm ${xui_folder}/bin/LICENSE -f > /dev/null 2>&1
        rm ${xui_folder}/bin/tuic-server -f > /dev/null 2>&1
        rm ${xui_folder}/bin/tuic -rf > /dev/null 2>&1
    else
        rm still-ui-linux-$(arch).tar.gz -f > /dev/null 2>&1
        _fail "ERROR: still-ui not installed."
    fi

    echo -e "${green}Installing new still-ui version...${plain}"
    tar zxvf still-ui-linux-$(arch).tar.gz > /dev/null 2>&1
    if [[ $? -ne 0 ]]; then
        rm still-ui-linux-$(arch).tar.gz -f > /dev/null 2>&1
        _fail "ERROR: Failed to extract the still-ui release archive; try running the update again"
    fi
    rm still-ui-linux-$(arch).tar.gz -f > /dev/null 2>&1
    cd still-ui > /dev/null 2>&1
    if [[ $? -ne 0 || ! -s x-ui ]]; then
        _fail "ERROR: Extracted still-ui archive is missing the x-ui binary; try running the update again"
    fi
    chmod +x x-ui > /dev/null 2>&1

    # Check the system's architecture and rename the file accordingly.
    # The panel binary maps GOARCH=arm to "arm32" (internal/xray/process.go),
    # so the Xray binary must be named xray-linux-arm32; mtg keeps plain "arm".
    if [[ $(arch) == "armv5" || $(arch) == "armv6" || $(arch) == "armv7" ]]; then
        mv bin/xray-linux-$(arch) bin/xray-linux-arm32 > /dev/null 2>&1
        chmod +x bin/xray-linux-arm32 > /dev/null 2>&1
        if [[ -f bin/mtg-linux-$(arch) ]]; then
            mv bin/mtg-linux-$(arch) bin/mtg-linux-arm > /dev/null 2>&1
            chmod +x bin/mtg-linux-arm > /dev/null 2>&1
        fi
    fi

    chmod +x x-ui bin/xray-linux-$(arch) > /dev/null 2>&1
    if [[ -f bin/mtg-linux-arm ]]; then
        chmod +x bin/mtg-linux-arm > /dev/null 2>&1
    elif [[ -f bin/mtg-linux-$(arch) ]]; then
        chmod +x bin/mtg-linux-$(arch) > /dev/null 2>&1
    fi

    # Nothing downloads after the stop: the panel's Xray, and with it any Panel Traffic
    # Outbound bridge that XUI_UPDATE_PROXY points at, is down from here on.
    echo -e "${green}Installing x-ui.sh script...${plain}"
    local xui_script_temp="/usr/bin/still-ui-temp.$$"
    rm -f "${xui_script_temp}"
    if ! cp -f "${xui_folder}/x-ui.sh" "${xui_script_temp}" > /dev/null 2>&1 || [[ ! -s "${xui_script_temp}" ]]; then
        rm -f "${xui_script_temp}"
        _fail "ERROR: The still-ui release archive has no x-ui.sh; try running the update again"
    fi
    mv -f "${xui_script_temp}" /usr/bin/still-ui
    if [[ $? -ne 0 ]]; then
        rm -f "${xui_script_temp}"
        _fail "ERROR: Failed to install x-ui.sh script"
    fi

    chmod +x ${xui_folder}/x-ui.sh > /dev/null 2>&1
    chmod +x /usr/bin/still-ui > /dev/null 2>&1
    mkdir -p /var/log/still-ui > /dev/null 2>&1

    echo -e "${green}Changing owner...${plain}"
    chown -R root:root ${xui_folder} > /dev/null 2>&1

    if [ -f "${xui_folder}/bin/config.json" ]; then
        echo -e "${green}Changing on config file permissions...${plain}"
        chmod 640 ${xui_folder}/bin/config.json > /dev/null 2>&1
    fi

    # Finish the schema/data migrations before the service starts, so the service and
    # config_after_update's CLI calls never run them on the same database at once (#6728).
    echo -e "${green}Migrating database...${plain}"
    # The old binary may not read a migrated database: from here on a failure
    # only restarts the panel (with the new files) instead of rolling back.
    update_rollback_files=0
    "${xui_folder}/x-ui" migrate

    if [[ $release == "alpine" ]]; then
        echo -e "${green}Installing startup unit x-ui.rc...${plain}"
        xui_rc_temp="/etc/init.d/still-ui.tmp.$$"
        rm -f "${xui_rc_temp}"
        if ! cp -f "${xui_folder}/x-ui.rc" "${xui_rc_temp}" > /dev/null 2>&1 || [[ ! -s "${xui_rc_temp}" ]]; then
            rm -f "${xui_rc_temp}"
            _fail "ERROR: The still-ui release archive has no x-ui.rc; try running the update again"
        fi
        mv -f "${xui_rc_temp}" /etc/init.d/still-ui
        if [[ $? -ne 0 ]]; then
            rm -f "${xui_rc_temp}"
            _fail "ERROR: Failed to install startup unit x-ui.rc"
        fi
        chmod +x /etc/init.d/still-ui > /dev/null 2>&1
        chown root:root /etc/init.d/still-ui > /dev/null 2>&1
        rc-update add still-ui > /dev/null 2>&1
        rc-service still-ui start > /dev/null 2>&1
    else
        if [ -f "still-ui.service" ]; then
            echo -e "${green}Installing systemd unit...${plain}"
            if ! _install_xui_service_unit "still-ui.service" "false"; then
                echo -e "${red}Failed to copy still-ui.service${plain}"
                exit 1
            fi
        else
            service_installed=false
            case "${release}" in
                ubuntu | debian | armbian)
                    if [ -f "x-ui.service.debian" ]; then
                        echo -e "${green}Installing debian-like systemd unit...${plain}"
                        if _install_xui_service_unit "x-ui.service.debian" "false"; then
                            service_installed=true
                        fi
                    fi
                    ;;
                arch | manjaro | parch)
                    if [ -f "x-ui.service.arch" ]; then
                        echo -e "${green}Installing arch-like systemd unit...${plain}"
                        if _install_xui_service_unit "x-ui.service.arch" "false"; then
                            service_installed=true
                        fi
                    fi
                    ;;
                *)
                    if [ -f "x-ui.service.rhel" ]; then
                        echo -e "${green}Installing rhel-like systemd unit...${plain}"
                        if _install_xui_service_unit "x-ui.service.rhel" "false"; then
                            service_installed=true
                        fi
                    fi
                    ;;
            esac

            # If service file not found in tar.gz, download from GitHub
            if [ "$service_installed" = false ]; then
                echo -e "${yellow}Service files not found in tar.gz, downloading from GitHub...${plain}"
                case "${release}" in
                    ubuntu | debian | armbian)
                        service_unit_url="https://raw.githubusercontent.com/Dv2Corn/still-ui/${script_ref}/x-ui.service.debian"
                        ;;
                    arch | manjaro | parch)
                        service_unit_url="https://raw.githubusercontent.com/Dv2Corn/still-ui/${script_ref}/x-ui.service.arch"
                        ;;
                    *)
                        service_unit_url="https://raw.githubusercontent.com/Dv2Corn/still-ui/${script_ref}/x-ui.service.rhel"
                        ;;
                esac

                if ! _install_xui_service_unit "$service_unit_url" "true"; then
                    echo -e "${red}Failed to install still-ui.service from GitHub (${script_ref}) -- the release tarball did not ship one either${plain}"
                    exit 1
                fi
            fi
        fi
        chown root:root ${xui_service}/still-ui.service > /dev/null 2>&1
        chmod 644 ${xui_service}/still-ui.service > /dev/null 2>&1
        _still_xray_unit || echo -e "${yellow}Xray stays inside the panel process.${plain}"
        systemctl daemon-reload > /dev/null 2>&1
        systemctl enable still-ui > /dev/null 2>&1
        systemctl start still-ui > /dev/null 2>&1
    fi

    config_after_update

    # IP Limit relies on fail2ban; install + configure it now so the feature
    # works out of the box on update too (no-op when XUI_ENABLE_FAIL2BAN=false).
    # Never fatal.
    setup_fail2ban

    echo -e "${green}still-ui ${tag_version}${plain} updating finished, it is running now..."
    echo -e ""
    echo -e "┌───────────────────────────────────────────────────────┐
│  ${blue}still-ui control menu usages (subcommands):${plain}              │
│                                                       │
│  ${blue}still-ui${plain}              - Admin Management Script          │
│  ${blue}still-ui start${plain}        - Start                            │
│  ${blue}still-ui stop${plain}         - Stop                             │
│  ${blue}still-ui restart${plain}      - Restart                          │
│  ${blue}still-ui status${plain}       - Current Status                   │
│  ${blue}still-ui settings${plain}     - Current Settings                 │
│  ${blue}still-ui enable${plain}       - Enable Autostart on OS Startup   │
│  ${blue}still-ui disable${plain}      - Disable Autostart on OS Startup  │
│  ${blue}still-ui log${plain}          - Check logs                       │
│  ${blue}still-ui banlog${plain}       - Check Fail2ban ban logs          │
│  ${blue}still-ui update${plain}       - Update                           │
│  ${blue}still-ui legacy${plain}       - Legacy version                   │
│  ${blue}still-ui install${plain}      - Install                          │
│  ${blue}still-ui uninstall${plain}    - Uninstall                        │
└───────────────────────────────────────────────────────┘"
}

echo -e "${green}Running...${plain}"
require_writable_update_paths
install_base
update_x-ui $1
