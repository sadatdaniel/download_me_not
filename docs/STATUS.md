# Status

**Current state (2026-10-13):** Latest release **v0.1.9** (signed): Stremio add-ons (experimental, off by default; defaults Cinemeta, OpenSubtitles v3, WatchHub; everything goes through the server, posters/subtitles via `/v1/addons/proxy`), auto-retry of failed downloads, `.part` removal and `/v1/cleanup`, sequential stream mode, port check (`/v1/ports`), media list (`/v1/media`). The owner runs it on an **Oracle Cloud ARM VM** (self-updates daily; reached through an SSH tunnel on 127.0.0.1:8080; a `claude-test` write token exists there for tests; the owner is fine with wiping it and reinstalling for multi-user). The **Flutter app** is in the private repo sadatdaniel/download_me_not_app (sibling folder `download_me_not_app`, built on the Windows host; read its README): Downloads, Media, Files, Discover (when add-ons are on), Settings; player with dual subtitles, stats, playlist, shortcuts. Tested on Flutter web; **never run on an Android device yet**.

**Multi-user server side is built (on main, not released).** Done and tested: SQLite migration `0005_users.sql` (users, device_users, jobs.user_id), `internal/store/users.go` (CreateUser, CheckPIN with lockout, SetPIN, DeleteUser, BindDevice/DeviceUser, ListJobsOf, Prefs), engine `AddIn(torrent, dir)` (per-folder storage, `Drop` deletes there), job manager puts each user's files under `users/<id>/` (`AddRequest.User`, `CopyRequest.User`, `jobs.UserDir`, `ErrBusyElsewhere` for the same torrent in two accounts), `fsapi.NewScoped` (signed links carry the user's scope and `u=`). **API (2026-10-13):** `POST /v1/login` (name + PIN + device → device token; owner gets admin, others write; same error for wrong name and PIN; per-address and global limiter), `GET /v1/users` (names, no token), `GET /v1/me`, `POST /v1/users`, `PUT /v1/users/{id}/pin` (own with the current PIN, or the owner), `DELETE /v1/users/{id}?deleteFiles`, `GET/DELETE /v1/devices[/{name}]`, `GET /v1/prefs` + `PUT/DELETE /v1/prefs/{key}`; every job, file, media, archive, cleanup, zip, raw, stream, events and add-on play/subtitle route uses the caller's user and folder; a token without a user reaches only admin server-wide routes once users exist. CLI `dmn user add|list|pin|remove` (on the server) and `dmn login`. The Preact web panel signs in with name + PIN when users exist (verified in the preview, config `webui-users`). install.sh asks for the first user (`--user`, `--pin`; questions via /dev/tty). Test `TestUsersAreSeparate` (jobs, files, signed links, progress, prefs, PINs, devices, removal) and the installer test pass. Decisions in scope §17 and failure modes in architecture.md.

