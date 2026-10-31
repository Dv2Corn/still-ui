#!/usr/bin/env bash
# still-ui: build Linux release archives locally, without GitHub Actions.
#
#   scripts/still-release.sh <output-dir> [amd64 arm64 ...]
#
# Needs bash, docker, curl, unzip, tar and sha256sum on a Linux x86_64 host.
# Produces <output-dir>/still-ui-linux-<arch>.tar.gz and a .sha256 next to it,
# in the layout install.sh/update.sh expect (the same as 3x-ui's release
# archives, with the top folder named still-ui):
#
#   still-ui/x-ui                 panel binary (static, musl)
#   still-ui/x-ui.sh              management script, installed as /usr/bin/still-ui
#   still-ui/x-ui.rc              OpenRC unit (Alpine)
#   still-ui/x-ui.service.*       systemd units (debian/arch/rhel)
#   still-ui/bin/xray-linux-<arch>, mtg-linux-<arch>, geo*.dat
#
# Mirrors .github/workflows/release.yml: frontend with Node from .nvmrc, Go from
# go.mod, Bootlin musl cross toolchains, Xray-core pinned below, geo files and
# mtg-multi from their latest releases. Downloads are cached in
# $STILL_RELEASE_CACHE (default ~/.cache/still-release).
set -euo pipefail

XRAY_VERSION="${XRAY_VERSION:-v26.10.21}"

die() { echo "still-release: $*" >&2; exit 1; }

