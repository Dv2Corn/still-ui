#!/usr/bin/env bash
# still-ui: build Linux release archives. .github/workflows/release.yml runs
# this on a version tag; it works the same on any Linux x86_64 host.
#
#   scripts/still-release.sh <output-dir> [amd64 arm64 ...]
#
# Needs bash, docker, git, curl, tar and sha256sum on a Linux x86_64 host.
# Produces <output-dir>/still-ui-linux-<arch>.tar.gz and a .sha256 next to it,
# in the layout install.sh/update.sh expect (the same as 3x-ui's release
# archives, with the top folder named still-ui), plus still-probe-linux-<arch>.tar.gz
# for scripts/still-probe-install.sh (still-probe/still-probe, still-probe/xray):
#
#   still-ui/x-ui                 panel binary (static, musl)
#   still-ui/x-ui.sh              management script, installed as /usr/bin/still-ui
#   still-ui/x-ui.rc              OpenRC unit (Alpine)
#   still-ui/x-ui.service.*       systemd units (debian/arch/rhel)
#   still-ui/migrate.sh           3x-ui -> still-ui migration and its rollback
#   still-ui/install.sh, update.sh  the installer and updater of this release
#   still-ui/bin/xray-linux-<arch>, mtg-linux-<arch>, geo*.dat
#
# install.sh, update.sh, x-ui.sh and migrate.sh are also written to
# <output-dir> as they are: the release publishes them next to the archives and
# lists them in SHA256SUMS, and the panel updater and the still-ui menu run
# them only when their sum matches.
#
# Every third-party input that ends up in an archive or builds one is pinned
# below and checked before use; a different sum stops the build:
#   - the node and golang build images, by digest;
#   - the Bootlin musl toolchain (the C compiler of the static panel binary),
#     by file name and sha256;
#   - mtg-multi (the MTProto sidecar), by tag and the sha256 of each archive;
#   - Xray: scripts/xray-build/build.sh builds Xray-core v26.10.31 with
#     still-ui's REALITY patch from source, pinned by commit, Go version and
#     sha256 (see scripts/xray-build/README.md).
# The geo files are data that has to stay current, so they come from the
# latest release of their repositories; the build prints the repository, tag
# and sha256 of each one, and the SBOM (scripts/still-sbom.sh) records every
# file of the archive with its sha256.
# Downloads and the Go caches are kept in $STILL_RELEASE_CACHE (default
# ~/.cache/still-release); cached downloads are checked again on every build.
set -euo pipefail

XRAY_VERSION="v26.10.31 (still-ui build, scripts/xray-build)"

# Build images. The tags must match .nvmrc and go.mod (checked below).
NODE_IMAGE=node:26-bookworm-slim@sha256:bc0a2afd3b74ce12222fc8bab99271e66257f021078f5f4a5d687a77d280c1d9
GO_IMAGE=golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea

# Bootlin musl cross toolchains: file name and sha256 (Bootlin publishes the
# sum next to each tarball as <name>.sha256).
BOOTLIN_RELEASE=stable-2026.08-1
# shellcheck disable=SC2034
BOOTLIN_SHA256_amd64=78d3a4683d6ac47b5ee73bd5bce210b55eb93dff1b137c61298af97eb0d2b5a6
# shellcheck disable=SC2034
BOOTLIN_SHA256_arm64=b388c480a48e8e9f9b99e3d14e69219c4d61e5a2424a82faecb88a015b781a60

# mtg-multi release and the sha256 of its linux-<arch> archives.
MTG_VER=v1.16.1
# shellcheck disable=SC2034
MTG_SHA256_amd64=c4a0ee4b6652530d93d9cfdb171492678dff1dbbed9d5702f6d72de8b132d167
# shellcheck disable=SC2034
MTG_SHA256_arm64=9a296cb8785f7a36d18b2537e7133757f09422dc033a5c42ea35d1b6b37e05af

