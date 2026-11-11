#!/usr/bin/env bash
# End-to-end check of "x-ui import marzban" in Docker: a real Marzban (its official image)
# with users and inbounds, a still-ui image built from this tree, and a real Xray client
# that connects with the links Marzban gave -- to Marzban first, then, unchanged, to
# still-ui after the import. Needs docker, curl, jq and python3.
#
#   scripts/test-import-marzban.sh            build the still-ui image, then run
#   STILL_IMAGE=name scripts/test-import-marzban.sh   use an image built earlier
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
MARZBAN_IMAGE=${MARZBAN_IMAGE:-gozargah/marzban:v0.8.4}
P=${E2E_PREFIX:-still-import-e2e}
WORK=$(mktemp -d)
ADMIN_PASSWORD=$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')

log() { printf '\n== %s\n' "$*"; }
fail() {
    printf '\nFAIL: %s\n' "$*" >&2
    docker logs --tail 30 "$P-still" 2>&1 | sed 's/^/  still-ui: /' >&2 || true
    exit 1
}
cleanup() {
    # E2E_KEEP=1 leaves the containers and the work folder for a look after a failure.
    [[ -z "${E2E_KEEP:-}" ]] || { echo "kept: containers $P-*, folder $WORK"; return; }
    docker rm -f "$P-marzban" "$P-still" "$P-client" "$P-target" "$P-dest" > /dev/null 2>&1 || true
    docker network rm "$P" > /dev/null 2>&1 || true
    # Containers wrote into the folder as root.
    docker run --rm -v "$WORK":/w busybox sh -c 'rm -rf /w/* /w/.[!.]*' > /dev/null 2>&1 || true
    rm -rf "$WORK"
}
trap cleanup EXIT

if [[ -z "${STILL_IMAGE:-}" ]]; then
    STILL_IMAGE="$P-image"
    log "building the still-ui image from $ROOT"
    docker build -q -t "$STILL_IMAGE" "$ROOT"
fi

# still-ui's default routing keeps clients out of private networks, so the test network
# takes a range that is neither private nor reachable from here (it never leaves the bridge).
docker network create --subnet "${E2E_SUBNET:-11.201.77.0/24}" "$P" > /dev/null
mkdir -p "$WORK/marzban"

log "a web server that answers with the address it was reached from, and a TLS 1.3 site for REALITY"
docker run -d --name "$P-target" --network "$P" --network-alias target.e2e node:26 \
    node -e "require('http').createServer((q,s)=>s.end('from '+q.socket.remoteAddress.replace('::ffff:',''))).listen(80)" > /dev/null