[[ $# -ge 1 ]] || die "usage: $0 <output-dir> [arch...]"
OUT_DIR=$(mkdir -p "$1" && cd "$1" && pwd)
shift
ARCHES=("$@")
[[ ${#ARCHES[@]} -gt 0 ]] || ARCHES=(amd64 arm64)

SRC=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${STILL_RELEASE_CACHE:-$HOME/.cache/still-release}"
mkdir -p "$CACHE"/{npm,gomod,gobuild,toolchains,dl}
GO_VERSION=$(awk '/^go / {print $2; exit}' "$SRC/go.mod")
NODE_MAJOR=$(tr -d '[:space:]' < "$SRC/.nvmrc")
VERSION=$(tr -d '[:space:]' < "$SRC/internal/config/version")
UIDGID="$(id -u):$(id -g)"

fetch() { # url dest
    [[ -s "$2" ]] && return 0
    curl -fsSL --retry 5 --retry-delay 3 -o "$2.part" "$1"
    mv -f "$2.part" "$2"
}

echo "still-release: still-ui ${VERSION}, Go ${GO_VERSION}, Node ${NODE_MAJOR}, Xray ${XRAY_VERSION}, arches: ${ARCHES[*]}"

# 1. Frontend bundle -> internal/web/dist (embedded by go:embed).
docker run --rm -u "$UIDGID" -e HOME=/tmp -v "$CACHE/npm:/tmp/.npm" -v "$SRC:/src" -w /src/frontend \
    "node:${NODE_MAJOR}-bookworm-slim" sh -c 'npm ci --no-audit --no-fund && npm run build' > "$CACHE/frontend.log" 2>&1 \
    || { tail -40 "$CACHE/frontend.log"; die "frontend build failed"; }
[[ -f "$SRC/internal/web/dist/index.html" ]] || die "frontend build produced no internal/web/dist/index.html"

# mtg-multi (MTProto sidecar): resolve the latest tag once.
MTG_VER=$(curl -sf -o /dev/null -w '%{redirect_url}' "https://github.com/mhsanaei/mtg-multi/releases/latest" | sed -n 's#.*/releases/tag/##p')
[[ -n "$MTG_VER" ]] || die "could not resolve the latest mtg-multi release"

for arch in "${ARCHES[@]}"; do
    case "$arch" in
        amd64) bootlin="x86-64"; xray_zip="64"; goarch="amd64" ;;
        arm64) bootlin="aarch64"; xray_zip="arm64-v8a"; goarch="arm64" ;;
        *) die "unsupported arch $arch (amd64, arm64)" ;;
    esac

    # 2. Bootlin musl toolchain for a static CGO (sqlite) build.
    tc_base="https://toolchains.bootlin.com/downloads/releases/toolchains/${bootlin}/tarballs/"
    tc_name=$(curl -fsSL --retry 5 "$tc_base" | grep -oE "${bootlin}--musl--stable-[^\"]+\.tar\.xz" | sort -r | head -n1)
    [[ -n "$tc_name" ]] || die "no Bootlin musl toolchain for $bootlin"
    if [[ ! -d "$CACHE/toolchains/${tc_name%.tar.xz}" ]]; then
        fetch "${tc_base}${tc_name}" "$CACHE/dl/${tc_name}"
        tar -xf "$CACHE/dl/${tc_name}" -C "$CACHE/toolchains"
    fi

    # 3. Panel binary.
    docker run --rm -u "$UIDGID" -e HOME=/tmp -e GOFLAGS=-buildvcs=false \
        -e GOMODCACHE=/gomod -e GOCACHE=/gobuild -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH="$goarch" \
        -v "$CACHE/gomod:/gomod" -v "$CACHE/gobuild:/gobuild" -v "$CACHE/toolchains:/toolchains:ro" \
        -v "$SRC:/src" -w /src "golang:${GO_VERSION}-bookworm" sh -ec '
            tc=$(ls -d /toolchains/'"${tc_name%.tar.xz}"')
            export CC=$(find "$tc/bin" -name "*-gcc.br_real" -type f | head -n1)
            export PATH="$tc/bin:$PATH"
            go build -ldflags "-w -s -linkmode external -extldflags -static" -o /src/build-release/x-ui-'"$arch"' .'

    # 4. Package.
    stage=$(mktemp -d)
    trap 'rm -rf "$stage"' EXIT
    pkg="$stage/still-ui"
    mkdir -p "$pkg/bin"
    cp "$SRC/build-release/x-ui-$arch" "$pkg/x-ui"
    cp "$SRC/x-ui.sh" "$SRC/x-ui.rc" "$SRC/x-ui.service.debian" "$SRC/x-ui.service.arch" "$SRC/x-ui.service.rhel" "$SRC/still-xray.service" "$pkg/"
    chmod 755 "$pkg/x-ui" "$pkg/x-ui.sh"

    fetch "https://github.com/XTLS/Xray-core/releases/download/${XRAY_VERSION}/Xray-linux-${xray_zip}.zip" "$CACHE/dl/Xray-${XRAY_VERSION}-linux-${xray_zip}.zip"
    unzip -q -o "$CACHE/dl/Xray-${XRAY_VERSION}-linux-${xray_zip}.zip" xray -d "$stage"
    mv "$stage/xray" "$pkg/bin/xray-linux-$arch"
    chmod 755 "$pkg/bin/xray-linux-$arch"

    mtg_pkg="mtg-multi-${MTG_VER#v}-linux-${arch}"
    fetch "https://github.com/mhsanaei/mtg-multi/releases/download/${MTG_VER}/${mtg_pkg}.tar.gz" "$CACHE/dl/${mtg_pkg}.tar.gz"
    tar -xzf "$CACHE/dl/${mtg_pkg}.tar.gz" -C "$stage" "${mtg_pkg}/mtg-multi"
    mv "$stage/${mtg_pkg}/mtg-multi" "$pkg/bin/mtg-linux-$arch"
    chmod 755 "$pkg/bin/mtg-linux-$arch"

    # Geo files are refreshed for every build, like the CI release.
    curl -fsSL --retry 5 -o "$pkg/bin/geoip.dat" https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat
    curl -fsSL --retry 5 -o "$pkg/bin/geosite.dat" https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat
    curl -fsSL --retry 5 -o "$pkg/bin/geoip_IR.dat" https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat
    curl -fsSL --retry 5 -o "$pkg/bin/geosite_IR.dat" https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat
    curl -fsSL --retry 5 -o "$pkg/bin/geoip_RU.dat" https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat
    curl -fsSL --retry 5 -o "$pkg/bin/geosite_RU.dat" https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat

    archive="still-ui-linux-${arch}.tar.gz"
    tar -C "$stage" --owner=0 --group=0 -czf "$OUT_DIR/$archive" still-ui
    (cd "$OUT_DIR" && sha256sum "$archive" > "$archive.sha256")
    rm -rf "$stage"
    trap - EXIT
    echo "still-release: $OUT_DIR/$archive ($(du -h "$OUT_DIR/$archive" | cut -f1))"
done
