**English** | [Русский](README.ru.md)

# Embedded AmneziaWG under connection churn: relay buffers

Measured on 2 November 2026. This page explains what was measured, how, and what the numbers do and do not show. The scripts and every raw sample are next to it.

The change under test is [`b638e494`](https://github.com/Dv2Corn/still-ui/commit/b638e4948c72cf78810bb77509af9baaa74aa616) "amneziawg: reuse relay buffers, bound the bridge connect time". Before it, every TCP connection relayed through the embedded AmneziaWG allocated two fresh 32 KiB copy buffers. `net.TCPConn` bypassed the buffer passed to it through `ReadFrom`/`WriteTo`. After it, the buffers come from a `sync.Pool`.

It is compared with its parent [`107b7610`](https://github.com/Dv2Corn/still-ui/commit/107b7610312266c91e2fd1a94d511d71a83f31d1). Both are still-ui 3.11.0-still.1, and the two trees differ only by this commit.

There are two separate claims, with two separate tests:

1. **Allocations per relayed connection.** A Go benchmark of the relay alone.
2. **Memory of the whole panel under connection churn.** The real panel on a test server, with real AmneziaWG clients.

## 1. Allocations per relayed connection

`internal/amneziawgnet/relay_alloc_bench_test.go` relays one short connection per iteration: a 4-byte request and a 16 KiB reply, after which both sides close. The same file was placed into both trees.

| Build | Allocated per connection (6 runs) | Allocations per connection |
|---|---|---|
| `107b7610` (before) | 69 040 – 69 138 B | 69 |
| `b638e494` (fix) | 2 884 – 2 985 B | 69 |

That is about 24× fewer bytes allocated per relayed connection, with the same number of allocations: the two 32 KiB buffers are gone. Raw output is in [`results/relay-bench.txt`](results/relay-bench.txt). `ns/op` is noisy because each iteration opens two loopback TCP pairs, so no speed claim is made.

## 2. Panel memory under connection churn

### Builds

Release archives were built from each commit with [`scripts/still-release.sh`](../../scripts/still-release.sh): a static musl build, Xray-core v26.10.21 and mtg-multi 1.16.1 in both.

| Build | `still-ui-linux-amd64.tar.gz` sha256 |
|---|---|
| `107b7610` | `5c57a985e67fd263a55f8cc0319728bbd6af95b99bb5d988d1d74afe0adca804` |
| `b638e494` | `817c3d8015c176ffd3623476a73c8254ffd7d57ea87af1c8d8ff41a577111a94` |

### Test server

- **Host:** Intel i7-12700H with the frequency capped (P-cores 2.8 GHz, E-cores 2.5 GHz), Linux 6.8.
- **Two unprivileged Incus (LXC) containers, Ubuntu 24.04:**
  - **server** — 2 vCPU, 2 GiB. The panel runs as `x-ui.service`, with `MemoryMax` lifted for the test so that it cannot cut the peaks.
  - **clients** — 2 vCPU, 1 GiB:
    - 4 AmneziaWG clients, each in its own network namespace (`amneziawg-go` v3.1.20260814, userspace, brought up by `awg-quick`);
    - a small HTTP server (Python `http.server`, `/ping` returns 2 bytes) that the clients can reach only through the tunnel.
- **Database.** Before every run the panel gets a fresh copy of the same database (`use-build.sh`):

  | Inbounds | Client records |
  |---|---|
  | 2 AmneziaWG | 34 + 1 |
  | 3 VLESS | 56 |
  | 1 Hysteria | 2 |

  These are records in the database. Traffic comes from 4 AmneziaWG clients only.

### Load

Each of the 4 clients opens **25 new TCP connections per second, 100 per second in total**. Every connection connects, sends `GET /ping`, reads until the server closes, and closes ([`scripts/churn.py`](scripts/churn.py)). One load phase lasts 5 minutes, so about 30 000 connections per run. The panel's UI or API is not polled during the run.

### One run

1. A fresh start of the panel on the copied database.
2. 60 s of quiet.
3. 300 s of load.
4. 180 s of quiet.

[`scripts/sampler.sh`](scripts/sampler.sh) samples every 5 s:
- **RSS of the panel process** (`/proc/<pid>/smaps_rollup`);
- **RSS of its Xray child**;
- **`memory.current` of `x-ui.service`** — panel + Xray + page cache, the number `MemoryMax` limits.

**Three rounds, with the build order alternating:**

| Round | First | Second |
|---|---|---|
| 1 | before | fix |
| 2 | fix | before |
| 3 | before | fix |

```
IDLE=60 LOAD=300 COOL=180 RATE=25 bash scripts/run-bench.sh
python3 scripts/analyze.py results/memory results/churn
```

### Results

MiB, mean of 3 runs with min–max in brackets. Per-run numbers are in [`results/summary.md`](results/summary.md).

| | `107b7610` (before) | `b638e494` (fix) |
|---|---|---|
| Panel RSS, quiet before load | 93 (92–94) | 93 (93–93) |
| Panel RSS, mean under load | **399** (395–402) | **175** (175–176) |
| Panel RSS, peak under load | **525** (523–527) | **210** (208–211) |
| Panel RSS, last minute of the quiet phase | 355 (309–436) | 180 (172–187) |
| Xray child RSS under load | 47 | 47 |
| `x-ui.service` memory.current, peak | 496 (494–498) | 180 (179–181) |
| Connections completed / failed | 83 839 / 6 173 | 83 297 / 6 715 |
| Panel restarts, OOM kills | 0 | 0 |

**What this shows:**
- At this rate of short connections through the embedded AmneziaWG, the fixed panel holds about **2.3× less memory on average** under load.
- Its **peak is 2.5× lower**.
- The quiet-state memory and the Xray child are the same in both builds, so the difference comes from the panel's relay path.
- Without the fix, memory goes down slowly after the load: it is still 309–436 MiB two to three minutes later.

### Failed connections

In both builds some connections did not complete within the client's 10 s timeout: 6.3–8.0 % per run, 6.9 % (before) and 7.5 % (fix) per build.

What is known:
- During the first 20–30 s of each load phase almost none fail. After that it is a steady 5–10 % in every 10-second interval.
- The clients show many retransmitted SYNs inside the tunnel, so connection attempts are lost on the way.
- There are no UDP receive-buffer errors on either side.
- The target server is not saturated: no listen-queue overflows, about 6 % CPU.
- The panel logs no relay errors.

The cause is **not established**. It is the same in both builds (0.6 percentage points apart), while memory differs 2.3×, so it does not explain the memory difference.

### Limits

- One host, one load shape (short connections only), 5-minute loads. This is not a multi-day measurement, and it does not show how memory behaves over days.
- 4 active clients; the other client records are idle.
- The numbers describe this test server. They are not a promise for any other server or traffic.

## Files

- [`scripts/`](scripts/):
  - `run-bench.sh` — the whole programme;
  - `use-build.sh` — installs a build and a fresh copy of the database;
  - `sampler.sh` — the memory samples;
  - `churn.py`, `churn-all.sh` — the load;
  - `analyze.py` — the tables.
- [`results/memory/`](results/memory/) — one sampler CSV per run (every 5 s, with the phase).
- [`results/churn/`](results/churn/) — per-client load counters per second (`epoch,started,ok,failed,in_flight`).
- [`results/relay-bench.txt`](results/relay-bench.txt), [`results/summary.md`](results/summary.md).
