#!/usr/bin/env bash
# End-to-end test of the installer and the lifecycle scripts on real systems:
# throwaway Incus system containers (real systemd) install still-ui from a
# local release archive that a fake github.com inside each container serves
# over HTTPS, with .sha256 sidecars and SHA256SUMS exactly as a release
# publishes them. install.sh, update.sh and x-ui.sh run unmodified: the
# container resolves github.com and api.github.com to 127.0.0.1 and trusts a
# throwaway CA, so every download, redirect and checksum is the real code path.
#
#   deploy/test/installer-e2e.sh --archive FILE [options] [IMAGE...]
#
#   --archive FILE   this build: a still-ui-linux-amd64.tar.gz from
#                    scripts/still-release.sh (or a release archive). Its
#                    install.sh, update.sh, x-ui.sh, migrate.sh and units are
#                    replaced by this checkout's, unless --as-is.
#   --as-is          test the archive's own scripts
#   --previous TAGS  releases to update from, comma-separated (default
#                    v1.5.1,v1.5.0; their assets are downloaded from GitHub and
#                    checked against their published sums). Releases before
#                    1.5.1 update with the update.sh of the main branch, which
#                    the fake raw.githubusercontent.com serves from this build.
#   --migrate        also run scripts/test-migrate.sh (3x-ui v3.11.0 ->
#                    this build) on the first image
#   --keep           keep the containers (KEEP=1 does the same)
#   E2E_CACHE=DIR    where the previous releases are kept (~/.cache/still-ui-e2e)
#   IMAGE            Incus images: names (default: debian/12 ubuntu/24.04)
#
# Per image the containers below run one after the other, in parallel with
# the other images':
#   su-inst-<image>-fresh    fresh install -> panel, still-xray, result file,
#                            memory ceilings, capabilities, pinned Xray; a second
#                            install changes nothing; a download through Xray
#                            survives `still-ui restart` and a killed panel;
#                            status/settings/restart-xray; a tampered archive,
#                            a wrong .sha256 and a tampered update.sh are refused
#                            with nothing changed; uninstall.
#   su-inst-<image>-from-<tag>  a previous release with inbounds and clients,
#                            `still-ui update` to this build while a download
#                            runs through Xray -> same data, login, settings,
#                            pinned Xray, download complete.
#
# Needs incus with the images: remote, openssl, curl, tar and sha256sum on the
# host, and internet in the containers (apt). Containers get 2 CPUs and 2 GiB.
# Exit status 0 when every check passed; logs in $LOG_DIR (default a temp dir).
# The snippets in single quotes run inside the containers.
# shellcheck disable=SC2016
set -uo pipefail

SRC=$(cd "$(dirname "$0")/../.." && pwd)
ARCHIVE="" AS_IS=0 PREVS=v1.5.1,v1.5.0 MIGRATE=0 KEEP=${KEEP:-0}
IMAGES=()
while (($#)); do
    case "$1" in
        --archive) ARCHIVE="$2"; shift 2 ;;
        --as-is) AS_IS=1; shift ;;
        --previous) PREVS="$2"; shift 2 ;;
        --migrate) MIGRATE=1; shift ;;
        --keep) KEEP=1; shift ;;
        -h | --help) sed -n '2,/^set -uo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
        -*) echo "unknown option $1" >&2; exit 2 ;;
        *) IMAGES+=("$1"); shift ;;
    esac