# Base of the third-party downloads; tests point these at a local stub.
MTG_BASE_URL=${STILL_RELEASE_MTG_BASE_URL:-https://github.com/mhsanaei/mtg-multi/releases/download}
BOOTLIN_BASE_URL=${STILL_RELEASE_BOOTLIN_BASE_URL:-https://toolchains.bootlin.com/downloads/releases/toolchains}

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
[[ ${NODE_IMAGE} == "node:${NODE_MAJOR}-"*@sha256:* ]] ||
    die "NODE_IMAGE (${NODE_IMAGE}) does not match .nvmrc (${NODE_MAJOR}); pin the digest of node:${NODE_MAJOR}-bookworm-slim"
[[ ${GO_IMAGE} == "golang:${GO_VERSION}-"*@sha256:* ]] ||
    die "GO_IMAGE (${GO_IMAGE}) does not match go.mod (go ${GO_VERSION}); pin the digest of golang:${GO_VERSION}-bookworm"

# check_sha256 FILE WANT: dies unless FILE has sha256 WANT. A cached download
# that does not match is removed, so the next build fetches it again.
check_sha256() {
    local got
    [[ $2 =~ ^[0-9a-f]{64}$ ]] || die "no pinned sha256 for $(basename "$1")"
    got=$(sha256sum "$1" | cut -d' ' -f1)
    if [[ $got != "$2" ]]; then
        rm -f "$1"
        die "sha256 mismatch for $(basename "$1"): got $got, pinned $2"
    fi
}

fetch() { # url dest [sha256]
    if [[ ! -s "$2" ]]; then
        curl -fsSL --retry 5 --retry-delay 3 -o "$2.part" "$1" || die "download failed: $1"
        mv -f "$2.part" "$2"
    fi
    [[ $# -lt 3 ]] || check_sha256 "$2" "$3"
}

echo "still-release: still-ui ${VERSION}, Go ${GO_VERSION}, Node ${NODE_MAJOR}, Xray ${XRAY_VERSION}, mtg-multi ${MTG_VER}, Bootlin ${BOOTLIN_RELEASE}, arches: ${ARCHES[*]}"
echo "still-release: images ${NODE_IMAGE} ${GO_IMAGE}"

# The pinned third-party downloads are checked before the long builds, so a
# changed file fails the build in seconds.
for arch in "${ARCHES[@]}"; do
    case "$arch" in
        amd64) bootlin="x86-64" ;;
        arm64) bootlin="aarch64" ;;
        *) die "unsupported arch $arch (amd64, arm64)" ;;
    esac
    want_var="MTG_SHA256_${arch}"
    mtg_pkg="mtg-multi-${MTG_VER#v}-linux-${arch}"
    fetch "${MTG_BASE_URL}/${MTG_VER}/${mtg_pkg}.tar.gz" "$CACHE/dl/${mtg_pkg}.tar.gz" "${!want_var:-}"
    want_var="BOOTLIN_SHA256_${arch}"
    tc_name="${bootlin}--musl--${BOOTLIN_RELEASE}.tar.xz"
    fetch "${BOOTLIN_BASE_URL}/${bootlin}/tarballs/${tc_name}" "$CACHE/dl/${tc_name}" "${!want_var:-}"
done
[[ ${STILL_RELEASE_CHECK_ONLY:-0} == 1 ]] && { echo "still-release: pinned downloads verified (STILL_RELEASE_CHECK_ONLY=1)"; exit 0; }

# 1. Frontend bundle -> internal/web/dist (embedded by go:embed).
docker run --rm -u "$UIDGID" -e HOME=/tmp -v "$CACHE/npm:/tmp/.npm" -v "$SRC:/src" -w /src/frontend \
    "$NODE_IMAGE" sh -c 'npm ci --no-audit --no-fund && npm run build' > "$CACHE/frontend.log" 2>&1 \
    || { tail -40 "$CACHE/frontend.log"; die "frontend build failed"; }
[[ -f "$SRC/internal/web/dist/index.html" ]] || die "frontend build produced no internal/web/dist/index.html"

# Xray core for every arch, from source (fails on a sha256 other than the pinned one).
XRAY_DIR="$SRC/build-release/xray"
XRAY_BUILD_GOCACHE="$CACHE/xray-go" "$SRC/scripts/xray-build/build.sh" "$XRAY_DIR" "${ARCHES[@]}" \
    || die "xray build failed"

for arch in "${ARCHES[@]}"; do
    case "$arch" in
        amd64) bootlin="x86-64"; goarch="amd64" ;;
        arm64) bootlin="aarch64"; goarch="arm64" ;;
        *) die "unsupported arch $arch (amd64, arm64)" ;;
    esac

    # 2. Bootlin musl toolchain for a static CGO (sqlite) build, from the
    # tarball checked above. It is unpacked again on every build, so a
    # toolchain left in the cache by an earlier build is never used unchecked.
    tc_name="${bootlin}--musl--${BOOTLIN_RELEASE}.tar.xz"
    want_var="BOOTLIN_SHA256_${arch}"
    check_sha256 "$CACHE/dl/${tc_name}" "${!want_var}"
    rm -rf "${CACHE:?}/toolchains/${tc_name%.tar.xz}"
    tar -xf "$CACHE/dl/${tc_name}" -C "$CACHE/toolchains"
    [[ -d "$CACHE/toolchains/${tc_name%.tar.xz}" ]] || die "${tc_name} did not unpack to ${tc_name%.tar.xz}"

    # 3. Panel binary.
    docker run --rm -u "$UIDGID" -e HOME=/tmp -e GOFLAGS=-buildvcs=false \
        -e GOMODCACHE=/gomod -e GOCACHE=/gobuild -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH="$goarch" \
        -v "$CACHE/gomod:/gomod" -v "$CACHE/gobuild:/gobuild" -v "$CACHE/toolchains:/toolchains:ro" \
        -v "$SRC:/src" -w /src "$GO_IMAGE" sh -ec '
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
    cp "$SRC/x-ui.sh" "$SRC/x-ui.rc" "$SRC/x-ui.service.debian" "$SRC/x-ui.service.arch" "$SRC/x-ui.service.rhel" "$SRC/still-xray.service" "$SRC/migrate.sh" "$SRC/install.sh" "$SRC/update.sh" "$SRC/still-setup.sh" "$pkg/"
    chmod 755 "$pkg/x-ui" "$pkg/x-ui.sh" "$pkg/migrate.sh" "$pkg/install.sh" "$pkg/update.sh" "$pkg/still-setup.sh"

    # Patched Xray, built above; the build script already checked its sha256.
    install -m 755 "$XRAY_DIR/xray-linux-$arch" "$pkg/bin/xray-linux-$arch"

    mtg_pkg="mtg-multi-${MTG_VER#v}-linux-${arch}"
    want_var="MTG_SHA256_${arch}"
    check_sha256 "$CACHE/dl/${mtg_pkg}.tar.gz" "${!want_var}"
    tar -xzf "$CACHE/dl/${mtg_pkg}.tar.gz" -C "$stage" "${mtg_pkg}/mtg-multi"
    mv "$stage/${mtg_pkg}/mtg-multi" "$pkg/bin/mtg-linux-$arch"
    chmod 755 "$pkg/bin/mtg-linux-$arch"

    # Geo files are data that has to stay current, so they are not pinned:
    # each comes from the latest release of its repository, resolved to a tag
    # first. The repository, tag and sha256 of each go to the build log, and
    # the SBOM lists every file with its sha256.
    while read -r geo_name geo_repo geo_asset; do
        geo_tag=$(curl -fsS --retry 5 -o /dev/null -w '%{redirect_url}' "https://github.com/${geo_repo}/releases/latest" | sed -n 's#.*/releases/tag/##p')
        [[ -n "$geo_tag" ]] || die "could not resolve the latest release of ${geo_repo}"
        curl -fsSL --retry 5 -o "$pkg/bin/$geo_name" "https://github.com/${geo_repo}/releases/download/${geo_tag}/${geo_asset}" ||
            die "download failed: ${geo_repo}@${geo_tag}/${geo_asset}"
        [[ -s "$pkg/bin/$geo_name" ]] || die "empty download: ${geo_repo}@${geo_tag}/${geo_asset}"
        echo "still-release: geo $arch $geo_name $(sha256sum "$pkg/bin/$geo_name" | cut -d' ' -f1) ${geo_repo}@${geo_tag}/${geo_asset}"
    done <<'GEO'
