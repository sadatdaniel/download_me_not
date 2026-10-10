# Architecture: built not to break

Goal: a server people install once and forget. The approach: **list everything that can break, and for each one have a proven pattern that contains it, detects it, and repairs it on its own.** Prefer patterns that mature systems already use (Kubernetes, OS updaters, Erlang, SQLite) over inventing new ones.

## Five rules

1. **Crash-only.** The server has no special "clean shutdown" path that recovery depends on. Startup *is* recovery: read the DB, look at the disk and the engine, and fix the difference. A `kill -9` or a power cut is a normal event, and a test does exactly that (M1).
2. **Reconcile, don't script.** The DB holds the *desired* state of every job ("downloading, stream mode, 2 files skipped"). A reconcile loop compares it with the *actual* state (engine, files on disk, helper processes) every few seconds and on every event, then takes one step towards the desired state. Same idea as Kubernetes controllers. A missed event or a restart heals itself on the next pass.
3. **Bulkheads.** Every job runs in its own supervised goroutine with `recover()`. A panic in one torrent, a hung yt-dlp or a corrupt archive fails *that job* with a clear error, never the server. Each provider can be switched off on its own, so a broken one never takes the others down.
4. **Last known good everywhere.** Anything that updates keeps the previous version and a self-test. If the self-test fails after an update, it reverts automatically: the server binary (confirm-after-boot, scope §5.8), yt-dlp and rclone (same pattern), the DB (backup before each migration, already built), and the config (atomic write, already built).
5. **Timeouts and limits on everything.** Every network call, helper process and archive extraction gets a `context` deadline, a size cap and a bounded queue. Nothing waits forever and nothing grows without limit.

## Breakage map

| What breaks | How often | Contain | Detect | Self-repair |
|---|---|---|---|---|
| **YouTube changes break yt-dlp** | Weekly-ish | yt-dlp is a separate process behind the video provider; only that provider fails | An extractor error class in yt-dlp's output | 1) daily auto-update with keep-previous; 2) **on an extractor error, retry once with the yt-dlp nightly channel** (fixes often land there first); 3) if still failing, mark the provider `error` with yt-dlp's message. The rest of the server is unaffected |
| yt-dlp update itself is bad | Rare | Same | Self-test after update (`--version` plus extraction of a local fixture page) | Revert to the kept previous binary |
| JS runtime (deno) needed by YouTube changes | Occasional | Managed tool like yt-dlp | `doctor` / the self-test | Same keep-previous update path |
| **Torrent library bug** (like the v2-only panic) | Per release | Engine interface plus `recover()` around engine calls; the job fails, not the server | Our engine tests plus offline swarm tests in CI | Dependabot PRs run the full offline suite; only green updates merge. The interface still allows swapping in libtorrent |
| Go dependency update breaks something | Per release | Pinned in `go.sum` | CI on every Dependabot PR | Merge only when green; GoReleaser builds only from tags |
| **Server update is bad** | Rare | Previous binary kept | Health check plus self-test after start | Confirm-after-boot: the marker is still present on the next start → revert (§5.8) |
| DB migration fails or the binary is downgraded | Rare | Forward-only migrations, each in a transaction | `user_version` newer than the binary → refuse to start | Backup `*.vN.bak` taken before migrating (built) |
| Config file corrupted or half-written | Rare | Atomic write-then-rename (built) | Parse error at start | Keep `config.yaml.bak` from the last good load (M1) |
| **Crash or power loss mid-download** | Common | Downloads write to `.part`, final rename only when complete | Startup reconcile | Torrents: `Recheck` unfinished ones (spike finding). HTTP: resume from the segment map in SQLite, verified against Content-Length/ETag |
| Signed URL expires mid-download (IDM's "refresh address") | Common for CDNs | Per-job | 403/410 or ETag change | Re-resolve the source (video: re-probe via yt-dlp; web: re-run redirects), then continue at the same offset when the size and ETag match |
| Disk full | Common on small VPSs | Free-space check before start and while running | `statfs` poll | Pause all jobs with the reason "disk full"; resume when space is back |
| Google OAuth token revoked or expired | Occasional | Destination provider only | rclone auth error | Provider status → `needs-signin`; the app shows a re-sign-in button; queued uploads wait instead of failing |
| Google Drive quota (750 GB/day) | Heavy users | Per destination | rclone quota error | Back off until the reset time and keep the queue |
| Helper tool missing | Install-time | Feature off, not a crash (built: provider status) | `doctor` | Managed tools: download them; others: a hint |
| **Android app and server versions drift** | Every release | `/v1` is additive-only; the OpenAPI spec is generated from code | **CI diffs `openapi.json` against the committed snapshot and fails on breaking changes** | Breaking changes need `/v2`, with both served for a deprecation window |
| Provider settings drift from what the app shows | Every release | The server sends JSON Schema; the app only renders it | — | Nothing to do: the app never hard-codes provider forms |
| TLS certificate renewal fails | Rare | CertMagic renews early and retries | `doctor` reports expiry | CertMagic's built-in backoff; alert in the app at under 14 days |
| SSRF / hostile input | Constant | `netguard` on every dial, also for yt-dlp (proxy) and torrent HTTP | Tests | — |
| Phone (Termux) kills the process | Common | Crash-only design | — | Restart loop script plus reconcile on start |

## Self-test: one command, used everywhere

`dmn doctor --selftest` (M1) runs a full loop **against local fixtures only**: an in-process HTTP server and a loopback torrent seeder, with nothing from the internet (AGENTS.md rule 7). It downloads, streams a Range, and checks hashes. The same check is the post-update health check, the Docker `HEALTHCHECK`, and a CI step. One test proves the install works on every platform.

## Things deliberately *not* built

- No custom update system, plugin system or scraper. yt-dlp is the only site-specific code, and it's someone else's full-time job.
- No message queue or extra daemons: one process, SQLite, goroutines. Fewer moving parts means fewer things to break.
- No ORM: plain SQL with forward-only migrations.
