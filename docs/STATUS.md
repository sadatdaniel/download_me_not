# Status

**Current state:** Step 2 (scaffold) done, apart from the first CI run on GitHub. `dmn` builds static; setup → serve → ls/mkdir/mv/rm/watch/providers/doctor/config work end to end. All tests pass offline. The torrent engine exists (internal/engine) but is not started by `serve` yet (job layer is M1).
**Next action:** 1) Check the first GitHub Actions run (`gh run list`) and fix anything red. 2) Write the M1 plan: job manager (crash-only + reconcile loop, docs/architecture.md), `add`, the IDM-grade HTTP downloader (dynamic segmentation, resume, retry, header passthrough), stream mode, ZIP folder download and copy, `doctor --selftest`.
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
- [ ] M2 torrent management (§5.9 "ours" items from engine-spike.md)
- [ ] M3 integrations: yt-dlp, rclone and Drive (device flow, `drive.file`), CertMagic, install.sh, self-update, `pair`
- [ ] M4 Android app (Kotlin + Compose + DocumentsProvider proposed)
- [ ] M5 browser extension (proposed): send downloads with cookies and headers, media sniffer

## Session log
- 2026-10-10: Plan approved. Bootstrap: repo, agent docs, spec plus verification notes.
- 2026-10-10: Legal-testing rule added (AGENTS.md rule 7). Engine spike: 20 offline checks, anacrolix kept; gaps: v2-only torrents, LSD.
- 2026-10-10: Step 2 scaffold: all internal packages, CLI, API, CI, GoReleaser, Docker. Engine fixes: IPv4/IPv6 pinning on bind, Done() signal. libtorrent rejected (cgo).
