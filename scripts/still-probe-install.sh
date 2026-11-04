#!/usr/bin/env bash
# still-probe installer: puts the reachability probe agent on a Linux box inside
# the network you want to test from, and runs it from a systemd timer.
#
#   STILL_PROBE_TOKEN='TOKEN' bash <(curl -fsSL https://raw.githubusercontent.com/Dv2Corn/still-ui/main/scripts/still-probe-install.sh) \
#       --panel 'https://panel.example.com:2053/path/'
#
# The token is a credential: give it through STILL_PROBE_TOKEN, a file
# (--token-file FILE) or standard input (asked for when nothing else gives it),
# not on the command line, where every local user sees it in ps.
#
# Options:
#   --panel URL       the panel URL with its base path, as you open it in a browser
#   --token-file FILE read the probe token (shown once on the panel's Probes page)
#                     from FILE; "-" reads standard input
#   --token TOKEN     the token on the command line (visible in ps; deprecated)
#   --pin SHA256      pin the panel's TLS certificate (hex SHA-256), for a self-signed panel
#   --release TAG     still-ui release to install the agent from (default: latest)
#   --archive FILE    install from a local still-probe-linux-<arch>.tar.gz instead of downloading
#   --uninstall       remove the agent, its timer, user and state
set -euo pipefail

REPO="Dv2Corn/still-ui"
PREFIX=/usr/local/still-probe
ETC=/etc/still-probe
STATE=/var/lib/still-probe
UNIT_DIR=/etc/systemd/system
USER_NAME=still-probe

die() { echo "still-probe-install: $*" >&2; exit 1; }

panel="" token="" token_file="" pin="" release="latest" archive="" uninstall=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --panel) panel="${2:-}"; shift 2 ;;
        --token)
            token="${2:-}"; shift 2
            echo "still-probe-install: warning: --token puts the token on the command line, where every local user can read it (ps, /proc); use STILL_PROBE_TOKEN or --token-file instead" >&2 ;;
        --token-file) token_file="${2:-}"; shift 2 ;;
        --pin) pin="${2:-}"; shift 2 ;;
        --release) release="${2:-}"; shift 2 ;;
        --archive) archive="${2:-}"; shift 2 ;;
        --uninstall) uninstall=1; shift ;;
        -h|--help) sed -n '2,23p' "$0" 2>/dev/null || true; exit 0 ;;
        *) die "unknown option $1" ;;
    esac
done

# read_token: --token-file, then --token, then STILL_PROBE_TOKEN, then
# standard input (with a hidden prompt on a terminal).
read_token() {
    local line=""
    if [[ -n "$token_file" ]]; then
        if [[ "$token_file" == - ]]; then
            IFS= read -r line || [[ -n "$line" ]] || die "no token on standard input"
        else
            [[ -r "$token_file" ]] || die "cannot read --token-file $token_file"
            IFS= read -r line < "$token_file" || [[ -n "$line" ]] || die "--token-file $token_file is empty"
        fi
        token="$line"
    elif [[ -z "$token" && -n "${STILL_PROBE_TOKEN:-}" ]]; then
        token="$STILL_PROBE_TOKEN"
    elif [[ -z "$token" ]]; then
        if [[ -t 0 ]]; then
            IFS= read -rs -p "Probe token (from the panel's Probes page): " line || true
            echo >&2
        else
            IFS= read -r line || true
        fi
        token="$line"
    fi
    token="${token//[[:space:]]/}"
    unset STILL_PROBE_TOKEN
}

# deploy/test/probe-install-token.sh sources this file to test read_token;
# everything below installs.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
    return 0
fi

[[ $(id -u) -eq 0 ]] || die "run as root"
command -v systemctl >/dev/null || die "systemd is required"

if [[ $uninstall -eq 1 ]]; then
    systemctl disable --now still-probe.timer 2>/dev/null || true
    systemctl stop still-probe.service 2>/dev/null || true
    rm -f "$UNIT_DIR/still-probe.service" "$UNIT_DIR/still-probe.timer"
    systemctl daemon-reload
    rm -rf "$PREFIX" "$ETC" "$STATE"
    if id "$USER_NAME" >/dev/null 2>&1; then userdel "$USER_NAME" 2>/dev/null || true; fi
    echo "still-probe removed"
    exit 0
fi

read_token

