# Downloader benchmark: httpdl vs aria2c

Date: 2026-10-11. Code: [spike/bench](../spike/bench/main.go). Everything runs on loopback in the Go dev container; aria2c 1.37.0 is installed there only as a yardstick (GPL, never shipped).

Server: a 128 MiB random file, **4 MiB/s per connection** (like CDN per-connection throttling). "Slow routes": every 4th request runs 10x slower, standing in for a bad path or a congested edge server. Both clients use 16 connections; aria2c runs with `-x16 -s16 -k1M`. Every result is verified by sha256.

| Scenario | httpdl | aria2c | 1 connection |
|---|---|---|---|
| Clean network | 39–48 MiB/s | 38–48 MiB/s (tie, within noise) | 3.5 MiB/s |
| 25% slow routes | **27.0–28.6 MiB/s** | 19.4–21.6 MiB/s | 3.5 MiB/s |

How we got there (each step measured with slow routes):
1. Plain halving work stealing: 15.5 vs aria2c 22.1. A slow connection kept half its remainder.
2. Speed-aware split (the thief takes `rem * myRate / (myRate + ownerRate)`) and stealing from the segment with the latest ETA, not the largest: 16.9.
3. Watchdog: a request below 1/5 of the median speed for 2 s is restarted, like aria2's `--lowest-speed-limit` but relative: 17.5.
4. Finer granularity (steal anything above 256 KiB plus the in-flight read): **27–28.6**. The tail no longer waits on a slow connection.

Re-run: `docker compose -f compose.dev.yaml run --rm go sh -c 'apt-get update -qq && apt-get install -y -qq aria2 && go run ./spike/bench -slow 0.25'`

Not covered yet: real internet paths (by design, AGENTS.md rule 7), per-server connection limits (servers that reject more than N connections), HTTP/2 multiplexing.
