#!/bin/bash
# run-bench.sh : on the host that runs the two containers (server SRV, clients NET). 3 rounds x 2 builds (order alternates), each run:
# fresh panel start on master-311.db -> IDLE s quiet -> LOAD s churn (4 clients x RATE/s) -> COOL s quiet
# (defaults 120/600/300; the published run used IDLE=60 LOAD=300 COOL=180 RATE=25).
# Samples (every 5 s) -> stage-awg:/root/still-bench/res/<run>.csv ; churn -> stage-awg-net:/var/log/awgstand/churn/
set -u
RATE=${RATE:-25}; IDLE=${IDLE:-120}; LOAD=${LOAD:-600}; COOL=${COOL:-300}
S="incus exec ${SRV:-stage-awg} --"; N="incus exec ${NET:-stage-awg-net} --"
$S mkdir -p /root/still-bench/res /run/xuimem
run() { # build tag
  local b=$1 tag=$2
  echo "== $(date -u +%FT%TZ) $tag ($b)"
  $S bash /root/still-bench/use-build.sh "$b" /root/still-bench/master-311.db
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
  if [ $((round % 2)) -eq 1 ]; then order="107b7610 b638e494"; else order="b638e494 107b7610"; fi
  for b in $order; do run "$b" "r$round-$b"; done
done
echo "== done $(date -u +%FT%TZ)"
