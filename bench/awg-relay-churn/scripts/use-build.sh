#!/bin/bash
# use-build.sh ARCHIVE DBFILE : stop the panel, install the release ARCHIVE (top folder x-ui/ or still-ui/)
# into /usr/local/x-ui, put a fresh copy of DBFILE as the panel's database, start, wait for the AWG port.
set -euo pipefail
a=${1:?archive}; db=${2:?db}
systemctl stop x-ui
rm -rf /usr/local/x-ui /tmp/still-unpack && mkdir -p /tmp/still-unpack
tar xzf "$a" -C /tmp/still-unpack
top=$(ls /tmp/still-unpack)
mv "/tmp/still-unpack/$top" /usr/local/x-ui
rm -f /root/still-bench/db/x-ui.db*
cp "$db" /root/still-bench/db/x-ui.db
systemctl reset-failed x-ui 2>/dev/null || true
systemctl start x-ui
for i in $(seq 60); do ss -lun | grep -q ':42528 ' && break; sleep 1; done
echo "archive $(basename "$a") sha256=$(sha256sum "$a" | cut -c1-16): $(systemctl is-active x-ui) pid=$(systemctl show -p MainPID --value x-ui) panel=$(/usr/local/x-ui/x-ui -v 2>&1 | head -1) xray=$(/usr/local/x-ui/bin/xray-linux-amd64 version 2>/dev/null | head -1 | cut -d' ' -f2)"
