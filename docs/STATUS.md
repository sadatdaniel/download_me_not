# Status

**Current state:** Step 0 (repo bootstrap) done. No Go code yet.
**Next action:** Step 1, torrent engine spike. Add `compose.dev.yaml` (Go 1.26 service plus an isolated `swarm` profile: tracker and seeder on an `internal: true` network, self-generated content only, see AGENTS.md rule 7). Create `spike/engine/` with a minimal Engine interface over anacrolix/torrent, then check every §5.9 item and stream seeking with `CGO_ENABLED=0`. Write the results to `docs/engine-spike.md`.
**Blockers:** none.

## Checklist

### Step 0: Bootstrap
- [x] Private GitHub repo, AGENTS.md, PROMPT.md, STATUS.md, spec copied to docs/scope.md
- [x] Verified answers recorded in scope §19

### Step 1: Torrent engine spike (time-box 2–3 sessions)
- [ ] `compose.dev.yaml`: Go dev container plus an isolated local swarm (no internet, self-made content and torrents, incl. v2/hybrid)
- [ ] Engine interface draft plus an anacrolix implementation in `spike/engine/`
- [ ] Every §5.9 item marked pass / our-layer / gap, with evidence
- [ ] Stream mode: Range seek moves the read priority; first and last pieces come first
- [ ] Builds and runs with `CGO_ENABLED=0` (amd64 and arm64); uTP status without cgo
- [ ] `docs/engine-spike.md` written; §14 decision gate applied; §17 updated

### Step 2: Scaffold
- [ ] Go module, `cmd/dmn` with cobra, §5.5 commands stubbed with `--json`
- [ ] Config (koanf): flags > env > file > defaults; `config get/set/show --effective`
- [ ] SQLite (modernc) plus an embedded migration runner (`user_version`)
- [ ] API (huma, `/v1`), token auth with read/write/admin scopes, SSE progress
- [ ] Folder API on `os.Root`, Range streaming, HMAC-signed URLs, path-escape tests
- [ ] Provider interfaces plus a registry with JSON Schema settings
- [ ] Engine interface wired to the spike's choice
- [ ] netguard: SSRF dialer plus a CONNECT proxy for yt-dlp
- [ ] CI: vet, staticcheck, `test -race`, static cross-build, gitleaks
- [ ] GoReleaser, Dockerfile, compose.yaml

### Milestones (each gets its own plan)
- [ ] M1 core: IDM-grade HTTP downloader (dynamic segmentation, resume, retry, header passthrough, aria2c benchmark), queue, stream mode, folder API
- [ ] M2 torrent management (§5.9, basic level)
- [ ] M3 integrations: yt-dlp, rclone and Drive (device flow, `drive.file`), CertMagic, install.sh, self-update, `pair`
- [ ] M4 Android app (Kotlin + Compose + DocumentsProvider proposed)
- [ ] M5 browser extension (proposed): send downloads with cookies and headers, media sniffer

## Session log
- 2026-10-10: Plan approved. Bootstrap: repo, agent docs, spec plus verification notes.
