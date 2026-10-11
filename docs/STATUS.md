# Status

**Current state (2026-10-11, second conversation):** Devices can now be paired from the web panel (Settings, "Pair a phone"; admin `POST /v1/pair`), unreleased until the next tag. Server side is feature-complete for the MVP except the items under "Open" below. Repo is **public** (history scrubbed: noreply email, no location; no licence file yet). Latest release **v0.1.6** (signed; the release workflow also proves self-update from the previous release and publishes the multi-arch Docker image to GHCR). The owner has **installed it on an Oracle Cloud ARM VM** with the one-liner and reached it through an SSH tunnel; ffmpeg was missing there, fixed in v0.1.5 (install.sh now installs it). `test/sim/run.sh` (Oracle-like VM, native + arm64) passes against v0.1.5. M4 (Flutter app) is **paused by the owner** until the server is confirmed in real use.

**Next action, in order:**
1. Help the owner finish the Oracle setup without SSH tunnels: open TCP 8080 in the VCN security list + iptables (docs/deploy.md), or a domain with ACME, or Tailscale (`--access proxy`). Confirm downloads, streaming, video and torrents work for real.
2. `dmn doctor`: report open/blocked ports and certificate expiry (scope §5.10).
3. M2.7 multi-container torrent swarm with netem (isolated network); a full code audit (`/ponytail-audit`) and fixes.
4. Then, when the owner says so: M4 Flutter app (plan in the checklist below).

**Open (known gaps):** real Google OAuth client ID (owner creates it; Drive tested only against a fake Google), GHCR image visibility (owner sets it public in the GitHub UI), FTP (deferred), per-torrent speed limits, licence choice.
**Blockers:** none. (2026-10-11: the host disk filled up once and Docker went read-only; the owner freed space and compacted docker_data.vhdx. Keep builds lean; run tests with `go test -p 2` if Docker Desktop struggles.)

## Checklist

### Step 0: Bootstrap
- [x] Private GitHub repo, AGENTS.md, PROMPT.md, STATUS.md, spec copied to docs/scope.md
- [x] Verified answers recorded in scope §19

### Step 1: Torrent engine spike
- [x] `compose.dev.yaml`: Go dev container; offline `test` service (`network_mode: none`) instead of a Docker swarm, using in-process peers on loopback
- [x] Engine interface draft plus checks in `spike/engine/`
- [x] Every §5.9 item marked pass / API / ours / gap
- [x] Stream mode: Range seek moves priority; first and last pieces first
- [x] `CGO_ENABLED=0` builds (amd64 run; arm64 and armv7 cross-compiled); pure-Go uTP works
- [x] `docs/engine-spike.md` written; §14 gate: keep anacrolix; §17 updated
- [ ] Later (M2): isolated multi-container swarm with netem, private DHT, SOCKS5, IPv6, run on real arm64

### Step 2: Scaffold
- [x] Go module, `cmd/dmn` with cobra, §5.5 commands stubbed with `--json`
- [x] Config (koanf): flags > env > file > defaults; `config get/set/show --effective`
- [x] SQLite (modernc) plus an embedded migration runner (`user_version`)
- [x] API (huma, `/v1`), token auth with read/write/admin scopes, SSE progress
- [x] Folder API on `os.Root`, Range streaming, HMAC-signed URLs, path-escape tests
- [x] Provider interfaces plus a registry with JSON Schema settings
- [x] Engine interface (from spike) plus the anacrolix implementation: GotInfo before DownloadAll, VerifyData on startup, recover around add, reject v2-only
- [x] netguard: SSRF dialer (also for anacrolix HTTP/tracker dialers) plus a CONNECT proxy for yt-dlp
- [x] CI: vet, staticcheck, `test -race`, static cross-build, gitleaks
- [x] GoReleaser, Dockerfile, compose.yaml (Docker image without helper tools yet: M3)
- [x] OpenAPI snapshot `api/openapi.json` as the app contract
- [x] docs/architecture.md: breakage map and self-healing rules

