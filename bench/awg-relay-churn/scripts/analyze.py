#!/usr/bin/env python3
"""Summarise the still-ui AWG churn benchmark.

usage: analyze.py RES_DIR CHURN_DIR
RES_DIR   - sampler CSVs, one per run: r<round>-<commit>.csv (every 5 s)
CHURN_DIR - churn CSVs: r<round>-<commit>-c0N.csv (epoch,started,ok,failed,in_flight; last line = totals)

Memory is the panel process (x-ui) RSS from /proc/<pid>/smaps_rollup, and separately its Xray child;
"cgroup" is memory.current of x-ui.service (panel + Xray + page cache), the number MemoryMax caps.
"""
import csv
import glob
import os
import statistics
import sys

res_dir, churn_dir = sys.argv[1], sys.argv[2]
MB = 1024.0


def churn_totals(tag):
    s = o = f = 0
    for p in glob.glob(os.path.join(churn_dir, tag + "-c*.csv")):
        last = open(p).read().strip().splitlines()[-1].split(",")
        s += int(last[1]); o += int(last[2]); f += int(last[3])
    return s, o, f


rows = []
for p in sorted(glob.glob(os.path.join(res_dir, "r*.csv"))):
    tag = os.path.basename(p)[:-4]
    data = list(csv.DictReader(open(p)))
    by = {ph: [r for r in data if r["phase"] == ph] for ph in ("idle", "load", "cool")}
    if not by["load"] or not by["cool"]:
        continue
    rss = lambda rs: [int(r["xui_rss_kb"]) / MB for r in rs]
    xr = lambda rs: [int(r["xray_rss_kb"]) / MB for r in rs]
    cg = lambda rs: [int(r["cg_memory_current"]) / MB / MB for r in rs if r["cg_memory_current"]]
    cool_tail = by["cool"][-12:]  # last minute of the quiet phase
    s, o, f = churn_totals(tag)
    rows.append({
        "run": tag, "build": tag.split("-", 1)[1],
        "idle": statistics.median(rss(by["idle"])),
        "load_mean": statistics.mean(rss(by["load"])),
        "load_peak": max(rss(by["load"])),
        "after": statistics.median(rss(cool_tail)),
        "xray_load": statistics.mean(xr(by["load"])),
        "cg_peak": max(cg(by["load"]) or [0]),
        "restarts": by["cool"][-1]["nrestarts"],
        "conns_ok": o, "conns_failed": f,
    })

hdr = ["run", "idle", "load_mean", "load_peak", "after", "xray_load", "cg_peak", "conns_ok", "conns_failed", "restarts"]
print("| " + " | ".join(hdr) + " |")
print("|" + "---|" * len(hdr))
for r in rows:
    print("| " + " | ".join(
        (f"{r[h]:.0f}" if isinstance(r[h], float) else str(r[h])) for h in hdr) + " |")

print()
print("Per build (mean over runs, min-max in brackets), MiB:")
for b in sorted({r["build"] for r in rows}):
    rs = [r for r in rows if r["build"] == b]
    def agg(k):
        v = [r[k] for r in rs]
        return f"{statistics.mean(v):.0f} ({min(v):.0f}-{max(v):.0f})"
    print(f"  {b}: runs={len(rs)} idle={agg('idle')} load_mean={agg('load_mean')} "
          f"load_peak={agg('load_peak')} after={agg('after')} xray_load={agg('xray_load')} cg_peak={agg('cg_peak')} "
          f"conns_ok={sum(r['conns_ok'] for r in rs)} failed={sum(r['conns_failed'] for r in rs)}")