done
[[ -s "$ARCHIVE" ]] || { echo "usage: $0 --archive still-ui-linux-amd64.tar.gz [IMAGE...]" >&2; exit 2; }
((${#IMAGES[@]})) || IMAGES=(debian/12 ubuntu/24.04)
for tool in incus openssl curl tar sha256sum; do
    command -v "$tool" > /dev/null || { echo "$tool is required" >&2; exit 2; }
done
ARCHIVE=$(realpath "$ARCHIVE")

NEW=v1.99.0-e2e          # tag this build is published under on the fake GitHub
TAMPERED_ARCHIVE=v1.99.1 # archive changed after its .sha256 was published
TAMPERED_SCRIPT=v1.99.2  # update.sh changed after SHA256SUMS was published
WRONG_SUM=v1.99.3        # .sha256 that does not match the archive
REPO=Dv2Corn/still-ui
XRAY_SHA=$(sed -n 's/^SHA256_amd64=//p' "$SRC/scripts/xray-build/build.sh")
W=$(mktemp -d)
LOG_DIR=${LOG_DIR:-$W/logs}
mkdir -p "$LOG_DIR"
trap 'rm -rf -- "$W/gh" "$W/pkg" "$W/tree.tar"' EXIT

log() { echo "[$(date +%H:%M:%S)] $*"; }
die() { log "ERROR $*"; exit 2; }

# --- the fake GitHub release tree -----------------------------------------------
dl="$W/gh/download"
IFS=, read -ra PREV_TAGS <<< "$PREVS"
mkdir -p "$dl/$NEW" "$W/gh/main" "$W/pkg"
log "release tree: $NEW from $ARCHIVE$([[ $AS_IS == 1 ]] || echo ' with this checkout'"'"'s scripts'), previous ${PREV_TAGS[*]}"
if [[ $AS_IS == 1 ]]; then
    cp "$ARCHIVE" "$dl/$NEW/still-ui-linux-amd64.tar.gz"
else
    tar -xzf "$ARCHIVE" -C "$W/pkg" || die "cannot extract $ARCHIVE"
    for f in install.sh update.sh x-ui.sh migrate.sh still-xray.service x-ui.service.debian x-ui.service.rhel x-ui.service.arch x-ui.rc; do
        [[ -f "$SRC/$f" ]] && cp "$SRC/$f" "$W/pkg/still-ui/$f"
    done
    chmod 755 "$W/pkg/still-ui/"*.sh
    tar -C "$W/pkg" --owner=0 --group=0 -czf "$dl/$NEW/still-ui-linux-amd64.tar.gz" still-ui
fi
for f in install.sh update.sh x-ui.sh migrate.sh; do
    tar -xzOf "$dl/$NEW/still-ui-linux-amd64.tar.gz" "still-ui/$f" > "$dl/$NEW/$f" || die "the archive has no $f"
done
publish() { # TAG: .sha256 next to the archive and SHA256SUMS over everything
    (cd "$dl/$1" && sha256sum still-ui-linux-amd64.tar.gz > still-ui-linux-amd64.tar.gz.sha256 &&
        sha256sum still-ui-linux-amd64.tar.gz install.sh update.sh x-ui.sh migrate.sh > SHA256SUMS)
}
publish "$NEW"
ln "$dl/$NEW/"*.sh "$W/gh/main/" # what raw.githubusercontent.com/.../main/ serves
for t in "$TAMPERED_ARCHIVE" "$TAMPERED_SCRIPT" "$WRONG_SUM"; do
    mkdir -p "$dl/$t"
    ln "$dl/$NEW/"* "$dl/$t/"
done
# Changed after publishing: the sums stay those of the real files.
rm "$dl/$TAMPERED_ARCHIVE/still-ui-linux-amd64.tar.gz"
{ cat "$dl/$NEW/still-ui-linux-amd64.tar.gz"; printf 'x'; } > "$dl/$TAMPERED_ARCHIVE/still-ui-linux-amd64.tar.gz"
rm "$dl/$TAMPERED_SCRIPT/update.sh"
{ cat "$dl/$NEW/update.sh"; echo 'touch /root/PWNED'; } > "$dl/$TAMPERED_SCRIPT/update.sh"
rm "$dl/$WRONG_SUM/still-ui-linux-amd64.tar.gz.sha256" "$dl/$WRONG_SUM/SHA256SUMS"
printf '%064d  still-ui-linux-amd64.tar.gz\n' 0 > "$dl/$WRONG_SUM/still-ui-linux-amd64.tar.gz.sha256"
(cd "$dl/$WRONG_SUM" && { cat still-ui-linux-amd64.tar.gz.sha256; sha256sum install.sh update.sh x-ui.sh migrate.sh; } > SHA256SUMS)

# The previous releases, as GitHub publishes them (kept in $E2E_CACHE, checked
# against their .sha256 on every run).
cache=${E2E_CACHE:-$HOME/.cache/still-ui-e2e}
for prev in "${PREV_TAGS[@]}"; do
    mkdir -p "$dl/$prev" "$cache/$prev"
    if ! (cd "$cache/$prev" && sha256sum -c --quiet still-ui-linux-amd64.tar.gz.sha256 > /dev/null 2>&1); then
        for f in still-ui-linux-amd64.tar.gz still-ui-linux-amd64.tar.gz.sha256; do
            curl -fsSL --retry 3 -o "$cache/$prev/$f" "https://github.com/$REPO/releases/download/$prev/$f" || die "cannot download $f of $prev"
        done
        (cd "$cache/$prev" && sha256sum -c --quiet still-ui-linux-amd64.tar.gz.sha256) || die "$prev does not match its .sha256"
    fi
    cp "$cache/$prev/still-ui-linux-amd64.tar.gz" "$cache/$prev/still-ui-linux-amd64.tar.gz.sha256" "$dl/$prev/"
    # Releases before 1.5.1 publish no scripts: they were installed with
    # `install.sh <tag>` of the main branch, and their menu is in the archive.
    for f in SHA256SUMS install.sh update.sh x-ui.sh; do
        curl -fsSL --retry 3 -o "$dl/$prev/$f" "https://github.com/$REPO/releases/download/$prev/$f" 2> /dev/null || rm -f "$dl/$prev/$f"
    done
    [[ -s "$dl/$prev/x-ui.sh" ]] || tar -xzOf "$dl/$prev/still-ui-linux-amd64.tar.gz" still-ui/x-ui.sh > "$dl/$prev/x-ui.sh" || die "$prev has no x-ui.sh"
    [[ ! -s "$dl/$prev/SHA256SUMS" ]] || (cd "$dl/$prev" && sha256sum -c --quiet --ignore-missing SHA256SUMS) || die "$prev does not match its SHA256SUMS"
done
new_version=$(tr -d '[:space:]' < "$SRC/internal/config/version")
[[ $AS_IS == 0 ]] || new_version=""
tar -C "$W/gh" -cf "$W/tree.tar" download main # hard links stay links

# --- a throwaway CA and a certificate for github.com ------------------------------
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=still-ui e2e test CA" \
    -keyout "$W/ca.key" -out "$W/ca.crt" > /dev/null 2>&1 || die "openssl CA"
openssl req -newkey rsa:2048 -nodes -subj "/CN=github.com" -keyout "$W/gh.key" -out "$W/gh.csr" > /dev/null 2>&1
printf 'subjectAltName=DNS:github.com,DNS:api.github.com,DNS:raw.githubusercontent.com\nextendedKeyUsage=serverAuth\n' > "$W/gh.ext"
openssl x509 -req -in "$W/gh.csr" -CA "$W/ca.crt" -CAkey "$W/ca.key" -CAcreateserial -days 2 \
    -extfile "$W/gh.ext" -out "$W/gh.crt" > /dev/null 2>&1 || die "openssl server certificate"

# The fake GitHub (HTTPS on 127.0.0.1:443) and a slow HTTP source for the
# downloads through Xray (:8099/slow?seconds=N, 64 KiB per second). Xray's
# default routing blocks private addresses, so the source is reached at a
# public address put on the container's loopback (SLOW_IP below).
cat > "$W/fakegh.py" << 'PY'
import http.server, json, os, ssl, sys, threading, time, urllib.parse
ROOT = "/srv/gh"
REPO = "/Dv2Corn/still-ui"

class GH(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, fmt, *args):
        with open(ROOT + "/access.log", "a") as f:
            f.write("%s %s %s\n" % (self.headers.get("Host", "?"), self.command, self.path))
    def send(self, code, body=b"", headers=()):
        self.send_response(code)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)
    def do_HEAD(self):
        self.do_GET()
    def do_GET(self):
        latest = open(ROOT + "/latest").read().strip()
        host = self.headers.get("Host", "").split(":")[0]
        path = urllib.parse.urlparse(self.path).path
        if host == "api.github.com" and path == "/repos" + REPO + "/releases/latest":
            return self.send(200, json.dumps({"tag_name": latest}).encode(), [("Content-Type", "application/json")])
        if path == REPO + "/releases/latest":
            return self.send(302, b"", [("Location", "https://github.com" + REPO + "/releases/tag/" + latest)])
        if path.startswith(REPO + "/releases/tag/"):
            return self.send(200, b"release page")
        f = None
        prefix = REPO + "/releases/download/"
        if host == "raw.githubusercontent.com" and path.startswith(REPO + "/main/"):
            rel = os.path.normpath(path[len(REPO + "/main/"):])
            f = os.path.join(ROOT, "main", rel)
        elif path.startswith(prefix):
            rel = os.path.normpath(path[len(prefix):])
            f = os.path.join(ROOT, "download", rel)
        if f:
            if not rel.startswith("..") and os.path.isfile(f):
                size = os.path.getsize(f)
                self.send_response(200)
                self.send_header("Content-Length", str(size))
                self.send_header("Content-Type", "application/octet-stream")
                self.end_headers()
                if self.command != "HEAD":
                    with open(f, "rb") as fh:
                        while True:
                            chunk = fh.read(1 << 20)
                            if not chunk:
                                break
                            self.wfile.write(chunk)
                return
        return self.send(404, b"Not Found")

class Slow(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def do_GET(self):
        q = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
        seconds = int(q.get("seconds", ["10"])[0])
        self.send_response(200)
        self.send_header("Content-Length", str(seconds * 4 * 16384))
        self.end_headers()
        for _ in range(seconds * 4):
            self.wfile.write(b"s" * 16384)
            self.wfile.flush()
            time.sleep(0.25)

ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain("/srv/gh.crt", "/srv/gh.key")
gh = http.server.ThreadingHTTPServer(("127.0.0.1", 443), GH)
gh.socket = ctx.wrap_socket(gh.socket, server_side=True)
threading.Thread(target=gh.serve_forever, daemon=True).start()
http.server.ThreadingHTTPServer(("0.0.0.0", 8099), Slow).serve_forever()
PY

# Inbounds before and after (sqlite3 -json rows): the same rows, and every
# field a setting had before has the same value after. The panel re-serialises
# the JSON and adds new defaults (created_at, comment, ...) on migration, and
# stamps every client's updated_at when it starts on a reinstall or an update.
cat > "$W/same-inbounds.py" << 'PY'
import json, sys
def sub(a, b, path):
    if isinstance(a, str) and a[:1] in "{[":
        try:
            a, b = json.loads(a), json.loads(b)
        except (ValueError, TypeError):
            pass
    if isinstance(a, dict) and isinstance(b, dict):
        return [d for k in a if k != "updated_at" for d in (sub(a[k], b[k], path + "." + k) if k in b else [path + "." + k + " missing"])]
    if isinstance(a, list) and isinstance(b, list):
        if len(a) != len(b):
            return ["%s: %d entries, now %d" % (path, len(a), len(b))]
        return [d for i, (x, y) in enumerate(zip(a, b)) for d in sub(x, y, "%s[%d]" % (path, i))]
    return [] if a == b else ["%s: %r -> %r" % (path, a, b)]
before, after = (json.load(open(f)) for f in sys.argv[1:3])
diffs = sub(before, after, "inbounds")
print("\n".join(diffs[:40]) or "same %d inbounds" % len(before))
sys.exit(1 if diffs or not before else 0)
PY

# --- container helpers -------------------------------------------------------------
# in CT SNIPPET: run a bash snippet as root in the container.
cx() { incus exec "$1" -- bash -c "$2"; }
latest() { cx "$1" "echo $2 > /srv/gh/latest"; }

prepare() { # CT IMAGE
    local ct="$1" image="$2" i
    incus delete -f "$ct" > /dev/null 2>&1
    incus launch "images:$image" "$ct" -c limits.cpu=2 -c limits.memory=2GiB -c security.nesting=false > /dev/null || return 1
    for i in $(seq 1 60); do
        cx "$ct" 'systemctl is-system-running 2> /dev/null | grep -qE "running|degraded" && getent hosts deb.debian.org archive.ubuntu.com > /dev/null' && break
        sleep 2
    done
    cx "$ct" 'export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq && apt-get install -y -qq python3 curl ca-certificates openssl sqlite3 tar procps iproute2 > /dev/null' || return 1
    incus file push "$W/ca.crt" "$ct/usr/local/share/ca-certificates/still-e2e.crt" > /dev/null
    incus file push "$W/gh.crt" "$W/gh.key" "$W/fakegh.py" "$W/same-inbounds.py" "$ct/srv/" > /dev/null
    incus file push "$SRC/scripts/test-migrate-seed.py" "$ct/srv/seed.py" > /dev/null
    incus exec "$ct" -- mkdir -p /srv/gh
    incus exec "$ct" -- tar -xf - -C /srv/gh < "$W/tree.tar"
    cx "$ct" 'update-ca-certificates > /dev/null 2>&1
        echo "127.0.0.1 github.com api.github.com raw.githubusercontent.com" >> /etc/hosts
        echo none > /srv/gh/latest
        ip addr add 45.45.45.45/32 dev lo
        systemd-run --unit=fakegh --quiet python3 /srv/fakegh.py
        for i in $(seq 1 30); do curl -sSI https://github.com/Dv2Corn/still-ui/releases/latest 2> /dev/null | head -n 1 | grep -q 302 && exit 0; sleep 1; done
        exit 1'
}

# Helpers for the snippets that run in the container.
LIB='
SLOW_IP=45.45.45.45
res() { . /etc/still-ui/install-result.env; }
url() { res; echo "http://127.0.0.1:${XUI_PANEL_PORT}/${XUI_WEB_BASE_PATH}"; }
mainpid() { systemctl show -p MainPID --value "$1"; }
wait_active() { local i; for i in $(seq 1 60); do systemctl is-active --quiet "$1" && return 0; sleep 1; done; return 1; }
wait_panel() { local i c; for i in $(seq 1 60); do c=$(curl -s -o /dev/null -w "%{http_code}" "$(url)/"); [[ $c =~ ^(200|30[12378])$ ]] && return 0; sleep 1; done; return 1; }
mib() { echo $(($1 * 1048576)); }
want_panel_max() { local kb m; kb=$(awk "/^MemTotal:/ {print \$2}" /proc/meminfo); m=$((kb * 35 / 100 / 1024)); ((m < 256)) && m=256; ((m > 1024)) && m=1024; echo $m; }
want_xray_max() { local kb m; kb=$(awk "/^MemTotal:/ {print \$2}" /proc/meminfo); m=$((kb / 2 / 1024)); ((m < 256)) && m=256; ((m > 4096)) && m=4096; echo $m; }
inbounds_dump() { sqlite3 -json /etc/still-ui/x-ui.db "select id, remark, port, protocol, settings, stream_settings, enable from inbounds order by id"; }
same_inbounds() { inbounds_dump > /root/t/inb.now; python3 /srv/same-inbounds.py /root/t/inb /root/t/inb.now; }
xray_pid() { local i p; for i in $(seq 1 30); do p=$(mainpid still-xray); [[ $p != 0 ]] && systemctl is-active --quiet still-xray && { echo "$p"; return 0; }; sleep 1; done; return 1; }
clients() { sqlite3 /etc/still-ui/x-ui.db "select email from client_traffics order by email" | tr "\n" " "; }
tree_digest() { (find /usr/local/still-ui -type f ! -path "*/bin/config.json" ! -name "*.log" -print0 | sort -z | xargs -0 sha256sum; sha256sum /usr/bin/still-ui; find /etc/systemd/system -path "*still*" -type f -print0 | sort -z | xargs -0 sha256sum) | sha256sum | cut -c1-16; }
slow_start() { rm -f /tmp/slow.res; systemd-run --quiet --collect bash -c "curl -s --max-time 120 --socks5 127.0.0.1:24441 \"http://$SLOW_IP:8099/slow?seconds=$1\" -o /tmp/slow.out -w \"%{http_code} %{size_download}\n\" > /tmp/slow.res; echo rc=\$? >> /tmp/slow.res"; sleep 2; }
slow_check() { local i; for i in $(seq 1 130); do grep -q "^rc=" /tmp/slow.res 2> /dev/null && break; sleep 1; done; cat /tmp/slow.res; grep -qx "200 $(($1 * 65536))" /tmp/slow.res && grep -qx rc=0 /tmp/slow.res; }
'

# ck CT DESCRIPTION SNIPPET: one check; the snippet's output goes to the log.
ck() {
    local ct="$1" what="$2" out
    if out=$(cx "$ct" "$LIB
$3" 2>&1); then
        log "  ok   $what"
        printf '== ok %s\n%s\n' "$what" "$out" >> "$LOG_DIR/$ct.log"
    else
        log "  FAIL $what"
        printf '== FAIL %s\n%s\n' "$what" "$out" >> "$LOG_DIR/$ct.log"
        [[ -z "$out" ]] || printf '%s\n' "$out" | tail -n 8 | sed 's/^/         /'
        CASE_FAILED=1
    fi
}

# Checks that hold after every successful install or update of this build.
installed_checks() { # CT LABEL
    local ct="$1" l="$2"
    ck "$ct" "$l: still-ui and still-xray are active" 'wait_active still-ui && wait_active still-xray'
    ck "$ct" "$l: the panel answers on its port and base path" 'wait_panel'
    ck "$ct" "$l: the login in install-result.env works" 'res; python3 /srv/seed.py "$(url)" "$XUI_USERNAME" "$XUI_PASSWORD" --check-login'
    ck "$ct" "$l: install-result.env is 0600 root, no default login" '
        [[ $(stat -c "%a %U" /etc/still-ui/install-result.env) == "600 root" ]] && res && [[ $XUI_USERNAME != admin && $XUI_PASSWORD != admin ]] &&
        /usr/local/still-ui/x-ui setting -show true | grep -qx "hasDefaultCredential: false"'
    ck "$ct" "$l: panel MemoryMax 35% of RAM (256M..1G), MemoryHigh 85%, no swap" '
        m=$(want_panel_max); echo "MemTotal $(awk "/^MemTotal:/ {print \$2}" /proc/meminfo) kB, want ${m}M"
        systemctl show still-ui -p MemoryMax -p MemoryHigh -p MemorySwapMax
        [[ $(systemctl show -p MemoryMax --value still-ui) == $(mib $m) && $(systemctl show -p MemoryHigh --value still-ui) == $(mib $((m * 85 / 100))) &&
            $(systemctl show -p MemorySwapMax --value still-ui) == 0 ]]'
    ck "$ct" "$l: still-xray MemoryMax half the RAM (256M..4G), GOMEMLIMIT 85%" '
        m=$(want_xray_max); systemctl show still-xray -p MemoryMax -p Environment
        [[ $(systemctl show -p MemoryMax --value still-xray) == $(mib $m) ]] && systemctl show -p Environment --value still-xray | grep -q "GOMEMLIMIT=$((m * 85 / 100))MiB"'
    # Incus/LXC adds /run/systemd/system/service.d/zzz-lxc-service.conf with
    # NoNewPrivileges=no to every service, so in a container only the unit's own
    # setting can be checked; the capability sets are the kernel's.
    ck "$ct" "$l: still-xray runs as still-xray with CAP_NET_BIND_SERVICE only" '
        p=$(xray_pid) || exit 1; grep -E "^(Uid|Cap|NoNewPrivs)" /proc/$p/status
        [[ $(stat -c %U /proc/$p) == still-xray ]] && grep -qx "CapEff:	0000000000000400" /proc/$p/status &&
            grep -qx "CapBnd:	0000000000000400" /proc/$p/status && grep -qx "CapPrm:	0000000000000400" /proc/$p/status &&
            { grep -qx "NoNewPrivs:	1" /proc/$p/status || { grep -qx "NoNewPrivileges=no" /run/systemd/system/service.d/zzz-lxc-service.conf &&
                grep -qx "NoNewPrivileges=yes" /etc/systemd/system/still-xray.service; }; }'
    # An update leaves the running core alone (clients stay connected), so after
    # one it may still run the previous, now deleted, file: hash what runs.
    ck "$ct" "$l: the only Xray is still-xray's; it and the installed core are the pinned still-ui build" '
        p=$(xray_pid) || exit 1; readlink /proc/$p/exe; sha256sum /proc/$p/exe /usr/local/still-ui/bin/xray-linux-amd64
        [[ $(pgrep -fc "^/usr/local/still-ui/bin/xray-linux") == 1 ]] && [[ $(sha256sum < /proc/$p/exe | cut -d" " -f1) == '"$XRAY_SHA"' ]] &&
            [[ $(sha256sum < /usr/local/still-ui/bin/xray-linux-amd64 | cut -d" " -f1) == '"$XRAY_SHA"' ]]'
    ck "$ct" "$l: unit, menu and env file come from the archive, each once" '
        tar -xzOf /srv/gh/download/'"$NEW"'/still-ui-linux-amd64.tar.gz still-ui/x-ui.service.debian | cmp - /etc/systemd/system/still-ui.service &&
        tar -xzOf /srv/gh/download/'"$NEW"'/still-ui-linux-amd64.tar.gz still-ui/x-ui.sh | cmp - /usr/bin/still-ui &&
        [[ -s /etc/systemd/system/still-ui.service.d/10-xui-sandbox.conf && -s /etc/systemd/system/still-ui.service.d/15-still-memory.conf ]] &&
        [[ $(grep -c "^XUI_XRAY_UNIT=still-xray$" /etc/default/still-ui) == 1 ]] &&
        [[ -z $(find /etc/systemd/system /usr/local /usr/bin -maxdepth 2 \( -name "*.tmp.*" -o -name "x-ui-bin-backup.*" -o -name ".still-ui-*" \)) ]]'
    [[ -z "$new_version" ]] || ck "$ct" "$l: the panel reports version $new_version" "[[ \$(/usr/local/still-ui/x-ui -v) == $new_version ]]"
}

# Nothing of the install changed and nothing was stopped (after a refusal).
unchanged() { # CT LABEL
    ck "$1" "$2: files, units and running processes unchanged" '
        [[ $(tree_digest) == $(cat /root/t/tree) && $(mainpid still-ui) == $(cat /root/t/ui-pid) && $(mainpid still-xray) == $(cat /root/t/xray-pid) && ! -e /root/PWNED ]]'
}
snap() { cx "$1" "$LIB"'
    mkdir -p /root/t; tree_digest > /root/t/tree; mainpid still-ui > /root/t/ui-pid; mainpid still-xray > /root/t/xray-pid'; }

INSTALL_ENV='XUI_NONINTERACTIVE=1 XUI_SSL_MODE=none XUI_ENABLE_FAIL2BAN=false'

fresh_case() { # CT IMAGE
    local ct="$1" image="$2"
    CASE_FAILED=0
    log "$ct: preparing $image"
    prepare "$ct" "$image" > "$LOG_DIR/$ct.prepare.log" 2>&1 || { log "$ct: FAIL preparing the container (see $LOG_DIR/$ct.prepare.log)"; return 1; }
    latest "$ct" "$NEW"
    ck "$ct" "fresh install of this build (latest release, no TTY)" "$INSTALL_ENV bash /srv/gh/download/$NEW/install.sh < /dev/null > /root/install1.log 2>&1 || { tail -n 20 /root/install1.log; exit 1; }
        grep -q 'Checksum verified' /root/install1.log"
    installed_checks "$ct" "fresh"
    ck "$ct" "seed inbounds and clients through the API" 'res; python3 /srv/seed.py "$(url)" "$XUI_USERNAME" "$XUI_PASSWORD" --only vless-reality,vmess-ws,trojan-tls,mixed | tee /root/seed.json
        grep -qF "\"created\": [\"vless-reality\", \"vmess-ws\", \"trojan-tls\", \"mixed\"]" /root/seed.json'
    ck "$ct" "Xray serves the new inbounds" 'for i in $(seq 1 90); do ss -lnt | grep -q ":24441 " && ss -lnt | grep -q ":24431 " && exit 0; sleep 1; done; ss -lnt; exit 1'

    # A second install of the same release: same files, same data, no duplicates.
    cx "$ct" "$LIB"'
        mkdir -p /root/t; inbounds_dump > /root/t/inb; clients > /root/t/cl
        (find /etc/systemd/system -path "*still*" -type f -print0 | sort -z | xargs -0 sha256sum; sha256sum /etc/default/still-ui /etc/still-ui/install-result.env /usr/bin/still-ui) > /root/t/files
        systemctl list-unit-files "still*" --no-legend | sort > /root/t/units; res; echo "$XUI_PANEL_PORT" > /root/t/port'
    ck "$ct" "second install of the same release" "x=\$(mainpid still-xray); $INSTALL_ENV bash /srv/gh/download/$NEW/install.sh < /dev/null > /root/install2.log 2>&1 || { tail -n 20 /root/install2.log; exit 1; }
        sleep 5; echo \"still-xray PID \$x -> \$(mainpid still-xray) (information: a reinstall replaces the binary)\""
    ck "$ct" "second install: units, drop-ins, env file, result file and menu byte-identical" 'sha256sum -c --quiet /root/t/files && diff /root/t/units <(systemctl list-unit-files "still*" --no-legend | sort)'
    ck "$ct" "second install: inbounds and clients kept" 'same_inbounds && [[ $(clients) == $(cat /root/t/cl) && -n $(cat /root/t/cl) ]]'
    installed_checks "$ct" "second install"

    # Restarting or killing the panel leaves Xray and its clients alone.
    ck "$ct" "a download through Xray survives 'still-ui restart'" '
        x=$(mainpid still-xray); n=$(systemctl show -p NRestarts --value still-xray); u=$(mainpid still-ui)
        slow_start 12; still-ui restart > /root/restart.log 2>&1; grep -q "Restarted successfully" /root/restart.log || { cat /root/restart.log; exit 1; }
        [[ $(mainpid still-ui) != "$u" ]] || { echo "panel not restarted"; exit 1; }
        slow_check 12 && [[ $(mainpid still-xray) == "$x" && $(systemctl show -p NRestarts --value still-xray) == "$n" ]]'
    ck "$ct" "a download through Xray survives a killed panel (Restart=on-failure)" '
        x=$(mainpid still-xray); u=$(mainpid still-ui)
        slow_start 12; kill -9 "$u"; sleep 8; wait_active still-ui && [[ $(mainpid still-ui) != "$u" ]] || { echo "panel not back"; exit 1; }
        slow_check 12 && [[ $(mainpid still-xray) == "$x" ]]'
    ck "$ct" "'still-ui status' shows both services running" 'out=$(still-ui status 2>&1); rc=$?; echo "$out" | grep -E "still-(ui|xray).service|Active:"
        [[ $rc == 0 ]] && echo "$out" | grep -q "still-ui.service" && echo "$out" | grep -q "still-xray.service" && [[ $(echo "$out" | grep -c "active (running)") == 2 ]]'
    ck "$ct" "'still-ui settings' shows port, base path and database" 'res; out=$(timeout 90 still-ui settings < /dev/null 2>&1); rc=$?; echo "$out" | tail -n 12
        [[ $rc == 0 ]] && echo "$out" | grep -q "port: $XUI_PANEL_PORT" && echo "$out" | grep -q "webBasePath: /$XUI_WEB_BASE_PATH/" && echo "$out" | grep -q "Database: SQLite"'
    ck "$ct" "'still-ui restart-xray' restarts the core and keeps the panel" 'x=$(mainpid still-xray); u=$(mainpid still-ui)
        still-ui restart-xray > /root/rx.log 2>&1; for i in $(seq 1 20); do [[ $(mainpid still-xray) != "$x" ]] && break; sleep 1; done
        wait_active still-xray && [[ $(mainpid still-xray) != "$x" && $(mainpid still-ui) == "$u" ]] && grep -q "xray state: .*Running" /root/rx.log'

    # Refused before anything changes.
    snap "$ct"
    latest "$ct" "$TAMPERED_ARCHIVE"
    ck "$ct" "install.sh refuses an archive that does not match its .sha256" "! $INSTALL_ENV bash /srv/gh/download/$NEW/install.sh < /dev/null > /root/bad1.log 2>&1 && grep -q 'Checksum mismatch' /root/bad1.log"
    unchanged "$ct" "tampered archive"
    ck "$ct" "install.sh refuses a wrong .sha256" "! $INSTALL_ENV bash /srv/gh/download/$NEW/install.sh $WRONG_SUM < /dev/null > /root/bad2.log 2>&1 && grep -q 'Checksum mismatch' /root/bad2.log"
    unchanged "$ct" "wrong .sha256"
    ck "$ct" "'still-ui update' refuses the tampered archive of a listed update.sh" "printf 'y\n\n' | timeout 300 still-ui update > /root/bad3.log 2>&1; grep -q 'update.sh of still-ui $TAMPERED_ARCHIVE matches' /root/bad3.log && grep -q 'Checksum mismatch' /root/bad3.log"
    unchanged "$ct" "update with a tampered archive"
    latest "$ct" "$TAMPERED_SCRIPT"
    ck "$ct" "'still-ui update' refuses an update.sh that SHA256SUMS does not match" "printf 'y\n\n' | timeout 300 still-ui update > /root/bad4.log 2>&1; grep -q 'Checksum mismatch for update.sh' /root/bad4.log"
    unchanged "$ct" "tampered update.sh"
    ck "$ct" "nothing was downloaded after a refused checksum" "! grep -q 'download/$TAMPERED_SCRIPT/still-ui-linux' /srv/gh/access.log"

    # Uninstall (documented: like 3x-ui it also deletes the database).
    ck "$ct" "'still-ui uninstall' removes panel, units, the still-xray user and the data" 'res; port=$XUI_PANEL_PORT
        printf "y\n" | still-ui uninstall > /root/uninstall.log 2>&1; echo "exit status $?"; grep -q "Uninstalled Successfully" /root/uninstall.log || exit 1
        sleep 2; bad=0
        for p in /usr/local/still-ui /usr/bin/still-ui /etc/still-ui /etc/still-xray /etc/default/still-ui \
            /etc/systemd/system/still-ui.service /etc/systemd/system/still-ui.service.d /etc/systemd/system/still-xray.service /etc/systemd/system/still-xray.service.d; do
            [[ ! -e $p ]] || { echo "left behind: $p"; bad=1; }
        done
        systemctl cat still-ui > /dev/null 2>&1 && { echo "still-ui.service still known"; bad=1; }
        systemctl cat still-xray > /dev/null 2>&1 && { echo "still-xray.service still known"; bad=1; }
        id still-xray > /dev/null 2>&1 && { echo "user still-xray still exists"; bad=1; }
        pgrep -fa "^/usr/local/still-ui/bin/xray-linux" && { echo "Xray still runs"; bad=1; }
        ss -lnt | grep ":$port " && { echo "panel port still open"; bad=1; }
        exit $bad'
    latest "$ct" "$NEW"
    ck "$ct" "a new install after the uninstall works" "$INSTALL_ENV bash /srv/gh/download/$NEW/install.sh < /dev/null > /root/install3.log 2>&1 || { tail -n 20 /root/install3.log; exit 1; }"
    installed_checks "$ct" "reinstall"
    return "$CASE_FAILED"
}

update_case() { # CT IMAGE PREV
    local ct="$1" image="$2" PREV="$3" prev_version="${3#v}"
    CASE_FAILED=0
    log "$ct: preparing $image"
    prepare "$ct" "$image" > "$LOG_DIR/$ct.prepare.log" 2>&1 || { log "$ct: FAIL preparing the container (see $LOG_DIR/$ct.prepare.log)"; return 1; }
    latest "$ct" "$PREV"
    local installer="/srv/gh/download/$PREV/install.sh" how="its own install.sh"
    [[ -s "$dl/$PREV/install.sh" ]] || installer="/srv/gh/main/install.sh $PREV" how="install.sh of main with the tag"
    ck "$ct" "install $PREV ($how)" "$INSTALL_ENV bash $installer < /dev/null > /root/install-prev.log 2>&1 || { tail -n 20 /root/install-prev.log; exit 1; }
        [[ \$(/usr/local/still-ui/x-ui -v) == ${prev_version} ]]"
    ck "$ct" "$PREV: panel and Xray up" 'wait_active still-ui && wait_panel'
    ck "$ct" "$PREV: seed inbounds and clients" 'res; python3 /srv/seed.py "$(url)" "$XUI_USERNAME" "$XUI_PASSWORD" --only vless-reality,vmess-ws,trojan-tls,mixed | tee /root/seed.json
        grep -qF "\"created\": [\"vless-reality\", \"vmess-ws\", \"trojan-tls\", \"mixed\"]" /root/seed.json || exit 1
        for i in $(seq 1 90); do ss -lnt | grep -q ":24441 " && exit 0; sleep 1; done; ss -lnt; journalctl -u still-xray -u still-ui -n 20 --no-pager; exit 1'
    cx "$ct" "$LIB"'
        mkdir -p /root/t; inbounds_dump > /root/t/inb; clients > /root/t/cl
        /usr/local/still-ui/x-ui setting -show true | grep -E "^(hasDefaultCredential|port|webBasePath):" > /root/t/settings
        sqlite3 /etc/still-ui/x-ui.db "select key, value from settings order by key" > /root/t/settings-table
        sha256sum /etc/still-ui/install-result.env > /root/t/result; mainpid still-xray > /root/t/xray-pid'
    latest "$ct" "$NEW"
    ck "$ct" "'still-ui update' of $PREV to this build while a download runs through Xray" 'slow_start 40
        printf "y\n\n" | timeout 600 still-ui update > /root/update.log 2>&1; grep -q "updating finished" /root/update.log || { tail -n 30 /root/update.log; exit 1; }
        grep -q "Checksum verified" /root/update.log && grep -q "\"state\":\"success\"" /etc/still-ui/update-status.json || exit 1
        if grep -q "^still_run_release_script() {" /srv/gh/download/'"$PREV"'/x-ui.sh; then
            grep -q "update.sh of still-ui '"$NEW"' matches its SHA256SUMS" /root/update.log
        else
            grep -q "raw.githubusercontent.com GET /Dv2Corn/still-ui/main/update.sh" /srv/gh/access.log
        fi'
    ck "$ct" "the download through Xray survived the update" 'slow_check 40'
    ck "$ct" "update kept the Xray process" '[[ $(mainpid still-xray) == $(cat /root/t/xray-pid) ]] || { echo "still-xray PID $(cat /root/t/xray-pid) -> $(mainpid still-xray)"; exit 1; }'
    ck "$ct" "update kept inbounds, clients, settings and the login" '
        same_inbounds && [[ $(clients) == $(cat /root/t/cl) ]] || { echo inbounds/clients differ; exit 1; }
        diff /root/t/settings <(/usr/local/still-ui/x-ui setting -show true | grep -E "^(hasDefaultCredential|port|webBasePath):") &&
        sha256sum -c --quiet /root/t/result &&
        { diff /root/t/settings-table <(sqlite3 /etc/still-ui/x-ui.db "select key, value from settings order by key") || true; }'
    installed_checks "$ct" "updated"
    return "$CASE_FAILED"
}

run_image() { # IMAGE
    local image="$1" base rc=0 prev ct
    base="su-inst-$(echo "$image" | tr -c 'a-z0-9\n' '-' | tr -s '-')"
    fresh_case "$base-fresh" "$image" || rc=1
    [[ $KEEP == 1 ]] || incus delete -f "$base-fresh" > /dev/null 2>&1
    for prev in "${PREV_TAGS[@]}"; do
        ct="$base-from-$(echo "$prev" | tr -c 'a-z0-9\n' '-' | tr -s '-')"
        update_case "$ct" "$image" "$prev" || rc=1
        [[ $KEEP == 1 ]] || incus delete -f "$ct" > /dev/null 2>&1
    done
    return $rc
}

log "logs: $LOG_DIR"
pids=()
for image in "${IMAGES[@]}"; do
    run_image "$image" > "$LOG_DIR/run-${image//\//-}.out" 2>&1 &
    pids+=("$!")
done
FAILED=0
for i in "${!IMAGES[@]}"; do
    if wait "${pids[$i]}"; then r=PASS; else r=FAIL; FAILED=1; fi
    echo "===== ${IMAGES[$i]}: $r"
    cat "$LOG_DIR/run-${IMAGES[$i]//\//-}.out"
done
if [[ $MIGRATE == 1 ]]; then
    log "migrate.sh: 3x-ui v3.11.0 -> this build on ${IMAGES[0]}"
    STILL_TEST_CT_PREFIX=su-inst-mig KEEP=$KEEP bash "$SRC/scripts/test-migrate.sh" "$dl/$NEW/still-ui-linux-amd64.tar.gz" "${IMAGES[0]}:v3.11.0" ||
        FAILED=1
fi
exit "$FAILED"