**Next action, in order:**
1. **Multi-user leftovers on the server:** add-ons per user (the list is still one per server, owner-managed; move `addons.urls`/`disabled` into each user's prefs, keep the on/off switch global); `dmn pair` QR still makes a token without a user (the app then needs name + PIN anyway).
2. **Multi-user in the app: done (2026-10-13)** except: favorites (the app has none yet), and a check on a real Android device. Sign-in = address (or QR/link) → name chips + PIN, one device name per install; Settings → Your account (PIN, devices, sign out) and Users (owner); add-on/Drive management owner-only; Continue watching syncs through `/v1/prefs/w_*`. Verified in the Flutter web preview (config `app-web-users`): wrong PIN, owner sign-in, add user, sign out, Guest sees no owner controls.
3. v0.1.10 is out, but its install.sh aborts without a terminal (fixed on main): release v0.1.11 after the owner OKs it, then re-run test/sim (amd64 + arm64); owner reinstalls on Oracle, run test/sim (it installs real releases, so only after the release; it will need `--user`/`--pin` or a prompt answer), re-test streaming (Sintel exception, scope §17).
4. Then: subtitle search in the player through the OpenSubtitles add-on (pick, save next to the video, delete); WireGuard profiles for downloads (userspace wireguard-go + netstack, paste a .conf; the UI must say incoming torrent connections through a provider's tunnel usually need port forwarding from the VPN provider).
5. Later: install.sh opens the host firewall (`--no-firewall`); `dmn doctor` certificate expiry; M2.7 multi-container swarm (re-check the -race stream stall); `/ponytail-audit`; web panel = Flutter web served by the server (sign in once, browser stays signed in), Preact at `/lite`.

**Open (known gaps):** app never run on a real Android device (player, proxy, download-to-phone, port test are Android-only paths), stream-window test skipped under -race (anacrolix interested/choked race, see internal/engine/stream_test.go), real Google OAuth client ID (owner creates it), GHCR image visibility (owner sets it public), FTP (deferred), per-torrent speed limits, licence choice, Cinemeta's own search is weak upstream.
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
- 2026-10-11: M4 resumed: app scaffold in the private repo download_me_not_app (analyze clean, unit tests + live pinned-TLS test against a self-signed dev server pass).
- 2026-10-11: server: archive browsing (/v1/archive, /v1/archive/raw), opt-in server.cors, `config set` takes JSON lists. App: file manager, responsive layout, queue drawer, web build verified in the browser pane.
- 2026-10-12: v0.1.7 released (archives, CORS). Server: GET /v1/media. App: player (media_kit, proxy, tracks, dual subtitles, resume), Media + Settings screens, grid file manager with unfinished/cloud badges, website-style header, Tailwind-like light theme, bulk .torrent drop with options dialog, wasm build + deferred player. Owner to try the Flutter web build before deciding on web-panel parity (Flutter web at / + Preact at /lite is the recommendation; bundling decided later).
- 2026-10-12: real streaming test on the owner's Oracle server with the CC "Sintel" torrent (owner-approved exception to rule 7). Finished file over the SSH tunnel: first frame ~1.1 s, seeks ~1.3 s, 9.4 MB/s. While downloading at a 450 KB/s cap (3x bitrate): first frame 10.8 s, seek into a missing part 26 s, ~5 s stalls per minute, because stream mode was rarest-first except the ends. Fixed: sequential stream window (engine StreamWindow, priorities set before data flows) and /v1/jobs/{id}/stream now serves the largest file of a finished folder download (was 400). Owner's limit restored to unlimited. Re-measure after the release.
- 2026-10-12: switching a torrent between stream and standard mode now releases parked pieces (StreamOff); file skip really skips (pieces only in that file are set too; Start had raised them all); GET /v1/ports reports the torrent port and whether peers connect in (verdict open/probably-closed/unknown/not-running).
- 2026-10-12: app UI pass: desktop-style context menus (right click hijacked on web), no bottom sheets, GNOME-style settings with a Network group (port verdict + test from the phone), player playlist through the folder with autoplay-next and keyboard shortcuts, Continue floating button. Proposed next (owner to confirm): clean partial data on cancel + orphan .part cleanup + auto-retry, subtitle search via OpenSubtitles (needs an API key) as a server provider. Release v0.1.8 pending the owner's OK.
- 2026-10-12: v0.1.8 released. Server: auto-retry, .part cleanup on remove, /v1/cleanup. App: Media second, centered add-server card, two independently styled subtitles, stats for nerds, viewer for text/PDF/images, GNOME-style folders, narrow-screen settings. Stremio add-on support approved (Discover tab behind an experimental switch).
- 2026-10-12: Stremio add-ons built (server /v1/addons*, app Discover tab + Settings). Defaults on first switch-on: Cinemeta, OpenSubtitles v3, WatchHub. Everything goes through the server (sources become server downloads, posters/subtitles via /v1/addons/proxy). Verified in the web preview: defaults install, posters load, WatchHub lists Big Buck Bunny on a legal service, OpenSubtitles finds subtitles. Cinemeta search is weak upstream (loose matches; %20 encoding gets 504 from them). Not released yet (v0.1.9 needs the owner's OK). Owner asked to test with piracy-index add-ons (TPB, YTS) for a Blender film: declined to source those myself; the owner can add any add-on URL; a source is only played if its info hash matches the known Creative Commons release.
- 2026-10-12: v0.1.9 released. Multi-user started (owner: strictly separate users, name + 4-digit PIN, sign-in with name + PIN returns a per-device token, no permissions, no strict mode, no migration of old data): store, engine per-folder storage, per-user job paths and scoped signed links done and tested; API, CLI, installer and app next (see Next action). End of the second conversation.
- 2026-10-13: multi-user server API, CLI and installer: login with name + PIN → device token, per-user scoping of every job/file/link/progress route, user and device management, per-user prefs; two-user isolation test and installer test pass. Not released.
- 2026-10-13: multi-user in the app (sign-in, account and users settings, watch progress on the server) and in the Preact panel; both repos pushed, not released.
- 2026-10-13: v0.1.10 released (release, update-e2e, image green). test/sim caught an install.sh bug: without a terminal and without --yes the /dev/tty check ended the script (failed redirection on a special builtin exits POSIX sh). Fixed on main with an installer test; needs v0.1.11 (owner approval). test/sim gained a step: sudo dmn user add, sign-in from outside, the installer token loses data access.
- 2026-10-13: owner reinstalled on Oracle and signed in from the real Android phone (first real-device run: works). Installer prints connection steps (`dmn connect`: address, fingerprint, ports, users) instead of the terminal QR; README + deploy.md walk through install, first user, Oracle ingress rules, sign-in, users and PINs. App: Discover hint fixed, spaces in the address ignored, firewall hint on timeouts. Open: an add-on install answered 403 from the Oracle server (upstream refuses; diagnosing with the owner).
- 2026-10-13: Torrentio answers 403 to the Oracle VM for every User-Agent (Cloudflare data-centre block). Built "Ask from this phone" for such add-ons (server stores, never contacts; the app fetches only JSON lookups and sends the chosen source to the server; no torrent traffic from the phone; magnet external links and copies blocked). App layout fixes for phones (stacked field and button, bottom bar), Suggested add-ons show "added", decoder "Copy" renamed "Compatible". Add-on requests identify as download_me_not. Needs v0.1.11 (owner OK) and a new APK.
- 2026-10-13: v0.1.11 released (release, image, update-e2e green); APK built. YouTube asks the Oracle VM to sign in (data-centre block): cookies are now per user (server store, jobs, API) and the app has Settings, Site sign-ins. Not released yet.