### Milestones (each gets its own plan)
- [ ] M1 core: IDM-grade HTTP downloader (dynamic segmentation, resume, retry, header passthrough, aria2c benchmark), queue, stream mode, folder API
  - [x] internal/httpdl: probe (size, ranges, ETag, filename), dynamic segmentation (work stealing, split the largest remainder), resume from a saved segment map, retry with backoff, header passthrough, single-connection stream mode, .part then rename without overwrite
  - [x] internal/jobs: crash-only manager; DB keeps desired state (`want`), a reconcile loop starts and stops runners (recover per job), max active, progress snapshots to SSE; torrent jobs start the engine lazily
  - [x] API + CLI: add (URL, magnet, .torrent), get, pause, resume, mode, cancel; /v1/jobs/{id}/stream for files still downloading (blocks until bytes arrive)
  - [x] fsapi: ZIP folder download, copy (API /v1/zip, /v1/fs/copy; CLI zip, cp)
  - [x] `doctor --selftest`: local HTTP plus loopback torrent, offline
  - [x] Benchmark against aria2c (docs/benchmark.md): tie on a clean network, ~30% faster with slow routes
  - [x] Expired links: every request goes to the original URL, so expiring redirect targets (signed CDN links) refresh themselves. Re-resolving pages comes with the video provider (M3)
  - [ ] FTP: deferred (rare; passive-mode data connections are an extra SSRF surface). Build when needed with jlaffaye/ftp and a guarded dial function for both connections
  - [ ] Global speed limit: moves to M2 together with `limits`
- [ ] M2 torrent management (§5.9 "ours" items from engine-spike.md)
  - [x] M2.1 settings: engine options from the torrent provider schema (encryption, uTP, IPv6, max peers, anonymous mode, bind address); runtime global limits (rate.Limiter.SetLimit) with API and CLI `limits`; swappable blocklist (our iplist.Ranger with an atomic swap, so refresh needs no restart); fields that need a restart are flagged in the schema
  - [x] M2.2 views: peers (address, client, progress, rates, encrypted), piece map, availability, trackers (list, add, remove), a plain-language "why slow/stalled" hint; API plus CLI peers, pieces, trackers, recheck
  - [x] M2.3 seeding policy (stop, ratio, time, forever), disk-space guard (check before start, poll, pause with reason), queue priority and reordering
  - [x] M2.4 post-completion: archive extraction (mholt/archives on os.Root, size and ratio limits, skip encrypted), optional delete of the archive
  - [x] M2.5 swarm memory (below)
  - [x] M2.6 kill switch (pause when the bound interface goes down), SOCKS5 dialer (ours: AddDialer plus x/net/proxy), UPnP opt-in, stream mode first/last pieces, blocklist from URL on a schedule
  - [ ] M2.7 deferred: multi-container swarm with netem and IPv6 as a nightly CI job. Loopback, private-DHT and SOCKS tests already cover the logic
- [x] M2 "swarm memory" (user idea, 2026-10-10): learn trackers ranked by announce success plus a persisted DHT node cache in a new SQLite table; auto-add learned trackers to new **public** torrents only (never when the private flag is set). Check first how to get per-tracker results, since anacrolix has no status API
- [ ] M3 integrations: yt-dlp, rclone and Drive (device flow, `drive.file`), CertMagic, install.sh, self-update, `pair`
  - [x] M3.1 video provider (managed install and self-update of yt-dlp moved to M3.5): probe (`-J`) to formats and presets, download as a job with progress, all traffic through the netguard proxy, `formats` CLI. Tests: yt-dlp's generic extractor on a local page with our own ffmpeg-made video. **Never YouTube from the dev machine**
  - [x] M3.2 cookies: import a Netscape cookie file per domain, AES-GCM at rest (key file 0600 in the config dir), passed to yt-dlp and httpdl, never logged or returned
  - [x] M3.3 Google Drive: device-flow Connect (API /v1/providers/gdrive/signin, CLI providers config gdrive --signin), encrypted settings (secret.key), copy jobs /v1/copy and CLI copy, post-completion Drive copy with optional delete-local. Tested with a fake Google server and rclone's local backend. **Still to verify with a real Google client ID** (owner creates it later)
  - [x] M3.4 HTTPS: server.tls auto|off|self-signed, fingerprint pinning (app and CLI --fingerprint), live token reload, `pair` QR (URL, new device token, fingerprint)
  - [x] M3.4b ACME: server.tls acme with CertMagic (HTTP-01 on server.httplisten, TLS-ALPN, DNS-01 via cloudflare/duckdns), plaintext DNS token sealed on first start. Pebble issuance test passes (test/acme/run.sh, isolated network, strict mode). Still to do: doctor port and expiry checks
  - [x] M3.5a managed tools: verified install, update, rollback (API /v1/tools/{name}/install|rollback, CLI tools), daily maintenance installs what enabled providers need
  - [x] M3.5b server self-update: go-selfupdate with an ECDSA-signed checksums.txt (key: CI secret RELEASE_SIGNING_KEY; public key internal/update/release-key.pub; owner keeps an offline backup), confirm-after-boot rollback, daily check (updates.server notify|auto|off), API /v1/update, CLI update check|apply|rollback
  - [x] M3.5c install.sh (signature + checksum verified, service user owns the binary for self-update, hardened systemd unit, config + owner token + pairing QR, re-run = upgrade/repair, --uninstall/--purge never touches downloads), `dmn uninstall` (typed confirmation to delete downloads), install.sh shipped as a release asset, CI installer test in a container. Docker image publishing still to do
  - [x] M3.6 web UI: Downloads (live progress via SSE, pause/resume/mode/remove, player, peers and pieces), Add (probe, video formats, .torrent upload, unpack/Drive options), Files (browse, play, download, ZIP, rename, delete, to Drive), Settings (limits, providers with schema forms, Connect Google Drive, cookies, tools, updates). Verified in the browser pane incl. phone width
