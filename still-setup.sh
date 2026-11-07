#!/bin/bash
# still-ui setup helpers shared by install.sh and update.sh. Not run on its
# own: both scripts source it from the unpacked release archive, so it always
# matches the binary it sets up. Uses their globals (release, xui_folder,
# xui_service and the colour codes).
# The globals come from the sourcing script; the code moved here verbatim
# from both scripts, which are only gated on errors (ls, A && B || C).
# shellcheck disable=SC2154,SC2012,SC2015

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

# still-xray: Xray runs as a systemd unit of its own, under an unprivileged user, so a restart,
# update or crash of the panel never drops a client. It is the default wherever systemd runs
# (STILL_XRAY_UNIT unset): an existing install moves over on its next update, unless its config
# needs what the unit's sandbox does not give (`x-ui xray-unit-check` lists it; Xray then stays
# inside the panel and the reasons are printed). STILL_XRAY_UNIT=off keeps Xray the panel's
# child process, as in 3x-ui; STILL_XRAY_UNIT=on forces the unit. Docker images have no
# systemd and always keep the child process.
# STILL_XRAY_MEMORY_MAX (e.g. 1G, default half the RAM within 256M..4G) caps the core.
_still_xray_unit() {
    local env_file setting template unit temp_file xray_bin bin_dir dropin_dir dropin
    local mem_kb max_mb gomem_mb check_out check_rc current check_flags
    [[ -n "${xui_service:-}" ]] || return 0
    command -v systemctl > /dev/null 2>&1 || return 0
    # systemctl without systemd as PID 1 (some containers) cannot run a unit.
    [[ -d /run/systemd/system ]] || return 0
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
    case "$setting" in
        on | off | auto) ;;
        "") setting=auto ;;
        *)
            echo -e "${yellow}Ignoring STILL_XRAY_UNIT=${setting}: use on, off or leave it unset.${plain}"
            setting=auto
            ;;
    esac
    if [[ "$setting" == "auto" && -s "$template" && -x "$xray_bin" ]]; then
        # What the unit about to be written and the local drop-ins give the core.
        check_flags=()
        grep -qs '^AmbientCapabilities=.*CAP_NET_ADMIN' "$template" "${xui_service}"/still-xray.service.d/*.conf && check_flags+=(-net-admin)
        if grep -qsE '^PrivateDevices=(no|false|0|off)$' "${xui_service}"/still-xray.service.d/*.conf &&
            "${xui_folder}/x-ui" xray-unit-check -h 2>&1 | grep -q -- '-devices'; then
            check_flags+=(-devices)
        fi
        if "${xui_folder}/x-ui" xray-unit-check -h 2>&1 | grep -q -- '-net-admin'; then
            check_out=$("${xui_folder}/x-ui" xray-unit-check "${check_flags[@]}" 2>&1)
            check_rc=$?
        else
            # An older release without the check: it cannot vouch for its config, so nothing moves.
            check_out="this release has no xray-unit-check"
            check_rc=2
        fi
        case "$check_rc" in
            0) setting=on ;;
            3)
                echo -e "${yellow}Xray stays inside the panel process: the still-xray service cannot run this config yet.${plain}"
                printf '%s\n' "$check_out" | sed 's/^/  - /'
                echo -e "${yellow}Fix that and run still-ui update again to move Xray to its own service, or set STILL_XRAY_UNIT=off in ${env_file} to keep it here.${plain}"
                setting=off
                ;;
            *)
                # The check itself failed: keep whatever mode this install already runs.
                current=$(sed -n 's/^[[:space:]]*\(export[[:space:]]\+\)\?XUI_XRAY_UNIT=["'"'"']\?\([^"'"'"' ]*\).*/\2/p' "$env_file" 2> /dev/null | tail -n 1)
                echo -e "${yellow}Could not check the Xray config for the still-xray service (${check_out:-exit ${check_rc}}); keeping the current mode.${plain}"
                if [[ -n "$current" ]]; then setting=on; else setting=off; fi
                ;;
        esac
    fi
    if [[ "$setting" != "on" || ! -s "$template" || ! -x "$xray_bin" ]]; then
        _still_env_set "$env_file" XUI_XRAY_UNIT ""
        if systemctl is-enabled still-xray > /dev/null 2>&1; then
            systemctl disable --now still-xray > /dev/null 2>&1
        fi
        echo -e "${yellow}Xray runs inside the panel process, as in 3x-ui: restarting or updating the panel drops clients.${plain}"
        return 0
    fi
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
    echo -e "${green}Xray runs as its own service still-xray${max_mb:+ (MemoryMax=${max_mb}M)}: restarting or updating the panel keeps the clients of Xray inbounds connected.${plain}"
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
