#!/bin/bash
# Runs on the server. Every 5 s prints one CSV line about the panel (x-ui.service) and its Xray child.
# usage: sampler.sh RUN_LABEL ; the phase is read from /run/xuimem/phase
ver=${1:?}
roll() { # pid -> rss_kb,pss_kb,anon_kb
  awk '/^Rss:/{r=$2} /^Pss:/{p=$2} /^Anonymous:/{a=$2} END{printf "%s,%s,%s", r+0, p+0, a+0}' "/proc/$1/smaps_rollup" 2>/dev/null || printf '0,0,0'
}
st() { # pid -> hwm_kb,threads,fds,cpu_ticks
  local hwm thr fds cpu
  hwm=$(awk '/^VmHWM:/{print $2}' "/proc/$1/status" 2>/dev/null); thr=$(awk '/^Threads:/{print $2}' "/proc/$1/status" 2>/dev/null)
  fds=$(ls "/proc/$1/fd" 2>/dev/null | wc -l)
  cpu=$(awk '{print $14+$15}' "/proc/$1/stat" 2>/dev/null)
  printf '%s,%s,%s,%s' "${hwm:-0}" "${thr:-0}" "${fds:-0}" "${cpu:-0}"
}
echo "utc,epoch,version,phase,xui_pid,xui_rss_kb,xui_pss_kb,xui_anon_kb,xui_hwm_kb,xui_threads,xui_fds,xui_cpu_ticks,xray_pid,xray_rss_kb,xray_pss_kb,xray_anon_kb,xray_hwm_kb,xray_threads,xray_fds,xray_cpu_ticks,cg_memory_current,cg_memory_peak,cg_anon,cg_file,cg_oom_kill,nrestarts,native_rx,native_tx,mem_available_kb"
CG=/sys/fs/cgroup/system.slice/x-ui.service
nxt=$(date +%s)
while :; do
  xp=$(systemctl show -p MainPID --value x-ui.service); cp=$(pgrep -P "$xp" -f xray | head -1); cp=${cp:-0}
  phase=$(cat /run/xuimem/phase 2>/dev/null || echo none)
  nat=$(awg show all transfer 2>/dev/null | awk '{r+=$3; t+=$4} END{printf "%d,%d", r, t}')
  printf '%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n' \
    "$(date -u +%FT%TZ)" "$(date +%s)" "$ver" "$phase" \
    "$xp" "$(roll "$xp")" "$(st "$xp")" "$cp" "$(roll "$cp")" "$(st "$cp")" \
    "$(cat $CG/memory.current 2>/dev/null)" "$(cat $CG/memory.peak 2>/dev/null)" \
    "$(awk '/^anon /{print $2}' $CG/memory.stat 2>/dev/null)" "$(awk '/^file /{print $2}' $CG/memory.stat 2>/dev/null)" \
    "$(awk '/^oom_kill /{print $2}' $CG/memory.events 2>/dev/null)" \
    "$(systemctl show -p NRestarts --value x-ui.service)" "$nat" "$(awk '/^MemAvailable:/{print $2}' /proc/meminfo)"
  nxt=$((nxt + 5)); s=$((nxt - $(date +%s))); [ "$s" -gt 0 ] && sleep "$s"
done
