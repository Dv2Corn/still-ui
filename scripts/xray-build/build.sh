#!/usr/bin/env bash
# still-ui: build the Xray core shipped with the panel.
#
#   scripts/xray-build/build.sh <output-dir> [amd64 arm64 armv7 armv6 386 ...]
#
# Xray-core v26.10.31 with one change in its REALITY library
# (reality-accept-no-mlkem.patch): a Client Hello without an X25519MLKEM768 key
# share is accepted again, as it was before Xray 26.9.9. Duplicated key shares
# are still refused. Everything else is the upstream release. See README.md.
#
# Writes <output-dir>/xray-linux-<arch> and prints its sha256. For amd64 and
# arm64 the sum must equal the pinned one below, or the script fails: the build
# is reproducible, so a different sum means a different build.
#
# By default Go runs in the pinned golang image (needs git and docker). With
# XRAY_BUILD_NATIVE=1 the `go` in PATH is used instead; the Dockerfile does that
# inside the same pinned image. XRAY_BUILD_GOCACHE=<dir> keeps the Go module and
# build caches between runs (docker mode only). XRAY_BUILD_NO_VERIFY=1 skips the
# sum check (for experiments only).
set -euo pipefail

XRAY_TAG=v26.10.31
XRAY_COMMIT=a4a3dff5b20c2faae567b742c172c7b95e75c083
REALITY_COMMIT=83f621788ca88b83179810b12baa7ae518aacb83
GO_VERSION=go1.27.3
GO_IMAGE=golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b
# The version string `xray version` prints after the codename. It is part of the
# binary, so changing it changes the sums below.
XRAY_BUILD_NAME=sp-ios
LDFLAGS="-s -w -buildid= -X github.com/xtls/xray-core/core.build=${XRAY_BUILD_NAME}"

# Pinned sums of xray-linux-<arch>.
SHA256_amd64=67dfd3a25b349adc128112864973a9f1c9f3f4b58a3541d4d447374b9e6301aa
SHA256_arm64=7c5be31d6ec2a65c8d902eebf382d050c5a84d28d12325a8c0cd3da310c6af6d

die() { echo "xray-build: $*" >&2; exit 1; }

[[ $# -ge 1 ]] || die "usage: $0 <output-dir> [amd64 arm64 armv7 armv6 386 ...]"
OUT=$(mkdir -p "$1" && cd "$1" && pwd)
shift
ARCHES=("$@")
[[ ${#ARCHES[@]} -gt 0 ]] || ARCHES=(amd64 arm64)
HERE=$(cd "$(dirname "$0")" && pwd)
NATIVE=${XRAY_BUILD_NATIVE:-0}

if [[ $NATIVE == 1 ]]; then
    have=$(go env GOVERSION)
    [[ $have == "$GO_VERSION" ]] || echo "xray-build: warning: go is $have, the pinned sums need $GO_VERSION" >&2
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Sources: the Xray tag (checked against its commit) and the REALITY commit
# Xray v26.10.31 requires, with the patch applied.
git -c advice.detachedHead=false clone -q --depth 1 --branch "$XRAY_TAG" \
    https://github.com/XTLS/Xray-core "$WORK/xray"
[[ $(git -C "$WORK/xray" rev-parse HEAD) == "$XRAY_COMMIT" ]] || die "tag $XRAY_TAG is not commit $XRAY_COMMIT"
git init -q "$WORK/reality"
git -C "$WORK/reality" fetch -q --depth 1 https://github.com/XTLS/REALITY "$REALITY_COMMIT"
git -C "$WORK/reality" -c advice.detachedHead=false checkout -q FETCH_HEAD
git -C "$WORK/reality" apply "$HERE/reality-accept-no-mlkem.patch"
# The relative path is recorded in the binary's build info; keep it as is.
printf '\nreplace github.com/xtls/reality => ../reality\n' >> "$WORK/xray/go.mod"

# One Go run for all arches: `go mod tidy` once (the replace needs it), then a
# cross-compiled build per arch. XRAY_TARGETS is "goarch:goarm:name ...".
TARGETS=()
for arch in "${ARCHES[@]}"; do
    case "$arch" in
        amd64) TARGETS+=("amd64::amd64") ;;
        arm64) TARGETS+=("arm64::arm64") ;;
        armv7) TARGETS+=("arm:7:armv7") ;;
        armv6) TARGETS+=("arm:6:armv6") ;;
        386) TARGETS+=("386::386") ;;
        *) die "unsupported arch $arch (amd64, arm64, armv7, armv6, 386)" ;;
    esac
done
mkdir -p "$WORK/out"
GO_SCRIPT='set -e
    go mod tidy >/tmp/xray-tidy.log 2>&1 || { cat /tmp/xray-tidy.log >&2; exit 1; }
    for t in $XRAY_TARGETS; do
        goarch=${t%%:*}; rest=${t#*:}; goarm=${rest%%:*}; name=${rest#*:}
        if [ -n "$goarm" ]; then export GOARM="$goarm"; else unset GOARM; fi
        GOOS=linux GOARCH="$goarch" go build -trimpath -ldflags "$XRAY_LDFLAGS" -o "$XRAY_OUT/xray-linux-$name" ./main
    done'
if [[ $NATIVE == 1 ]]; then
    (cd "$WORK/xray" && env CGO_ENABLED=0 GOFLAGS=-buildvcs=false \
        XRAY_TARGETS="${TARGETS[*]}" XRAY_LDFLAGS="$LDFLAGS" XRAY_OUT="$WORK/out" sh -c "$GO_SCRIPT")
else
    cache=()
    if [[ -n ${XRAY_BUILD_GOCACHE:-} ]]; then
        mkdir -p "$XRAY_BUILD_GOCACHE"/{mod,build}
        cache=(-v "$XRAY_BUILD_GOCACHE/mod:/gomod" -v "$XRAY_BUILD_GOCACHE/build:/gobuild" -e GOMODCACHE=/gomod -e GOCACHE=/gobuild)
    fi
    docker run --rm --platform linux/amd64 -u "$(id -u):$(id -g)" -e HOME=/tmp "${cache[@]}" \
        -v "$WORK:/src" -w /src/xray -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
        -e XRAY_TARGETS="${TARGETS[*]}" -e XRAY_LDFLAGS="$LDFLAGS" -e XRAY_OUT=/src/out \
        "$GO_IMAGE" sh -c "$GO_SCRIPT"
fi

for arch in "${ARCHES[@]}"; do
    dest="$OUT/xray-linux-$arch"
    install -m 0755 "$WORK/out/xray-linux-$arch" "$dest"
    sum=$(sha256sum "$dest" | cut -d' ' -f1)
    echo "xray-build: $dest $sum"
    want_var="SHA256_$arch"
    want=${!want_var:-}
    if [[ -n $want && ${XRAY_BUILD_NO_VERIFY:-0} != 1 ]]; then
        [[ $sum == "$want" ]] || die "xray-linux-$arch sha256 $sum, pinned $want: not the pinned build"
    fi
done
