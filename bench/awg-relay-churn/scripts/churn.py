#!/usr/bin/python3
"""Short TCP connections at a fixed rate through the client's tunnel.

usage: churn.py RATE_PER_SEC SECONDS LOGFILE [HOST] [PORT] [MAX_INFLIGHT]

Every connection: connect, "GET /ping HTTP/1.0", read until the server closes.
Once a second prints: epoch, started, ok, failed, in_flight.
"""
import asyncio
import sys
import time

rate = float(sys.argv[1])
seconds = float(sys.argv[2])
log = open(sys.argv[3], "a", buffering=1)
host = sys.argv[4] if len(sys.argv) > 4 else "8.8.4.4"
port = int(sys.argv[5]) if len(sys.argv) > 5 else 80
max_inflight = int(sys.argv[6]) if len(sys.argv) > 6 else 400

stats = {"started": 0, "ok": 0, "failed": 0, "inflight": 0}
REQ = b"GET /ping HTTP/1.0\r\nHost: inet\r\n\r\n"


async def one():
    stats["inflight"] += 1
    try:
        r, w = await asyncio.wait_for(asyncio.open_connection(host, port), 10)
        w.write(REQ)
        await w.drain()
        data = await asyncio.wait_for(r.read(), 10)
        w.close()
        stats["ok" if data.startswith(b"HTTP/1.") else "failed"] += 1
    except Exception:
        stats["failed"] += 1
    finally:
        stats["inflight"] -= 1


async def report():
    while True:
        await asyncio.sleep(1)
        log.write("%d,%d,%d,%d,%d\n" % (time.time(), stats["started"], stats["ok"], stats["failed"], stats["inflight"]))


async def main():
    rep = asyncio.ensure_future(report())
    start = time.monotonic()
    interval = 1.0 / rate
    n = 0
    tasks = set()
    while time.monotonic() - start < seconds:
        due = start + n * interval
        delay = due - time.monotonic()
        if delay > 0:
            await asyncio.sleep(delay)
        n += 1
        if stats["inflight"] >= max_inflight:
            stats["failed"] += 1  # counted as a drop: the tunnel is not keeping up
            continue
        stats["started"] += 1
        t = asyncio.ensure_future(one())
        tasks.add(t)
        t.add_done_callback(tasks.discard)
    if tasks:
        await asyncio.wait(tasks, timeout=15)
    await asyncio.sleep(1.1)
    rep.cancel()


asyncio.run(main())