# Values go into a JSON file written by printf, so they are checked strictly.
panel_re='^https?://[]A-Za-z0-9.:[-]+(/[A-Za-z0-9._~/%+-]*)?$'
[[ "$panel" =~ $panel_re ]] || die "--panel must be the panel URL, e.g. https://panel.example.com:2053/path/"
[[ "$token" =~ ^[A-Za-z0-9]{48}$ ]] || die "the token must be the 48-character token from the Probes page (STILL_PROBE_TOKEN, --token-file or standard input)"
[[ -z "$pin" || "$pin" =~ ^[A-Fa-f0-9:]{64,95}$ ]] || die "--pin must be a hex SHA-256"
[[ "$panel" == */ ]] || panel="$panel/"

case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "unsupported CPU $(uname -m) (amd64 and arm64 are built)" ;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
name="still-probe-linux-${arch}.tar.gz"
if [[ -n "$archive" ]]; then
    [[ -f "$archive" ]] || die "no such archive: $archive"
    cp "$archive" "$work/$name"
else
    command -v curl >/dev/null || die "curl is required"
    if [[ "$release" == latest ]]; then
        base="https://github.com/${REPO}/releases/latest/download"
    else
        base="https://github.com/${REPO}/releases/download/${release}"
    fi
    curl -fsSL --retry 3 -o "$work/$name" "$base/$name" || die "download of $base/$name failed"
    curl -fsSL --retry 3 -o "$work/$name.sha256" "$base/$name.sha256" || die "download of the checksum failed"
    (cd "$work" && sha256sum -c "$name.sha256" >/dev/null) || die "checksum mismatch for $name"
fi
tar -xzf "$work/$name" -C "$work"
[[ -x "$work/still-probe/still-probe" && -x "$work/still-probe/xray" ]] || die "$name does not hold still-probe/still-probe and still-probe/xray"

if ! id "$USER_NAME" >/dev/null 2>&1; then
    useradd --system --no-create-home --home-dir "$STATE" --shell /usr/sbin/nologin "$USER_NAME"
fi
install -d -m 0755 "$PREFIX"
install -m 0755 "$work/still-probe/still-probe" "$PREFIX/still-probe.new"
install -m 0755 "$work/still-probe/xray" "$PREFIX/xray.new"
mv -f "$PREFIX/still-probe.new" "$PREFIX/still-probe"
mv -f "$PREFIX/xray.new" "$PREFIX/xray"
install -d -m 0750 -o root -g "$USER_NAME" "$ETC"
install -d -m 0700 -o "$USER_NAME" -g "$USER_NAME" "$STATE"

umask 027
printf '{\n  "panel": "%s",\n  "token": "%s",\n  "panelCertSha256": "%s",\n  "xray": "%s/xray",\n  "stateDir": "%s"\n}\n' \
    "$panel" "$token" "$pin" "$PREFIX" "$STATE" > "$ETC/config.json.new"
chown root:"$USER_NAME" "$ETC/config.json.new"
chmod 0640 "$ETC/config.json.new"
mv -f "$ETC/config.json.new" "$ETC/config.json"
umask 022

cat > "$UNIT_DIR/still-probe.service" <<EOF
[Unit]
Description=still-ui reachability probe
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=$USER_NAME
Group=$USER_NAME
ExecStart=$PREFIX/still-probe run -config $ETC/config.json
TimeoutStartSec=300
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ReadWritePaths=$STATE
CapabilityBoundingSet=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
EOF
# The agent itself waits out the panel's interval (10 min by default), so the
# timer only has to wake it up every minute.
cat > "$UNIT_DIR/still-probe.timer" <<EOF
[Unit]
Description=Run the still-ui reachability probe

[Timer]
OnBootSec=1min
OnUnitActiveSec=1min
AccuracySec=10s

[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable still-probe.timer >/dev/null

echo "still-probe $("$PREFIX/still-probe" version) installed; running the first check..."
if systemctl start still-probe.service; then
    journalctl -u still-probe.service -n 20 --no-pager -o cat || true
else
    journalctl -u still-probe.service -n 20 --no-pager -o cat || true
    systemctl start still-probe.timer
    die "the first check failed; see the lines above (the timer keeps retrying every minute)"
fi
systemctl start still-probe.timer
echo "done: results appear on the panel's Probes page"
