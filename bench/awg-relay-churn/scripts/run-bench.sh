#!/bin/bash
# run-bench.sh : on the laptop. 3 rounds x 2 builds (order alternates), each run:
# fresh panel start on master-311.db -> 120 s idle -> 600 s churn (4 clients x RATE/s) -> 300 s idle.
# Samples (every 5 s) -> stage-awg:/root/still-bench/res/<run>.csv ; churn -> stage-awg-net:/var/log/awgstand/churn/
set -u
A=${A:-x-ui-v3.11.0}; B=${B:-still-ui-v3.11.0-still.4}
RATE=${RATE:-25}; IDLE=${IDLE:-120}; LOAD=${LOAD:-600}; COOL=${COOL:-300}
S="incus exec stage-awg --"; N="incus exec stage-awg-net --"
$S mkdir -p /root/still-bench/res /run/xuimem
run() { # build tag
  local b=$1 tag=$2
  echo "== $(date -u +%FT%TZ) $tag ($b)"
  $S bash /root/still-bench/use-build.sh "/root/still-bench/rel/$b.tar.gz" "${DB:-/root/still-bench/x-ui-370.db}"
  $S sh -c "echo idle > /run/xuimem/phase"
  $S sh -c "nohup bash /root/still-bench/sampler.sh $tag > /root/still-bench/res/$tag.csv 2>/dev/null & echo \$! > /run/xuimem/sampler.pid"
  sleep "$IDLE"
  $S sh -c "echo load > /run/xuimem/phase"
  $N bash /opt/awgstand/churn-all.sh "$RATE" "$LOAD" "$tag"
  $S sh -c "echo cool > /run/xuimem/phase"
  sleep "$COOL"
  $S sh -c 'kill $(cat /run/xuimem/sampler.pid)'
}
for round in 1 2 3; do
  if [ $((round % 2)) -eq 1 ]; then order="$A $B"; else order="$B $A"; fi
  for b in $order; do run "$b" "r$round-$b"; done
done
echo "== done $(date -u +%FT%TZ)"
