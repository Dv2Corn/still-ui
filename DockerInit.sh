#!/bin/sh
set -e
# $1 is Docker's TARGETARCH and $2 its TARGETVARIANT (v6/v7 on arm). The
# platform names match the release archives and the mtg-multi packages.
# $3 is the Xray binary built by scripts/xray-build/build.sh (the Dockerfile's
# "xray" stage): still-ui ships its patched core, not the official release.
XRAY_PREBUILT=${3:-}
if [ -z "$XRAY_PREBUILT" ] || [ ! -f "$XRAY_PREBUILT" ]; then
    echo "DockerInit: pass the Xray binary from scripts/xray-build/build.sh as the third argument" >&2
    exit 1
fi
case $1 in
    amd64 | "")
        PLATFORM="amd64"
        ;;
    arm64 | armv8 | aarch64)
        PLATFORM="arm64"
        ;;
    arm | arm32 | armv7)
        PLATFORM="armv7"
        if [ "$2" = "v6" ]; then
            PLATFORM="armv6"
        fi
        ;;
    armv6)
        PLATFORM="armv6"
        ;;
    386 | i386)
        PLATFORM="386"
        ;;
    *)
        echo "DockerInit: unsupported architecture '$1'" >&2
        exit 1
        ;;
esac
# The panel looks binaries up by GOARCH; on arm Xray's is "arm32"
# (internal/xray/process.go) and mtg's plain "arm", as install.sh renames them.
case $PLATFORM in
    armv7 | armv6)
        XRAY_BIN="xray-linux-arm32"
        MTG_BIN="mtg-linux-arm"
        ;;
    *)
        XRAY_BIN="xray-linux-${PLATFORM}"
        MTG_BIN="mtg-linux-${PLATFORM}"
        ;;
esac
MTG_MULTI_VER=$(curl -sfL "https://api.github.com/repos/mhsanaei/mtg-multi/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
if [ -z "$MTG_MULTI_VER" ]; then
    echo "DockerInit: could not resolve the latest mtg-multi release tag" >&2
    exit 1
fi
mkdir -p build/bin
cd build/bin
install -m 755 "$XRAY_PREBUILT" "${XRAY_BIN}"
# mtg-multi (MTProto sidecar) ships prebuilt release binaries for every target
# we package, so download and unpack the matching one instead of compiling.
MTG_PKG="mtg-multi-${MTG_MULTI_VER#v}-linux-${PLATFORM}"
curl -sfLRO "https://github.com/mhsanaei/mtg-multi/releases/download/${MTG_MULTI_VER}/${MTG_PKG}.tar.gz"
tar -xzf "${MTG_PKG}.tar.gz"
mv "${MTG_PKG}/mtg-multi" "${MTG_BIN}"
rm -rf "${MTG_PKG}" "${MTG_PKG}.tar.gz"
chmod +x "${MTG_BIN}"
curl -sfLRO https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat
curl -sfLRO https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat
curl -sfLRo geoip_IR.dat https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat
curl -sfLRo geosite_IR.dat https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat
curl -sfLRo geoip_RU.dat https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat
curl -sfLRo geosite_RU.dat https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat
cd ../../
