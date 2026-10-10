# Engine spike: anacrolix/torrent v1.61.0

Date: 2026-10-10. Go 1.26.9, `CGO_ENABLED=0`, linux/amd64. Cross-compiles for arm64 and armv7.
Code: [spike/engine/](../spike/engine/). The interface draft is in `engine.go`, the checks in `spike_test.go`.
Run: `docker compose -f compose.dev.yaml run --rm test go test -v ./spike/engine/`. All tests pass in about 30 s.

**How it was tested (AGENTS.md rule 7):** the container has `network_mode: none` (loopback only), all peers are in-process clients on 127.0.0.1, and DHT, PEX and UPnP are off. All content is random bytes generated locally. The v2 and hybrid torrents were made by libtorrent 2.0.10 from our own files (`testdata/gen_v2.py`).

**Legend:** **pass** = works, shown by a test. **API** = the library has it, but no test (yet). **ours** = we build it in our layer. **gap** = missing or broken.

## Results (scope §5.9 and stream mode)

| Item | Result | Evidence / note |
|---|---|---|
| Basic download | pass | 4 MiB over loopback in ~0.5 s, sha256 matches |
| **Nothing downloads by default** | note | `AddTorrent` alone fetches nothing. Call `DownloadAll()`, `File.Download()`, or read through a Reader. `DownloadAll()` before the metadata arrives panics, so wait on `GotInfo()` first |
| Stream mode: seek moves priority | pass | HTTP `Range` at 12 of 16 MiB was answered 206 in ~45 ms with under 1% of the file downloaded. `http.ServeContent` over `File.NewReader()` with `SetReadahead` works unchanged |
| First and last pieces first | pass | `Piece(i).SetPriority(PiecePriorityNow)`. Both were done at ~6% overall |
| Metadata handling | pass + ours | Magnet → `GotInfo()` channel. The timeout and the "no peers found" outcome are a `select` in our layer |
| Duplicate detection | pass | `.torrent` and a magnet for the same infohash return the same `*Torrent` |
| File selection / per-file priority | pass | A skipped file (`PiecePriorityNone`) got 0 bytes. Pieces that span two files still download, as expected |
| Pause / resume | pass | `DisallowDataDownload` / `AllowDataDownload` |
| Force recheck | pass | `VerifyData()` found the corrupted piece and it was downloaded again (the peer must be re-added, because two complete seeds drop each other) |
| Incomplete files and resume | pass + ours | Incomplete files are kept as `name.part` (this is the spec's temp area). Piece state lives in `.torrent.bolt.db` (pure Go). **After a restart, progress shows 0 until `VerifyData()`**, so run a recheck on startup. It recovered 2.4 of 3.4 MiB; the remaining pieces were still in flight. The move-to-final-folder step is ours |
| Global bandwidth limits | pass | `DownloadRateLimiter` 1 MiB/s → 3 MiB took 2.8 s. Upload uses `UploadRateLimiter` (used in other tests) |
| Per-torrent limits | ours | No API. Build it as a per-torrent `rate.Limiter` on our read path, or as weighted priorities. Cheap |
| IP blocklist | pass | `IPBlocklist` (`iplist.New`) blocked 127.0.0.1: 0 peers. File/URL loading and refresh are ours (an `iplist` parser exists) |
| Protocol encryption | pass | `HeaderObfuscationPolicy{Preferred, RequirePreferred}`. With required on both sides it completes; plaintext-only against required transfers 0 bytes |
| uTP without cgo | pass | TCP disabled on both sides, completed over the pure-Go uTP (`utp_go.go`, picked when `!cgo`) |
| DHT / PEX / IPv6 toggles | API | `NoDHT`, `DisablePEX`, `DisableIPv6`, `DisableIPv4`. Not exercised, and DHT must never touch the public network here. Test later with private DHT nodes inside an isolated Docker network |
| Local service discovery (LSD, BEP 14) | gap (minor) | Not in v1.61 (no code in the module). Only finds peers on the same LAN; low value for a server. The toggle shows "not supported", and anonymous mode loses nothing |
| Anonymous mode | ours | Our switch sets the toggles above, plus a neutral client name (`Bep20`, `ExtendedHandshakeClientVersion`) |
| Network binding | API + ours | `ListenHost` binds listeners (tested with 127.0.0.1). The kill switch (watch the interface, pause everything when it drops) is ours |
| SOCKS5 proxy | API | Covers outgoing peer connections only (README); incoming connections are not proxied. Test with a local SOCKS server in M2 |
| Peer list, piece map, availability | pass | `PeerConns()`: `RemoteAddr`, `PeerClientName`, per-connection stats. `Stats()`: active peers, connected seeders, bytes. `PieceStateRuns()` gives the piece-map bar directly. The "why is it slow" hint is ours, built from these |
| Trackers | pass + ours | A peer was found only through a local HTTP tracker. `AddTrackers` / `ModifyTrackers` exist. **No per-tracker status API** (only `Client.WriteStatus` text), so tracker status display is ours/limited. Decide in M2 |
| Web seeds (BEP 19) | pass | No peers, only a local HTTP server → completed and verified. Web-seed URLs must go through the SSRF-guarded `HTTPDialContext` (ours) |
| BitTorrent hybrid (v1+v2) | pass | A libtorrent-made hybrid torrent was seeded and downloaded, data verified |
| **BitTorrent v2-only** | **gap** | `AddTorrent` panics (zero v1 infohash). Patching in the truncated v2 hash panics differently, and `Client.Close` then panics as well. v2-only torrents are rare in practice (hybrid is the norm). Report upstream; until it's fixed, reject v2-only with a clear message and `recover()` around the add |
| Path sanitization | pass | A file path containing `..` was rejected by `AddTorrent` ("is not sub path"). Our `os.Root` layer stays as defence in depth |
| UPnP / NAT-PMP | API | `NoDefaultPortForwarding` (off for us by default). Can't be tested without a real router; the reachability test is ours |
| Seeding policy (ratio, time, forever, stop at 100%) | ours | From `Stats()` bytes plus a timer, then `DisallowDataUpload` or drop. Simple |
| Archive extraction | ours | mholt/archives plus `os.Root` (scope §19) |
| Disk space guard | ours | `statfs` before starting and periodically, then pause |
| Stream-only cache with a size limit | ours | Custom or capped storage. Medium effort; build in M2 |
| Queue, max active, reorder | ours | Our scheduler on top of pause/resume |
| Post-completion pipeline | ours | Triggered by `Complete().On()` |

## Decision gate (scope §14)

**Keep anacrolix/torrent.** Every must-have item passes or is cheap to build in our layer. The single library gap (v2-only torrents) affects a rare format, while hybrid, the common v2 format, works, so it doesn't reach "more than a couple of must-haves failing". The build stays pure Go and static, and cross-compiles to arm64 and armv7. No libtorrent/cgo fallback is needed.

## Carried into step 2 / M2
- Engine layer: wait on `GotInfo` before `DownloadAll`; `VerifyData` on startup for unfinished torrents; `recover()` around adds; reject v2-only torrents.
- Guarded dialers: `HTTPDialContext` and `TrackerDialContext` → SSRF guard.
- Our-layer items above: per-torrent limits, seeding policy, kill switch, tracker status, stream cache, queue.
- Not yet run: the test binary on real arm64 (only cross-compiled), SOCKS5, private-DHT and IPv6 tests, and load with many peers (Docker swarm plus netem, still isolated).
