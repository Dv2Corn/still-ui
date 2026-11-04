#!/usr/bin/env bash
# Tests of the checksum gates around the scripts that run as root, with stubbed
# downloads (no root, no network, no docker):
#   - update.sh refuses an archive whose .sha256 is missing, unreadable or wrong,
#     and refuses the retired dev channel;
#   - install.sh refuses a missing or wrong .sha256 and an archive without the
#     files it installs from it;
#   - x-ui.sh (still-ui update / install / legacy / menu) runs a release script
#     only when the release's SHA256SUMS lists its sum;
#   - scripts/still-release.sh refuses a third-party download whose sum is not
#     the pinned one;
#   - scripts/still-probe-install.sh takes the token from the environment, a
#     file or standard input, and warns about --token.
# Variables assigned below are read by the functions under test.
# shellcheck disable=SC2034
set -uo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
failures=0

pass() { echo "ok   $1"; }
fail() {
    echo "FAIL $1"
    [[ -z "${2:-}" ]] || printf '     %s\n' "$2"
    failures=$((failures + 1))
}
# check DESCRIPTION WANT_RC(0|nonzero) OUTPUT_PATTERN RC OUTPUT
check() {
    local what="$1" want="$2" pattern="$3" rc="$4" out="$5"
    if [[ "$want" == 0 && "$rc" -ne 0 ]] || [[ "$want" != 0 && "$rc" -eq 0 ]]; then
        fail "$what" "exit status $rc; output: $out"
    elif [[ -n "$pattern" && ! "$out" =~ $pattern ]]; then
        fail "$what" "output does not match '$pattern': $out"
    else
        pass "$what"
    fi
}

# load_function FILE NAME: defines function NAME exactly as FILE has it.
load_function() {
    local definition
    definition=$(awk -v name="$2" '
        $0 == name "() {" {copy = 1}
        copy {print}
        copy && /^}$/ {exit}
    ' "$1")
    [[ -n "$definition" ]] || { echo "Missing function $2 in $1" >&2; exit 1; }
    eval "$definition"
}

red='' green='' yellow='' plain=''
sha() { sha256sum "$1" | awk '{print $1}'; }

# Stub for curl -o FILE -w '%{http_code}' URL: SIDECAR decides what the
# .sha256 download returns (ok, wrong, missing = 404, error = no answer).
# shellcheck disable=SC2317,SC2329
curl_stub() {
    local out="" prev=""
    for arg in "$@"; do
        [[ "$prev" == -o ]] && out="$arg"
        prev="$arg"
    done
    case "$SIDECAR" in
        ok) printf '%s  archive.tar.gz\n' "$(sha "$ARCHIVE")" > "$out"; printf 200 ;;
        wrong) printf '%s  archive.tar.gz\n' "$(printf x | sha256sum | awk '{print $1}')" > "$out"; printf 200 ;;
        missing) printf 'Not Found' > "$out"; printf 404 ;;
        error) printf 000; return 7 ;;
    esac
}

# --- update.sh ---------------------------------------------------------------
load_function "$repo_root/update.sh" _fail
load_function "$repo_root/update.sh" _verify_update_checksum
load_function "$repo_root/update.sh" _check_update_tag
curl_bin=curl_stub
ARCHIVE="$work/still-ui-linux-amd64.tar.gz"

for mode in ok wrong missing error; do
    printf 'release archive' > "$ARCHIVE"
    out=$(SIDECAR=$mode _verify_update_checksum "$ARCHIVE" "https://example.invalid/a.tar.gz.sha256" 2>&1)
    rc=$?
    case "$mode" in
        ok) check "update.sh accepts a matching .sha256" 0 "Checksum verified" "$rc" "$out" ;;
        wrong) check "update.sh refuses a wrong .sha256" 1 "Checksum mismatch" "$rc" "$out" ;;
        missing) check "update.sh refuses a missing .sha256 (404)" 1 "No checksum is published.*refusing" "$rc" "$out" ;;
        error) check "update.sh refuses an unreadable .sha256" 1 "Failed to download the checksum.*refusing" "$rc" "$out" ;;
    esac
    if [[ "$mode" != ok && -e "$ARCHIVE" ]]; then
        fail "update.sh removes a refused archive ($mode)"
    fi