# REALITY borrows the handshake of a real TLS 1.3 site that speaks HTTP/2.
docker run -d --name "$P-dest" --network "$P" --network-alias dest.e2e node:26 sh -c \
    'openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout /k -out /c -subj /CN=dest.e2e -days 2 2>/dev/null && exec node -e "
      const fs = require(\"fs\");
      require(\"http2\").createSecureServer({key: fs.readFileSync(\"/k\"), cert: fs.readFileSync(\"/c\"), allowHTTP1: true, minVersion: \"TLSv1.3\"}, (q, s) => s.end(\"dest\")).listen(443)"' > /dev/null

keys=$(docker run --rm --entrypoint xray "$MARZBAN_IMAGE" x25519)
private_key=$(awk '/Private/ {print $NF}' <<< "$keys")
[[ -n "$private_key" ]] || fail "xray x25519 gave no private key: $keys"
cat > "$WORK/marzban/xray_config.json" << EOF
{
  // The config a Marzban operator edits; the comment must survive the import.
  "log": {"loglevel": "warning"},
  "inbounds": [
    {"tag": "VLESS TCP REALITY", "listen": "0.0.0.0", "port": 8443, "protocol": "vless",
     "settings": {"clients": [], "decryption": "none"},
     "streamSettings": {"network": "tcp", "tcpSettings": {}, "security": "reality",
       "realitySettings": {"show": false, "dest": "dest.e2e:443", "xver": 0, "serverNames": ["dest.e2e"],
         "privateKey": "$private_key", "shortIds": ["0123abcd"]}},
     "sniffing": {"enabled": true, "destOverride": ["http", "tls"]}},
    {"tag": "VMESS WS", "listen": "0.0.0.0", "port": 8080, "protocol": "vmess", "settings": {"clients": []},
     "streamSettings": {"network": "ws", "wsSettings": {"path": "/vm"}, "security": "none"}},
    {"tag": "Shadowsocks TCP", "listen": "0.0.0.0", "port": 1080, "protocol": "shadowsocks",
     "settings": {"clients": [], "network": "tcp,udp"}}
  ],
  "outbounds": [{"protocol": "freedom", "tag": "DIRECT"}, {"protocol": "blackhole", "tag": "BLOCK"}]
}
EOF

log "starting Marzban ($MARZBAN_IMAGE)"
docker run -d --name "$P-marzban" --network "$P" --network-alias vpn.e2e \
    -v "$WORK/marzban":/var/lib/marzban \
    -e UVICORN_HOST=0.0.0.0 -e UVICORN_PORT=8000 -e SUDO_USERNAME=admin -e SUDO_PASSWORD="$ADMIN_PASSWORD" \
    -e XRAY_JSON=/var/lib/marzban/xray_config.json -e SQLALCHEMY_DATABASE_URL=sqlite:////var/lib/marzban/db.sqlite3 \
    "$MARZBAN_IMAGE" > /dev/null
# Without a certificate Marzban's API listens on its own loopback only, so curl runs
# inside its network namespace (the still-ui image has curl, Marzban's has none).
api=http://127.0.0.1:8000
mcurl() { docker run --rm --network "container:$P-marzban" --entrypoint curl "$STILL_IMAGE" -fsS "$@"; }
token=""
for _ in $(seq 60); do
    token=$(mcurl -d "username=admin" -d "password=$ADMIN_PASSWORD" "$api/api/admin/token" 2> /dev/null | jq -r .access_token) && [[ -n "$token" ]] && break
    sleep 1
done
[[ -n "$token" ]] || { docker logs --tail 30 "$P-marzban" >&2; fail "Marzban did not start"; }
mz() { mcurl -H "Authorization: Bearer $token" -H 'Content-Type: application/json' "$@"; }

host() { printf '[{"remark":"{USERNAME} %s","address":"vpn.e2e","port":null,"sni":null,"host":null,"path":null,"security":"inbound_default","alpn":"","fingerprint":""}]' "$1"; }
mz -X PUT "$api/api/hosts" -d "{\"VLESS TCP REALITY\":$(host reality),\"VMESS WS\":$(host ws),\"Shadowsocks TCP\":$(host ss)}" > /dev/null
mz -X POST "$api/api/user" -d '{"username":"alice","proxies":{"vless":{"flow":"xtls-rprx-vision"},"vmess":{},"shadowsocks":{}},
  "inbounds":{"vless":["VLESS TCP REALITY"],"vmess":["VMESS WS"],"shadowsocks":["Shadowsocks TCP"]},
  "data_limit":10737418240,"data_limit_reset_strategy":"month","expire":4102444800,"note":"e2e"}' > /dev/null
mz -X POST "$api/api/user" -d '{"username":"bob","proxies":{"vless":{}},"inbounds":{"vless":["VLESS TCP REALITY"]}}' > /dev/null
mz "$api/api/user/alice" > "$WORK/alice.json"
mz "$api/api/user/bob" > "$WORK/bob.json"
mz -X PUT "$api/api/user/bob" -d '{"status":"disabled"}' > /dev/null
jq -r '.links[]' "$WORK/alice.json" > "$WORK/alice.links"
bob_link=$(jq -r '.links[0]' "$WORK/bob.json")
sub_path=$(jq -r .subscription_url "$WORK/alice.json" | sed -E 's#^https?://[^/]+##')
[[ $(wc -l < "$WORK/alice.links") -eq 3 && "$sub_path" == /sub/* ]] || fail "unexpected Marzban user: $(cat "$WORK/alice.json")"
echo "Marzban gave alice:"
sed 's/^/  /' "$WORK/alice.links"

# One Xray client per link: a door on 8081 that leads through the link to the web server.
client_config() {
    python3 - "$1" << 'PY'
import base64, json, sys, urllib.parse as up
link = sys.argv[1]
if link.startswith("vmess://"):
    v = json.loads(base64.b64decode(link[8:]))
    out = {"protocol": "vmess", "settings": {"vnext": [{"address": v["add"], "port": int(v["port"]),
           "users": [{"id": v["id"], "security": v.get("scy", "auto")}]}]},
           "streamSettings": {"network": v["net"], "security": "none", "wsSettings": {"path": v.get("path", "/")}}}
elif link.startswith("ss://"):
    u = up.urlsplit(link)
    method, password = base64.b64decode(u.username + "=" * (-len(u.username) % 4)).decode().split(":", 1)
    out = {"protocol": "shadowsocks", "settings": {"servers": [{"address": u.hostname, "port": u.port, "method": method, "password": password}]}}
else:
    u = up.urlsplit(link)
    q = dict(up.parse_qsl(u.query))
    user = {"id": u.username, "encryption": "none"}
    if q.get("flow"):
        user["flow"] = q["flow"]
    out = {"protocol": "vless", "settings": {"vnext": [{"address": u.hostname, "port": u.port, "users": [user]}]},
           "streamSettings": {"network": q["type"], "security": "reality", "realitySettings": {
               "serverName": q["sni"], "fingerprint": q["fp"], "publicKey": q["pbk"], "shortId": q["sid"], "spiderX": q.get("spx", "")}}}
print(json.dumps({"log": {"loglevel": "warning"},
    "inbounds": [{"listen": "0.0.0.0", "port": 8081, "protocol": "dokodemo-door", "settings": {"address": "target.e2e", "port": 80, "network": "tcp"}}],
    "outbounds": [out]}))
PY
}

# through LINK prints the web server's answer when the link connects, and fails when not.
through() {
    local answer=""
    client_config "$1" > "$WORK/client.json"
    docker rm -f "$P-client" > /dev/null 2>&1 || true
    docker run -d --name "$P-client" --network "$P" -v "$WORK/client.json":/client.json:ro \
        --entrypoint xray "$MARZBAN_IMAGE" run -c /client.json > /dev/null
    for _ in 1 2 3 4 5; do
        # The Host header names the web server: the inbounds sniff HTTP and route by it.
        answer=$(docker run --rm --network "$P" busybox wget -qO- -T 5 --header "Host: target.e2e" "http://$P-client:8081/" 2> /dev/null) && break
        sleep 1
    done
    [[ -n "$answer" ]] || docker logs --tail 8 "$P-client" 2>&1 | sed 's/^/  client: /' >&2
    docker rm -f "$P-client" > /dev/null 2>&1 || true
    [[ -n "$answer" ]] && echo "$answer"
}
server_ip() { docker inspect -f "{{(index .NetworkSettings.Networks \"$P\").IPAddress}}" "$1"; }
check_links() {
    local server="$1" ip link answer
    ip=$(server_ip "$server")
    while read -r link; do
        answer=$(through "$link") || fail "${link%%:*} link does not connect to $server: $link"
        [[ "$answer" == "from $ip" ]] || fail "${link%%:*} link went around $server ($ip): $answer"
        echo "  ok  ${link%%:*} through $server ($ip)"
    done < "$WORK/alice.links"
}

log "the links work against Marzban"
check_links "$P-marzban"

log "stopping Marzban, starting still-ui in its place"
docker stop "$P-marzban" > /dev/null
before=$(cd "$WORK/marzban" && sha256sum db.sqlite3 xray_config.json)
docker run -d --name "$P-still" --network "$P" --network-alias vpn.e2e -p 127.0.0.1::2096 \
    -v "$WORK/marzban":/var/lib/marzban:ro -e XUI_ENABLE_FAIL2BAN=false "$STILL_IMAGE" > /dev/null
for _ in $(seq 60); do
    docker exec "$P-still" /app/x-ui setting -show > /dev/null 2>&1 && break
    sleep 1
done

log "dry run"
docker exec "$P-still" /app/x-ui import marzban /var/lib/marzban -dry-run | tee "$WORK/dry.txt"
grep -q 'would import: 3 client(s); source subscription links that keep working: 2' "$WORK/dry.txt" || fail "unexpected dry-run report"
through "$(head -n 1 "$WORK/alice.links")" > /dev/null 2>&1 && fail "a link connected after a dry run: it wrote something"

log "import"
docker exec "$P-still" /app/x-ui import marzban /var/lib/marzban | tee "$WORK/import.txt"
grep -q '^imported: 3 client(s)' "$WORK/import.txt" || fail "the import did not report 3 clients"
docker restart "$P-still" > /dev/null
for _ in $(seq 60); do
    through "$(head -n 1 "$WORK/alice.links")" > /dev/null 2>&1 && break
    sleep 1
done

log "the same links, unchanged, work against still-ui"
check_links "$P-still"
through "$bob_link" > /dev/null 2>&1 && fail "bob was disabled in Marzban but his link connects to still-ui"
echo "  ok  the user disabled in Marzban stays locked out"

log "the subscription link Marzban gave is served by still-ui"
sub="http://$(docker port "$P-still" 2096/tcp | head -n 1)$sub_path"
curl -fsS "$sub" | base64 -d > "$WORK/sub.txt" || fail "still-ui does not serve $sub_path"
python3 - "$WORK/alice.links" "$WORK/sub.txt" << 'PY' || fail "the subscription does not hold alice's links"
import base64, json, sys, urllib.parse as up
def ident(link):
    if link.startswith("vmess://"):
        v = json.loads(base64.b64decode(link[8:] + "=" * (-len(link[8:]) % 4)))
        return ("vmess", v["id"], v["add"], str(v["port"]), v.get("path", ""))
    u = up.urlsplit(link)
    q = dict(up.parse_qsl(u.query))
    return (u.scheme, up.unquote(u.username).rstrip("="), u.hostname, str(u.port), q.get("pbk", ""), q.get("sid", ""), q.get("flow", ""), q.get("sni", ""))
want = {ident(l) for l in open(sys.argv[1]).read().split()}
got = {ident(l) for l in open(sys.argv[2]).read().split()}
missing = want - got
print("  subscription links:", len(got), "missing:", sorted(missing))
sys.exit(1 if missing or len(got) != len(want) else 0)
PY
echo "  ok  $sub_path"

after=$(cd "$WORK/marzban" && sha256sum db.sqlite3 xray_config.json)
[[ "$before" == "$after" ]] || fail "the import changed Marzban's files"
echo "  ok  Marzban's database and config are byte-identical"

log "PASS"
