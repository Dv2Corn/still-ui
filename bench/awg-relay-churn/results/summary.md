| run | idle | load_mean | load_peak | after | xray_load | cg_peak | conns_ok | conns_failed | restarts |
|---|---|---|---|---|---|---|---|---|---|
| r1-107b7610 | 92 | 395 | 523 | 436 | 47 | 494 | 28116 | 1888 | 0 |
| r1-b638e494 | 93 | 176 | 211 | 172 | 47 | 181 | 27758 | 2246 | 0 |
| r2-107b7610 | 94 | 402 | 527 | 319 | 47 | 498 | 28004 | 2000 | 0 |
| r2-b638e494 | 93 | 175 | 208 | 179 | 47 | 179 | 27617 | 2387 | 0 |
| r3-107b7610 | 94 | 401 | 526 | 309 | 47 | 497 | 27719 | 2285 | 0 |
| r3-b638e494 | 93 | 175 | 210 | 187 | 47 | 181 | 27922 | 2082 | 0 |

Per build (mean over runs, min-max in brackets), MiB:
  107b7610: runs=3 idle=93 (92-94) load_mean=399 (395-402) load_peak=525 (523-527) after=355 (309-436) xray_load=47 (47-47) cg_peak=496 (494-498) conns_ok=83839 failed=6173
  b638e494: runs=3 idle=93 (93-93) load_mean=175 (175-176) load_peak=210 (208-211) after=180 (172-187) xray_load=47 (47-47) cg_peak=180 (179-181) conns_ok=83297 failed=6715