done

out=$(_check_update_tag dev-latest 2>&1)
check "update.sh refuses the dev channel" 1 "no dev build" "$?" "$out"
out=$(_check_update_tag main 2>&1)
check "update.sh refuses a branch as XUI_UPDATE_TAG" 1 "not a release tag" "$?" "$out"
out=$(_check_update_tag v1.6.0 2>&1)
check "update.sh accepts a release tag" 0 "" "$?" "$out"
out=$(_check_update_tag "" 2>&1)
check "update.sh accepts no tag (latest release)" 0 "" "$?" "$out"

# --- install.sh --------------------------------------------------------------
load_function "$repo_root/install.sh" verify_release_checksum
load_function "$repo_root/install.sh" require_archive_files
# shellcheck disable=SC2317,SC2329
curl() { curl_stub "$@"; }

for mode in ok wrong missing error; do
    printf 'release archive' > "$ARCHIVE"
    out=$(SIDECAR=$mode verify_release_checksum "https://example.invalid/a.tar.gz" "$ARCHIVE" 2>&1)
    rc=$?
    case "$mode" in
        ok) check "install.sh accepts a matching .sha256" 0 "Checksum verified" "$rc" "$out" ;;
        wrong) check "install.sh refuses a wrong .sha256" 1 "Checksum mismatch" "$rc" "$out" ;;
        missing) check "install.sh refuses a missing .sha256 (404)" 1 "No checksum is published.*Refusing" "$rc" "$out" ;;
        error) check "install.sh refuses an unreadable .sha256" 1 "Failed to download the checksum.*Refusing" "$rc" "$out" ;;
    esac
done
unset -f curl

mkdir -p "$work/pkg/still-ui"
printf 'bin' > "$work/pkg/still-ui/x-ui"
printf 'menu' > "$work/pkg/still-ui/x-ui.sh"
tar -C "$work/pkg" -czf "$ARCHIVE" still-ui
out=$(require_archive_files "$ARCHIVE" x-ui.sh 2>&1)
check "install.sh finds x-ui.sh in the archive" 0 "" "$?" "$out"
out=$(require_archive_files "$ARCHIVE" x-ui.sh x-ui.service.debian 2>&1)
check "install.sh refuses an archive without its unit" 1 "has no still-ui/x-ui.service.debian" "$?" "$out"

# install.sh, update.sh and the units come from the verified archive only.
for f in install.sh update.sh; do
    if grep -n 'raw\.githubusercontent\.com' "$repo_root/$f" > /dev/null; then
        fail "$f downloads nothing from raw.githubusercontent.com" "$(grep -n 'raw\.githubusercontent\.com' "$repo_root/$f")"
    else
        pass "$f downloads nothing from raw.githubusercontent.com"
    fi
done
if grep -n 'raw\.githubusercontent\.com' "$repo_root/x-ui.sh" | grep -v 'LOGE\|echo' > /dev/null; then
    fail "x-ui.sh mentions raw.githubusercontent.com only in messages" "$(grep -n 'raw\.githubusercontent\.com' "$repo_root/x-ui.sh")"
else
    pass "x-ui.sh mentions raw.githubusercontent.com only in messages"
fi

# --- x-ui.sh: scripts from a release -----------------------------------------
load_function "$repo_root/x-ui.sh" still_sha256_of
load_function "$repo_root/x-ui.sh" still_release_script
load_function "$repo_root/x-ui.sh" still_run_release_script
# shellcheck disable=SC2317,SC2329
LOGE() { echo "[ERR] $*"; }
# shellcheck disable=SC2317,SC2329
still_latest_tag() { echo v9.9.0; }
still_release_repo="Dv2Corn/still-ui"
rel="$work/releases"
export STILL_RELEASE_DOWNLOAD_BASE="file://$rel"

