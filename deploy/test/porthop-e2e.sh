#!/usr/bin/env bash
# Hysteria2 port hopping end to end, in two throwaway containers on a private Docker network.
# "sut" (NET_ADMIN, nothing else) runs internal/sub's TestPortHopE2E: it saves a Hysteria
# inbound with the redirect on, loads the real nftables table and starts the inbound in a real
# Xray. "peer" runs a second Xray with the JSON subscription the test writes, so its packets
# reach sut from another network namespace, through the hop ports.
#
#   go install github.com/xtls/xray-core/main@<version in go.mod>
#   deploy/test/porthop-e2e.sh --xray "$(go env GOPATH)/bin/main" [--image IMAGE]
#
# IMAGE needs Go (go.mod's version), nft and a C compiler; the default installs nft on the fly.
# PORTHOP_E2E_DOCKER_ARGS adds arguments to sut's "docker run" (a Go cache volume, for example).
set -euo pipefail

xray=""
image=""
while [ $# -gt 0 ]; do
    case "$1" in
        --xray) xray="$2"; shift 2 ;;
        --image) image="$2"; shift 2 ;;
        *) echo "usage: $0 --xray /path/to/xray [--image IMAGE]" >&2; exit 2 ;;
    esac
done
[ -x "$xray" ] || { echo "$0: --xray must name an executable xray binary" >&2; exit 2; }

repo="$(cd "$(dirname "$0")/../.." && pwd)"
setup=":"
if [ -z "$image" ]; then
    image="golang:$(awk '$1 == "go" { print $2 }' "$repo/go.mod")"
    setup="apt-get update -qq && apt-get install -y -qq nftables > /dev/null"
fi
xray="$(cd "$(dirname "$xray")" && pwd)/$(basename "$xray")"
net="still-porthop-e2e-$$"
shared="$(mktemp -d)"
chmod 0777 "$shared"

# shellcheck disable=SC2317 # reached through the EXIT trap
cleanup() {
    docker rm -f "$net-peer" "$net-sut" > /dev/null 2>&1 || true
    docker network rm "$net" > /dev/null 2>&1 || true
    docker run --rm -v "$shared":/e2e "$image" sh -c 'rm -rf /e2e/* /e2e/.[!.]*' > /dev/null 2>&1 || true
    rmdir "$shared" 2> /dev/null || true
}
trap cleanup EXIT

docker network create --subnet 172.31.253.0/24 "$net" > /dev/null
docker run -d --name "$net-peer" --network "$net" --ip 172.31.253.3 \
    -v "$shared":/e2e -v "$xray":/usr/local/bin/xray:ro "$image" \
    sh -c 'until [ -f /e2e/client.json ]; do sleep 0.2; done; exec xray run -c /e2e/client.json > /e2e/client.log 2>&1' > /dev/null

status=0
# shellcheck disable=SC2086 # PORTHOP_E2E_DOCKER_ARGS is a list of arguments
docker run --rm --name "$net-sut" --network "$net" --ip 172.31.253.2 --cap-add NET_ADMIN \
    -v "$shared":/e2e -v "$xray":/usr/local/bin/xray:ro -v "$repo":/src -w /src \
    ${PORTHOP_E2E_DOCKER_ARGS:-} \
    -e GOFLAGS=-buildvcs=false -e XRAY_E2E_BINARY=/usr/local/bin/xray \
    -e XUI_PORTHOP_E2E_DIR=/e2e -e XUI_PORTHOP_E2E_SELF=172.31.253.2 -e XUI_PORTHOP_E2E_PEER=172.31.253.3 \
    "$image" sh -c "$setup && mkdir -p internal/web/dist && touch internal/web/dist/.gitkeep && go test -count=1 -timeout 15m -v -run '^TestPortHopE2E\$' ./internal/sub/" || status=$?

if [ "$status" -ne 0 ]; then
    echo "--- peer (client) log" >&2
    docker run --rm -v "$shared":/e2e "$image" sh -c 'tail -n 60 /e2e/client.log' >&2 || true
fi
exit "$status"