- [ ] M4 Android app (**paused by the owner until the server is confirmed working and deployable**): **Flutter** (owner's choice), in `app/`, built with the Flutter SDK on Windows (C:devlutter, 3.47.5; Android SDK installed)
  - [ ] M4.1 project, API client on dart:io HttpClient with certificate pinning (fingerprint from pairing), pairing (scan the dmn://pair QR or type URL + token), token in secure storage
  - [ ] M4.2 queue with live progress (SSE), pause/resume/remove, add (URL, magnet, .torrent, video formats, stream mode)
  - [ ] M4.3 share target (links from any app), magnet: and .torrent handlers
  - [ ] M4.4 file manager (browse, rename, move, delete, copy to Drive, storage) and player. The player streams through a loopback proxy inside the app, because ExoPlayer can't use our pinned TLS
  - [ ] M4.5 server version and update notice; completion notifications (in-app first; push via UnifiedPush later)
  - [ ] Later: DocumentsProvider (native Kotlin) so the server folder shows in the system Files app
- [ ] M5 browser extension (proposed): send downloads with cookies and headers, media sniffer

## Session log
- 2026-10-10: Plan approved. Bootstrap: repo, agent docs, spec plus verification notes.
- 2026-10-10: Legal-testing rule added (AGENTS.md rule 7). Engine spike: 20 offline checks, anacrolix kept; gaps: v2-only torrents, LSD.
- 2026-10-10: Step 2 scaffold: all internal packages, CLI, API, CI, GoReleaser, Docker. Engine fixes: IPv4/IPv6 pinning on bind, Done() signal. libtorrent rejected (cgo).
- 2026-10-11: M3.3-M3.6 built (Drive, HTTPS/ACME, tools, self-update, installer, web UI). Disk-full incident; resumed after cleanup. Releasing v0.1.0.
- 2026-10-11: v0.1.1-v0.1.3 released (signed; update-e2e and Docker image on GHCR in the release workflow). README + docs/deploy.md. Pebble ACME test passes. Owner is trying a first real install on an Oracle Cloud ARM (arm64) VM with install.sh --from; Oracle needs both the VCN security list and the instance iptables opened (see docs/deploy.md). M4 paused.
- 2026-10-11: repo made public (history scrubbed). test/sim realistic install (Oracle-like VM, native + arm64) passes against v0.1.4 after three fixes.
- 2026-10-11: owner's first Oracle install showed ffmpeg missing; install.sh now installs it (v0.1.5), simulation checks it and passes.
- 2026-10-11: end of the first conversation. Handoff: STATUS next actions (web-panel pairing first), PROMPT.md refreshed.
- 2026-10-11: web-panel pairing: POST /v1/pair (admin) + Settings card with a server-rendered QR (rsc.io/qr); CLI setup/pair share api.AddToken. Released v0.1.6 (release workflow green).
- 2026-10-11: fixed the flaky jobs test (test server 4x slower so pause/resume happen mid-download); CI green again.