make_release() { # TAG MODE
    local d="$rel/$1"
    mkdir -p "$d"
    # shellcheck disable=SC2016
    printf 'echo "RAN update.sh of $XUI_UPDATE_TAG with $*"\n' > "$d/update.sh"
    printf 'other' > "$d/still-ui-linux-amd64.tar.gz"
    case "$2" in
        ok) (cd "$d" && sha256sum still-ui-linux-amd64.tar.gz update.sh > SHA256SUMS) ;;
        tampered) (cd "$d" && sha256sum still-ui-linux-amd64.tar.gz update.sh > SHA256SUMS) && printf 'echo PWNED\n' > "$d/update.sh" ;;
        nosums) ;;
        unlisted) (cd "$d" && sha256sum still-ui-linux-amd64.tar.gz > SHA256SUMS) ;;
    esac
}
make_release v9.9.0 ok
make_release v9.9.1 tampered
make_release v9.9.2 nosums
make_release v9.9.3 unlisted

out=$(still_run_release_script v9.9.0 update.sh arg1 2>&1)
check "x-ui.sh runs a release script listed in SHA256SUMS, for that release" 0 "RAN update.sh of v9.9.0 with arg1" "$?" "$out"
out=$(still_run_release_script "" update.sh 2>&1)
check "x-ui.sh resolves the latest release when no tag is given" 0 "RAN update.sh of v9.9.0" "$?" "$out"
out=$(still_run_release_script v9.9.1 update.sh 2>&1)
rc=$?
check "x-ui.sh refuses a script whose sum differs from SHA256SUMS" 1 "Checksum mismatch" "$rc" "$out"
[[ "$out" != *PWNED* ]] || fail "x-ui.sh did not run the tampered script" "$out"
out=$(still_run_release_script v9.9.2 update.sh 2>&1)
check "x-ui.sh refuses a release without SHA256SUMS" 1 "Could not download SHA256SUMS" "$?" "$out"
out=$(still_run_release_script v9.9.3 update.sh 2>&1)
check "x-ui.sh refuses a script SHA256SUMS does not list" 1 "does not list update.sh" "$?" "$out"
out=$(still_run_release_script v9.9.9 update.sh 2>&1)
check "x-ui.sh refuses a missing release script" 1 "Could not download update.sh" "$?" "$out"
out=$(still_run_release_script main update.sh 2>&1)
check "x-ui.sh refuses a branch instead of a release tag" 1 "Not a release tag" "$?" "$out"
unset STILL_RELEASE_DOWNLOAD_BASE

# --- scripts/still-release.sh: pinned third-party downloads -------------------
release_sh="$repo_root/scripts/still-release.sh"
mtg_ver=$(sed -n 's/^MTG_VER=//p' "$release_sh")
mkdir -p "$work/mtg/$mtg_ver" "$work/bootlin" "$work/rcache"
mtg_file="mtg-multi-${mtg_ver#v}-linux-amd64.tar.gz"
printf 'not the pinned mtg' > "$work/mtg/$mtg_ver/$mtg_file"
out=$(STILL_RELEASE_CHECK_ONLY=1 STILL_RELEASE_CACHE="$work/rcache" \
    STILL_RELEASE_MTG_BASE_URL="file://$work/mtg" STILL_RELEASE_BOOTLIN_BASE_URL="file://$work/bootlin" \
    bash "$release_sh" "$work/rout" amd64 2>&1)
rc=$?
check "still-release.sh refuses an mtg-multi archive with another sha256" 1 "sha256 mismatch for ${mtg_file}" "$rc" "$out"
if [[ -e "$work/rcache/dl/$mtg_file" ]]; then
    fail "still-release.sh drops the mismatching download from its cache"
