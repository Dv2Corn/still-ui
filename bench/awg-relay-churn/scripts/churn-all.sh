#!/bin/bash
# churn-all.sh RATE_PER_CLIENT SECONDS TAG [CLIENTS...] : run churn.py in every client's namespace at once,
# then print each client's last line (epoch,started,ok,failed,in_flight) and the totals.
r=${1:?}; s=${2:?}; tag=${3:?}; shift 3
cl=${*:-c01 c02 c03 c04}
mkdir -p /var/log/awgstand/churn
for c in $cl; do
  ip netns exec "$c" python3 -B /opt/awgstand/churn.py "$r" "$s" "/var/log/awgstand/churn/$tag-$c.csv" &
done
wait
for c in $cl; do tail -1 "/var/log/awgstand/churn/$tag-$c.csv"; done | awk -F, -v t="$tag" '{s+=$2; o+=$3; f+=$4} END {printf "%s started=%d ok=%d failed=%d (%.1f%%)\n", t, s, o, f, (s+f>0? 100*f/(o+f):0)}'
