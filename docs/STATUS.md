# Status

**Current state:** M1 core done: web and torrent jobs, pause, resume, stream, cancel, crash recovery, ZIP, copy, selftest; downloader benchmarked against aria2c. CI green.
**Next action:** M2.6 kill switch, SOCKS5, UPnP opt-in, stream first/last pieces, blocklist URL refresh (see the checklist). Engine facts for M2: no runtime blocklist setter (use our own Ranger), no SOCKS5 in the library (AddDialer + DialForPeerConns=false), limiters can change at runtime.
**Blockers:** none.

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
  - [ ] M2.6 kill switch (pause when the bound interface goes down), SOCKS5 dialer (ours: AddDialer plus x/net/proxy), UPnP opt-in, stream mode first/last pieces, blocklist from URL on a schedule
  - [ ] M2.7 isolated multi-container swarm with netem; private DHT; IPv6
- [x] M2 "swarm memory" (user idea, 2026-10-10): learn trackers ranked by announce success plus a persisted DHT node cache in a new SQLite table; auto-add learned trackers to new **public** torrents only (never when the private flag is set). Check first how to get per-tracker results, since anacrolix has no status API
- [ ] M3 integrations: yt-dlp, rclone and Drive (device flow, `drive.file`), CertMagic, install.sh, self-update, `pair`
- [ ] M4 Android app (Kotlin + Compose + DocumentsProvider proposed)
- [ ] M5 browser extension (proposed): send downloads with cookies and headers, media sniffer

## Session log
- 2026-10-10: Plan approved. Bootstrap: repo, agent docs, spec plus verification notes.
- 2026-10-10: Legal-testing rule added (AGENTS.md rule 7). Engine spike: 20 offline checks, anacrolix kept; gaps: v2-only torrents, LSD.
- 2026-10-10: Step 2 scaffold: all internal packages, CLI, API, CI, GoReleaser, Docker. Engine fixes: IPv4/IPv6 pinning on bind, Done() signal. libtorrent rejected (cgo).
