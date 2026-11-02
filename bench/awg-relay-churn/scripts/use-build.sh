#!/bin/bash
# use-build.sh COMMIT DBFILE : stop the panel, install still-ui build COMMIT into /usr/local/x-ui,
# put a fresh copy of DBFILE as the panel's database, start, wait for the AWG port.
set -euo pipefail
c=${1:?commit}; db=${2:?db}
systemctl stop x-ui
rm -rf /usr/local/x-ui /tmp/still-unpack && mkdir -p /tmp/still-unpack
tar xzf /root/still-ui-$c.tar.gz -C /tmp/still-unpack
mv /tmp/still-unpack/still-ui /usr/local/x-ui
rm -f /root/still-bench/db/x-ui.db*
cp "$db" /root/still-bench/db/x-ui.db
systemctl reset-failed x-ui 2>/dev/null || true
systemctl start x-ui
for i in $(seq 60); do ss -lun | grep -q ':42528 ' && break; sleep 1; done
echo "build $c: $(systemctl is-active x-ui) pid=$(systemctl show -p MainPID --value x-ui) sha=$(sha256sum /usr/local/x-ui/x-ui | cut -c1-16) xray=$(/usr/local/x-ui/bin/xray-linux-amd64 version 2>/dev/null | head -1 | cut -d' ' -f2)"
ss -lunp | grep -E ':4252[89] ' | awk '{print $5, $7}'