geoip.dat Loyalsoldier/v2ray-rules-dat geoip.dat
geosite.dat Loyalsoldier/v2ray-rules-dat geosite.dat
geoip_IR.dat chocolate4u/Iran-v2ray-rules geoip.dat
geosite_IR.dat chocolate4u/Iran-v2ray-rules geosite.dat
geoip_RU.dat runetfreedom/russia-v2ray-rules-dat geoip.dat
geosite_RU.dat runetfreedom/russia-v2ray-rules-dat geosite.dat
GEO

    # Reachability probe agent: static, standard library only, shipped with the same Xray.
    docker run --rm -u "$UIDGID" -e HOME=/tmp -e GOFLAGS=-buildvcs=false \
        -e GOMODCACHE=/gomod -e GOCACHE=/gobuild -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH="$goarch" \
        -v "$CACHE/gomod:/gomod" -v "$CACHE/gobuild:/gobuild" \
        -v "$SRC:/src" -w /src "$GO_IMAGE" \
        go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "/src/build-release/still-probe-$arch" ./cmd/still-probe
    probe_stage="$stage/probe/still-probe"
    mkdir -p "$probe_stage"
    install -m 755 "$SRC/build-release/still-probe-$arch" "$probe_stage/still-probe"
    install -m 755 "$XRAY_DIR/xray-linux-$arch" "$probe_stage/xray"
    install -m 755 "$SRC/scripts/still-probe-install.sh" "$probe_stage/still-probe-install.sh"
    probe_archive="still-probe-linux-${arch}.tar.gz"
    tar -C "$stage/probe" --owner=0 --group=0 -czf "$OUT_DIR/$probe_archive" still-probe
    (cd "$OUT_DIR" && sha256sum "$probe_archive" > "$probe_archive.sha256")

    archive="still-ui-linux-${arch}.tar.gz"
    tar -C "$stage" --owner=0 --group=0 -czf "$OUT_DIR/$archive" still-ui
    (cd "$OUT_DIR" && sha256sum "$archive" > "$archive.sha256")
    rm -rf "$stage"
    trap - EXIT
    echo "still-release: $OUT_DIR/$archive ($(du -h "$OUT_DIR/$archive" | cut -f1)) $(cut -d' ' -f1 "$OUT_DIR/$archive.sha256")"
done
# The scripts users run as root are published next to the archives (and
# listed in SHA256SUMS by scripts/still-sbom.sh), byte for byte the copies in
# the archives.
for script in install.sh update.sh x-ui.sh migrate.sh; do
    install -m 755 "$SRC/$script" "$OUT_DIR/$script"
done
echo "still-release: published scripts: $(cd "$OUT_DIR" && sha256sum install.sh update.sh x-ui.sh migrate.sh | tr '\n' ' ')"
echo "still-release: bundled Xray:"
(cd "$XRAY_DIR" && for arch in "${ARCHES[@]}"; do sha256sum "xray-linux-$arch"; done)