else
    pass "still-release.sh drops the mismatching download from its cache"
fi
rm -f "$work/mtg/$mtg_ver/$mtg_file"
out=$(STILL_RELEASE_CHECK_ONLY=1 STILL_RELEASE_CACHE="$work/rcache" \
    STILL_RELEASE_MTG_BASE_URL="file://$work/mtg" STILL_RELEASE_BOOTLIN_BASE_URL="file://$work/bootlin" \
    bash "$release_sh" "$work/rout" amd64 2>&1)
check "still-release.sh stops when a pinned download fails" 1 "download failed" "$?" "$out"

load_function "$release_sh" check_sha256
# shellcheck disable=SC2317,SC2329
die() { echo "still-release: $*"; exit 1; }
printf 'toolchain' > "$work/tc.tar.xz"
out=$(check_sha256 "$work/tc.tar.xz" "$(sha "$work/tc.tar.xz")" 2>&1)
check "still-release.sh accepts the pinned sum" 0 "" "$?" "$out"
out=$(check_sha256 "$work/tc.tar.xz" "" 2>&1)
check "still-release.sh refuses a file without a pinned sum" 1 "no pinned sha256" "$?" "$out"
for v in MTG_SHA256_amd64 MTG_SHA256_arm64 BOOTLIN_SHA256_amd64 BOOTLIN_SHA256_arm64; do
    if grep -Eq "^${v}=[0-9a-f]{64}$" "$release_sh"; then pass "still-release.sh pins $v"; else fail "still-release.sh pins $v"; fi
done
for v in NODE_IMAGE GO_IMAGE; do
    if grep -Eq "^${v}=[^ ]+@sha256:[0-9a-f]{64}$" "$release_sh"; then pass "still-release.sh pins $v by digest"; else fail "still-release.sh pins $v by digest"; fi
done

# --- scripts/still-probe-install.sh: the token -------------------------------
probe_sh="$repo_root/scripts/still-probe-install.sh"
tok=abcdefghijABCDEFGHIJ0123456789abcdefghijABCDEFGH
# shellcheck disable=SC2016
read_token_cmd='script=$1; shift; source "$script" "$@"; read_token; echo "token=$token env=${STILL_PROBE_TOKEN-unset}"'

out=$(STILL_PROBE_TOKEN=$tok bash -c "$read_token_cmd" test "$probe_sh" < /dev/null 2>&1)
check "probe installer reads STILL_PROBE_TOKEN and unsets it" 0 "token=$tok env=unset" "$?" "$out"
printf '%s\n' "$tok" > "$work/token"
out=$(bash -c "$read_token_cmd" test "$probe_sh" --token-file "$work/token" < /dev/null 2>&1)
check "probe installer reads --token-file" 0 "token=$tok env=unset" "$?" "$out"
out=$(printf '%s\n' "$tok" | bash -c "$read_token_cmd" test "$probe_sh" --token-file - 2>&1)
check "probe installer reads --token-file - from standard input" 0 "token=$tok" "$?" "$out"
out=$(printf '%s\n' "$tok" | bash -c "$read_token_cmd" test "$probe_sh" 2>&1)
check "probe installer reads the token from standard input by default" 0 "token=$tok" "$?" "$out"
out=$(bash -c "$read_token_cmd" test "$probe_sh" --token "$tok" < /dev/null 2>&1)
check "probe installer still takes --token, with a warning" 0 "warning: --token puts the token on the command line.*token=$tok" "$?" "$out"
out=$(bash -c "$read_token_cmd" test "$probe_sh" --token-file "$work/none" < /dev/null 2>&1)
check "probe installer refuses an unreadable --token-file" 1 "cannot read --token-file" "$?" "$out"

if [[ $failures -gt 0 ]]; then
    echo "$failures release-script test(s) failed"
    exit 1
fi
echo "all release-script tests passed"
