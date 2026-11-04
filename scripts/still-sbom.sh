#!/usr/bin/env bash
# still-ui: SBOMs and checksums for the archives scripts/still-release.sh built.
#
#   scripts/still-sbom.sh <dist-dir>
#
# For every <dist-dir>/still-ui-linux-<arch>.tar.gz writes
# still-ui-linux-<arch>.tar.gz.spdx.json (SPDX 2.3 JSON, made by syft from the
# unpacked archive: every file with its sha256, and the Go modules inside x-ui,
# xray and mtg), then <dist-dir>/SHA256SUMS over the archives, the SBOMs, the
# probe agent archives and the scripts still-release.sh published next to them
# (install.sh, update.sh, x-ui.sh, migrate.sh). The panel updater and the
# still-ui menu refuse to run a script whose sum is not in SHA256SUMS.
# Needs docker.
# The release workflow runs this; it works the same on any Linux host.
set -euo pipefail

SYFT_IMAGE=anchore/syft:v1.55.0@sha256:4cc92b64ca74ffca4c94d4c8fcc01f217731375300be14d0b6eeb2e82eaa110a

die() { echo "still-sbom: $*" >&2; exit 1; }

[[ $# -eq 1 ]] || die "usage: $0 <dist-dir>"
DIST=$(cd "$1" && pwd)
SRC=$(cd "$(dirname "$0")/.." && pwd)
VERSION=$(tr -d '[:space:]' < "$SRC/internal/config/version")

shopt -s nullglob
archives=("$DIST"/still-ui-linux-*.tar.gz)
[[ ${#archives[@]} -gt 0 ]] || die "no still-ui-linux-*.tar.gz in $DIST"

for archive in "${archives[@]}"; do
    name=$(basename "$archive")
    work=$(mktemp -d)
    tar -xzf "$archive" -C "$work"
    [[ -x "$work/still-ui/x-ui" ]] || die "$name has no still-ui/x-ui"
    docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -e SYFT_CHECK_FOR_APP_UPDATE=false \
        -e SYFT_FILE_METADATA_SELECTION=all -e SYFT_FILE_METADATA_DIGESTS=sha256 \
        -v "$work:/scan:ro" -v "$DIST:/out" "$SYFT_IMAGE" \
        scan "dir:/scan/still-ui" --source-name "${name%.tar.gz}" --source-version "$VERSION" \
        -o "spdx-json=/out/$name.spdx.json" -q
    rm -rf "$work"
    [[ -s "$DIST/$name.spdx.json" ]] || die "syft wrote no SBOM for $name"
    echo "still-sbom: $DIST/$name.spdx.json"
done

scripts=(install.sh update.sh x-ui.sh migrate.sh)
for script in "${scripts[@]}"; do
    [[ -s "$DIST/$script" ]] || die "$DIST/$script is missing (scripts/still-release.sh publishes it)"
done
# The probe agent archives (still-probe-linux-<arch>.tar.gz) are listed too, when present.
(cd "$DIST" && shopt -s nullglob && sha256sum -- still-ui-linux-*.tar.gz still-ui-linux-*.tar.gz.spdx.json still-probe-linux-*.tar.gz "${scripts[@]}" > SHA256SUMS)
echo "still-sbom: $DIST/SHA256SUMS"
cat "$DIST/SHA256SUMS"
