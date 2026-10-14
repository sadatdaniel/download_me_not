# Project Scope: Self-Hosted Downloader

Working title. Status: draft, v0.8 (2026-10-10).

Changes in v0.8: build order for the handoff to Claude Code (section 18): engine spike first, project scaffold second. Changes in v0.7: providers (5.12). Sources (web downloads, torrents, video sites) and destinations (local folder, Google Drive, other rclone drives) are built-in providers that can be switched on and off from the settings, each with a server-generated settings form so the apps need no update when one is added; `providers` CLI commands; third-party plugins are not in the MVP. Earlier changes in v0.6: the torrent management features formerly in Phase 2 (peer, piece and availability view; IP blocklists and anonymous mode; web seeds; archive extraction; BitTorrent v2 and hybrid support) plus the other essential torrent features are now in the MVP (5.9); HTTPS access modes, QR pairing, and port guidance (5.10); one-command install, single config file, and clean uninstall (5.11); built-in WireGuard is out of scope for now; simplicity and hands-off maintenance added as a design principle; torrent engine chosen by a spike with libtorrent as the fallback. Earlier changes in v0.5: updates for the server and the helper tools, built on existing mechanisms (yt-dlp and rclone self-update, a Go self-update library with checksum and signature checks, image pulls for Docker, a supervisor for restarts) with only a small "confirm after boot" rollback piece custom; yt-dlp is checked daily. Earlier changes in v0.4: Docker and the native binary are equal first-class install paths; the program manages its own helper tools (yt-dlp, rclone) and gains `doctor` and `update` commands; Windows and macOS are explicitly unsupported; the Termux note for YouTube was dropped. Earlier changes in v0.3: YouTube and other video sites via yt-dlp with format and quality choice (MVP); two download modes (standard and stream); folder API designed for a file-manager style Android app; licensing and dependency rules; open-core recommendation; Stremio-style source plugin added as a later, optional phase.

## 1. What this is

A self-hosted download server that you run on your own VPS, home server, or (best effort) Android device, paired with an Android app and a CLI. You send it a link (web URL, YouTube or other video page, magnet, or .torrent), it downloads on the server, and you can browse the download folder like a file manager, watch videos from the server, or copy files to Google Drive.

It is a self-hostable alternative to Seedr.cc, with IDM-style web downloading and video-site support added.

## 2. Goals

1. One place to add any download: direct web link, video page (YouTube and others), magnet link, or .torrent file.
2. Pick format and quality for video pages.
3. Downloads run on the server, not on the phone.
4. Two download modes: standard, and stream mode (watch while it downloads).
5. Stream already-downloaded videos directly from the server.
6. Browse and manage the whole download folder from the Android app as a file manager, through the API.
7. Copy finished files to Google Drive.
8. Send links to the server from anywhere on Android in two taps (share sheet).
9. Control everything from a command line as well as the app and web UI.
10. Run almost anywhere: any Linux distro through Docker, plus a native binary for bare Linux, Raspberry Pi, and Termux.
11. Easy to self-host: a single Docker Compose setup, or a single binary.
12. Install, configure, update, and uninstall with one command each, with HTTPS handled without manual steps.
13. Full torrent management in the first version: seeding policy, limits, peer and piece visibility, blocklists, web seeds, and archive extraction.

## 3. Users

- **Primary user:** one household running their own server: several users, each strictly separate (changed 2026-10-12, see §17; was single-user).
- **Not targeting:** teams, public services, or hosted multi-tenant use.

Single-user removes quotas, per-user isolation, sign-up flows, and billing from the server.

## 4. Design principles

1. **API first.** The server exposes one HTTP API. The CLI, web UI, and Android app are all clients of it. Nothing is possible in the UI that the API cannot do.
2. **The filesystem is the source of truth.** The database tracks download jobs only. Files in the download folder are listed from disk, so the folder can already contain files the server did not create (for example an existing media library).
3. **Few moving parts.** Prefer one process over several daemons. Optional tools (ffmpeg, yt-dlp, rclone, a JavaScript runtime) are detected at startup, and features that need a missing tool switch off cleanly.
4. **Portable by default.** No dependency on systemd, a specific distro, or root.
5. **Safe by default.** Closed to the network until a token is set; SSRF protection on every user-supplied URL; strict path safety on every file operation.
6. **Reuse before building,** within the licensing rules in section 11.
7. **Simplicity over complication.** Prefer proven, pure-Go or standard components, and keep maintenance hands-off: automated dependency updates, CI with integration tests, automatic releases, and self-updating tools.

## 5. In scope

### 5.1 Server (MVP)

| Area | What it does |
|---|---|
| Add downloads | Send a URL, video page, magnet link, or .torrent file through one input |
| Web downloads | Multi-connection, resumable HTTP/HTTPS/FTP downloads |
| Video pages | YouTube and other sites through yt-dlp, with format and quality choice (5.2) |
| Providers | Sources and destinations are providers with on/off toggles and generated settings forms (5.12) |
| Torrents | Full torrent management: file selection, pause and resume, seeding policy, limits, peer and piece view, blocklists, web seeds, archive extraction (5.9) |
| Download modes | Standard and stream mode (5.3) |
| Queue | List of active and finished downloads with progress, speed, and ETA |
| Streaming | Serve files with HTTP Range requests so players can seek, including files still downloading in stream mode |
| Folder API | Browse and manage the download folder (5.4) |
| Destinations | Copy finished files or folders to Google Drive first (rclone), and later other drives, as switchable providers (5.12) |
| API | Documented HTTP API with live progress updates |
| Auth | A single access token or password, with scopes |
| Remote access | Works behind a tunnel or VPN (for example Cloudflare Tunnel or Tailscale) |
| Safety | SSRF protection on every user-supplied URL |
| Deployment | Docker Compose, plus a native single-binary install |

### 5.2 Video pages and formats

Two-step flow:

1. **Probe:** the server asks yt-dlp for the video's title, thumbnail, and the list of available formats.
2. **Choose:** the app and CLI show presets (best, 1080p, 720p, audio only as m4a, opus, or mp3) and an option to pick a specific format, then the job starts.

Requirements and realities:

- ffmpeg is required to merge separate video and audio streams.
- YouTube support now needs an external JavaScript runtime (Deno recommended; Node, Bun, and QuickJS are also supported) plus the yt-dlp-ejs component. Without a runtime, formats may be limited.
- YouTube changes often, so yt-dlp must update itself. The server checks for updates, and the Docker image refreshes yt-dlp on start.
- Login-only or bot-checked videos need cookie support, so basic cookie import is in the MVP (5.6).
- Playlists, subtitles, and chapter or sponsor handling are Phase 2.
- Streaming a YouTube video while it downloads works only for formats that combine audio and video; merged high-quality formats are download-first.

### 5.3 Download modes

| Mode | Behaviour | Best for |
|---|---|---|
| **Standard** | Default piece or segment order, fastest overall completion, watch after it finishes | Downloading to keep |
| **Stream** | Sequential download with the first and last pieces first, and priority that follows the player's position when it seeks | Watching while downloading |

- Mode is chosen per download and can be switched while it runs.
- First and last pieces first matters because some containers (for example MP4) keep their index at the end of the file.
- When a player seeks past the downloaded part, the Range request moves piece priority to that position and the player waits for the data.
- For HTTP downloads, stream mode means one sequential connection that serves the file while it grows; standard mode uses multiple connections.
- For torrents with several files, the user picks which file to stream.
- Already-downloaded videos play directly through the same Range endpoint, with no mode needed.

### 5.4 Folder API (file-manager access)

The Android app presents the server's download folder as a file manager, so the folder API is a core feature, not an extra.

| Capability | Detail |
|---|---|
| Browse | List a directory with names, sizes, types, modified times; stat a single entry |
| Read | Download any file with Range support; stream video and audio |
| Folders | Download a folder as a ZIP; create folders |
| Manage | Rename, move, copy, delete (write scope only) |
| Storage | Disk usage and free space |
| Links | Short-lived signed URLs for streaming, so a player never holds the main token |
| WebDAV | Optional endpoint so file managers and players can mount the folder (Phase 2) |
| Extras | Thumbnails, search, subtitle sidecar discovery (Phase 2) |

Rules:

- One configured root. Every path is resolved and checked; `..` and symlinks that escape the root are rejected.
- Token scopes: read, write, admin. Tokens default to read-only.
- Changes made outside the server (files added or removed on disk) show up without a restart.
- Every operation has a matching CLI command.

### 5.5 CLI (MVP)

The CLI is the same binary as the server, run with subcommands. It talks to a local or remote server using a URL and token.

| Command | Purpose |
|---|---|
| `serve` | Start the server |
| `add <url \| magnet \| file>` | Add a download; flags for format, quality, and mode |
| `formats <url>` | Show available formats for a video page |
| `ls`, `stat` | List downloads, or list and inspect files |
| `watch <id \| path>` | Print a stream URL or open a player |
| `copy <id \| path> --drive` | Copy to Google Drive |
| `mv`, `rm`, `mkdir` | File operations (write scope) |
| `pause`, `resume`, `mode` | Control a download and switch its mode |
| `peers <id>`, `pieces <id>`, `trackers <id>` | Show the peer list, piece map and availability, and trackers; re-announce |
| `limits`, `recheck <id>` | Set bandwidth limits; force a hash recheck |
| `config` | View and change settings |
| `cookies import` | Import a cookie file for a domain |
| `doctor` | Report helper tools that are present or missing and the features affected |
| `update check`, `update apply`, `update rollback` | Check for, apply, or undo updates to the server and the managed helper tools (yt-dlp, rclone) |
| `providers list`, `providers enable <name>`, `providers disable <name>` | Show providers with status, and switch them on or off |
| `providers config <name>` | View or change a provider's settings; for a drive, run its sign-in |
| `setup` | Guided or flag-driven first-run setup; safe to re-run |
| `pair` | Show the QR code (URL, token, certificate fingerprint) for the Android app |
| `uninstall` | Remove the service and binary; keeps config and downloads unless `--purge` |

- A `--json` flag on every command, for scripting.
- Server URL and token from flags or environment variables.
- Built first, as the first client of the API, so the API is tested before the Android app exists.

### 5.6 Cookies (MVP, basic)

- Import a cookie file per domain through the CLI or API.
- Pass the cookies and the matching User-Agent to yt-dlp and the HTTP downloader.
- Cookies are encrypted at rest, scoped per domain, sent over TLS only, and never logged.
- In-app WebView login capture on Android is Phase 2.

### 5.7 Android app (MVP)

- Share target: "Share to app" from any browser or app sends the link to the server.
- Handler for `magnet:` links and .torrent files.
- Format and quality picker for video pages.
- Choose standard or stream mode when adding.
- File manager over the folder API: browse, play, share, rename, move, delete, copy to Google Drive, storage usage.
- Native video player using the Range endpoint.
- Queue view with progress, and completion notifications.
- Connect to your own server by URL and token.
- Shows the server version and an "update available" notice.

### 5.8 Updates

Updates must be easy to deliver and safe to apply. The rule is to use existing, proven update mechanisms and libraries, not a custom update system.

| What | Existing mechanism | Native binary | Docker |
|---|---|---|---|
| yt-dlp | Its own built-in self-update, with stable and nightly channels (verify exact flags) | The program runs it on a daily schedule | Same, writing into the data volume |
| rclone | Its own built-in `rclone selfupdate` (verify) | The program runs it on a weekly schedule | Same, writing into the data volume |
| Server binary | A Go self-update library such as go-selfupdate (MIT): finds the latest GitHub release, checks SHA-256 checksums and ECDSA signatures, replaces the executable, and rolls back a failed replace | Check daily; apply on approval or automatically | Not self-replaced; update by pulling the new image |

Release pipeline: build, archive, checksum, and publish to GitHub Releases with a standard Go release tool such as GoReleaser, and sign the release artifacts (from my own knowledge; verify the signing options).

Docker:

- The standard way to update a container is to update its image: `docker compose pull && docker compose up -d`. The project documents this and does not build its own mechanism.
- Watchtower, the popular automatic updater, was archived in December 2025 and is no longer maintained. Alternatives to mention in the docs: What's Up Docker, and a maintained Watchtower fork.
- yt-dlp and rclone still update in place because they live in the data volume.

What the existing pieces cover, and the small glue that remains:

1. **Check, download, verify, replace:** the self-update library.
2. **Restart:** left to the supervisor. The program exits cleanly after an update, when idle or on user approval, and systemd (`Restart=always`) or the Docker restart policy starts the new version. Jobs resume from the database.
3. **Rollback of a bad version:** the library handles a failed replace. For a version that installs but does not run, use the established "confirm after boot" pattern from OS A/B updates: keep the previous binary, write a pending marker when updating, clear it once the new version passes its health check, and revert on the next start if the marker is still there. This is the only custom piece, and it is small.
4. **Database:** copy the SQLite file before any schema migration, since a rollback after a migration needs the old data. Migrations only move forward.

Rules:

- Modes per category: `auto`, `notify`, or `off`. Proposed defaults: helper tools `auto`, server `notify`.
- Stable and beta channels, using release tags; the user can pin a version.
- The API is versioned so an older Android app works with a newer server and the reverse.
- Manual control from the CLI: `update check`, `update apply`, `update rollback`.
- The Android app updates separately (store, F-Droid, or direct APK) and only shows the server's update status.

### 5.9 Torrent management (MVP)

Every item below is in the first version, built at a basic level first and polished later. Each is also an item on the engine spike checklist (section 14).

| Feature | Detail |
|---|---|
| Seeding policy | Stop at 100% (default), seed to a ratio or time limit, or seed forever; per-torrent override |
| Bandwidth limits | Global and per-torrent download and upload caps |
| Connection settings | Listening port; toggles for DHT, peer exchange, local discovery, uTP, IPv6; peer limits; protocol encryption policy (prefer, require, off) |
| Anonymous mode | One switch that turns off DHT, peer exchange, and local discovery and limits what the client reveals. It does not hide the IP address; that needs a proxy or VPN binding |
| Network binding | Bind torrent traffic to a chosen interface or address, optional kill switch if it goes down, and a SOCKS5 proxy option |
| IP blocklists | Load from a file or URL and refresh on a schedule |
| Peer, piece, and availability view | Peer list (address, client, progress, speeds, direction), piece map bar, availability (seeders, peers, distributed copies), and a plain-language "why is this slow or stalled" hint that also explains buffering in stream mode |
| Web seeds | HTTP sources listed inside a torrent are used automatically |
| BitTorrent v2 and hybrid | Open and download them; no special interface needed |
| Archive extraction | Optional per download or by default; zip, tar, gzip, 7z, and rar where the chosen library allows; option to delete the archive after a successful extract. Safety: reject paths that escape the folder, enforce size and ratio limits against decompression bombs, skip password-protected archives with a clear message |
| Incomplete files and resume | Incomplete files stay in a temporary area and move when finished; resume data survives crashes; force recheck |
| Disk space guard | Check free space before starting; pause when it runs low |
| Metadata handling | A "fetching metadata" state with a timeout, then the file picker, with a clear "no peers found" outcome |
| Duplicate detection | Adding an existing magnet or torrent reuses the existing entry |
| Tracker management | See tracker status, add or remove trackers, force a re-announce |
| Path sanitization | Reject torrent file names containing `..` or absolute paths |
| Stream-only cache | A "watch only" mode with a cache size limit that discards data after viewing |
| Queue and priorities | Maximum active downloads, reordering, per-file priority |
| Inbound reachability | Optional automatic port mapping (UPnP or NAT-PMP, off by default) and a reachability test |
| Post-completion pipeline | After finishing: extract, copy to Google Drive, move, notify, and optionally delete the local copy once the Drive copy is verified |

### 5.10 Access, HTTPS, and ports (MVP)

Access modes:

1. **LAN, self-signed certificate.** The Android app pins the certificate fingerprint it receives through QR pairing.
2. **Domain with automatic certificates.** Built on CertMagic: HTTP-01 or TLS-ALPN-01 when ports 80 and 443 are reachable, or DNS-01 with a DNS provider token otherwise. DuckDNS is an optional provider, not the default (verify that a provider exists).
3. **Reverse proxy, tunnel, or VPN.** Plain HTTP on localhost or on a chosen interface, for users running a proxy, Tailscale, a Cloudflare Tunnel, or their own WireGuard.

Phase 2: Let's Encrypt IP address certificates (short-lived) and a Tailscale integration.

Pairing and ports:

- `pair` prints a QR code containing the URL, token, and certificate fingerprint.
- `doctor` lists the ports to open (HTTPS and certificate challenge, the torrent port over TCP and UDP) and prints matching firewall commands. It never changes the firewall without consent; an opt-in `--open-firewall` flag may do it for supported firewalls.
- Port conflicts (80 and 443 already in use) are detected, with an alternative port or the reverse-proxy mode offered.
- `doctor` also reports certificate expiry.
- Public certificates appear in public certificate transparency logs, so the hostname is discoverable.

### 5.11 Install, configuration, and uninstall (MVP)

- **Install:** one command, `curl -fsSL <release host>/install.sh | sh`, with flags and matching environment variables: `--docker`, `--data-dir`, `--downloads-dir`, `--port`, `--domain`, `--access`, `--version`, `--channel`, `--no-start`, `--yes`. With no flags it asks a few questions with defaults.
- The script creates a dedicated non-root user, installs the binary after checking its checksum and signature, writes the config and a systemd unit, generates a token, starts the service, and prints the pairing QR code. Re-running it upgrades or repairs.
- **Packages:** `.deb` and `.rpm` packages and an apt repository follow later.
- **Configuration:** one config file; precedence is flags over environment variables over file over defaults. `config get`, `config set`, `config show --effective`, and the same settings in the app and web UI; most changes apply without a restart, and the rest are flagged as needing one. `setup` can be re-run.
- **Uninstall:** `uninstall` stops and removes the service, unit, and binary and keeps config and downloads. `--purge` also removes config and data. The downloads folder is only removed after an explicit typed confirmation. The install script has an `--uninstall` option for people who no longer have the binary. Docker: `docker compose down`.
- **Updates:** see 5.8.

### 5.12 Providers: sources and destinations (MVP)

Two kinds of provider sit behind the same interfaces, so new ones can be added without touching the rest of the program.

- **Source:** takes a user input and turns it into something the download engine can fetch (probe, list formats, resolve to a URL or info hash).
- **Destination:** a place finished files can be sent (list, upload, verify, optionally delete the local copy).

Built-in providers in the MVP:

| Kind | Provider | Notes |
|---|---|---|
| Source | Web downloads | Direct HTTP/HTTPS/FTP links |
| Source | Torrents | Magnet links and .torrent files |
| Source | Video sites | Through yt-dlp, which covers many sites; no per-site toggles |
| Destination | Local folder | Always on |
| Destination | Google Drive | Through rclone |
| Destination | Other rclone backends (for example OneDrive, Dropbox, S3, SFTP) | After Google Drive works; each is a toggle |

How it works:

- Every provider declares a name, a description, the helper tools it needs, its status (ready, missing tool, needs sign-in, or error), and a settings schema (fields, types, defaults, which are secret).
- The settings screen is generated by the server from those declarations: a toggle per provider, a status line, and a form for its settings. The Android app and web UI render whatever the server sends, so adding a provider needs a server update only.
- A disabled provider is hidden from the add-download and copy-to menus. A provider whose tool is missing shows why, and a button or `doctor` hint to fix it.
- Provider settings are stored in the single config file (5.11) and apply without a restart where possible.
- Secrets (tokens, API keys) are encrypted at rest and never returned by the API after they are set.

Signing in to a drive on a headless server:

- The server has no browser, so the sign-in runs on the phone: the app opens the provider's login page, receives the token, and sends it to the server over the paired connection. The CLI offers a device-code or paste-the-token flow where the provider supports it. To be verified for Google Drive and rclone.
- For Google Drive, use our own client ID (see section 10).

Third-party plugins:

- Not in the MVP. Built-in providers only, so every one is tested and reviewed.
- Later (Phase 2): external providers as separate executables that speak a small JSON contract over stdin/stdout or local HTTP, or a WASM sandbox. Go's native `plugin` package is avoided because it is fragile and not portable.
- External providers would be off by default, with a clear warning, and possibly signed. A marketplace is out of scope.

Policy:

- Legally sensitive providers (the Stremio-style source in section 8) are off by default and left out of the Play build.
- Toggles are for the user's own server and do not change what the Play build contains.

## 6. Portability tiers

| Tier | Targets | Support level |
|---|---|---|
| 1 | Docker Compose on any Linux distro, VPS, NAS, home server | Supported and tested; first class |
| 2 | Native binary on bare Linux and Raspberry Pi (x86-64 and ARM) | Supported and tested; first class, equal priority to tier 1 |
| 3 | Native binary in Termux on an Android device | Best effort |

Notes on tiers 1 and 2:

- Docker and the native binary are both important. Neither is a second-class option, and every feature must work in both.
- **Docker** bundles every helper tool, isolates the risky parts (fetching arbitrary URLs, running ffmpeg and yt-dlp on untrusted files), and fits NAS platforms such as Synology, Unraid, and TrueNAS. The image refreshes yt-dlp on start.
- **Native binary** is one static file for x86-64 and ARM that runs on any Linux distribution with no installer. It needs no C dependencies (pure-Go SQLite driver; verify the choice).
- **Helper tools on the native binary:** the program downloads yt-dlp and rclone into its own data folder and keeps them updated. ffmpeg is checked on the system, since it normally comes from the distro's package manager.
- **`doctor` command** reports which helper tools are present, which are missing, and which features are switched off as a result.
- **Packaging for the native binary:** release binaries, a systemd unit file, and an install script.
- Windows and macOS: the Go code may cross-compile, but they are untested and unsupported.

Notes on tier 3:

- Docker generally does not run on a stock, unrooted Android phone, so Termux with the native binary is the route.
- Android has no systemd; the process needs its own way to stay running.
- Android's phantom process killer limits background processes (about 32 across all apps) unless child process restrictions are disabled in Developer Options. A single-process server fits this limit far better than a multi-daemon stack.
- Termux needs its wake lock enabled so the system does not suspend it.
- Mobile networks often sit behind carrier NAT, so remote access needs a tunnel or VPN, and inbound torrent connections may be poor.
- Video-site support (yt-dlp, YouTube) is not a goal on tier 3. The server runs without it there, and the feature switches off cleanly.

## 7. Phase 2 (planned, not in the MVP)

- In-app WebView login that captures cookies on Android.
- yt-dlp playlists, subtitles, and chapter handling.
- ffmpeg fallback transcoding (HLS) for files players cannot play directly.
- WebDAV endpoint, thumbnails, and search in the folder API.
- Multiple servers in the app.
- Web UI polish.
- Pro features for the paid app.
- Torrent extras: categories or labels with their own save folders, a bandwidth scheduler, creating `.torrent` files, exporting magnet links, private tracker ratio tracking.
- Let's Encrypt IP address certificates and a Tailscale integration.
- External providers (separate executables with a JSON contract, or WASM), off by default.

## 8. Phase 3: source plugins (Stremio-style), optional

### 8.1 Is it possible?

Yes. The Stremio addon protocol is plain HTTP and JSON:

- `GET /manifest.json` describes the addon and what it offers.
- `GET /catalog/...` and `GET /meta/...` list and describe titles.
- `GET /stream/{type}/{id}.json` returns stream objects, each either a direct `url` or an `infoHash` (with an optional file index) for a torrent.

So the server can act as a client of addons the user adds: it asks for streams, takes the returned URL or info hash, starts the download in stream mode, and serves the result through the same Range and folder API. Playback then happens in this app from the user's server, not in Stremio. The reverse is possible too: the server can expose its own library as an addon so Stremio can play from it.

### 8.2 How it would be built

- A source provider (5.12) that can browse or search and resolve to a stream, with a Stremio-addon adapter as the first implementation.
- The user adds addon URLs themselves. Nothing is bundled and no catalogs are pre-installed.
- Disabled by default.
- Resolved streams are handed to the normal download engine in stream mode.

### 8.3 Constraints

- This is the highest-risk feature for legal exposure and store policy. Many third-party addons point at infringing content, and a plugin that browses and resolves them behaves like a content discovery layer.
- It conflicts with the existing out-of-scope rule on torrent search and indexers, so it stays a separate, optional plugin.
- It must stay out of the Google Play build. Distribute it only through the direct APK and F-Droid builds, if at all.
- The project does not endorse infringement; users are responsible for the addons they add and what they fetch.
- Decide this phase only after the MVP is proven.

## 9. Out of scope

- Multi-user accounts, public sign-up, quotas, or user management.
- A hosted or managed service run by us.
- Built-in torrent search, indexers, or bundled content catalogs.
- Site-specific features or branding for YouTube or similar platforms.
- iOS app.
- RSS automation and share links.
- A shared cache of other users' torrents (the Seedr-style instant-download trick).
- Billing infrastructure on the server.
- Docker support on stock Android.
- Official support for Windows or macOS servers.
- Built-in WireGuard or another VPN (not for now). The server can bind to a VPN interface, so people who run WireGuard or Tailscale can use them.
- A third-party plugin marketplace.
- Any guarantee of reliable 24/7 hosting on a phone.

## 10. Constraints and assumptions

- The server runs on hardware the user controls.
- Hosting providers have their own torrent and DMCA policies. Users must check theirs; some reportedly restrict public torrents.
- YouTube's terms restrict downloading. Using yt-dlp is the user's responsibility on their own server.
- Google Drive limits uploads to about 750 GB per day per account. Using our own Google API client ID is recommended.
- Google Play removes apps that download or access YouTube content against its terms. The Play app must stay a generic "send URL to my server" client.
- Cookies are session credentials and must be protected as described in 5.6.

## 11. Licensing and dependencies

Reuse existing tools wherever the licence allows. This is not legal advice; verify each licence at the version you ship, and get a lawyer's check before charging money.

| Tool | Role | Licence | Rule |
|---|---|---|---|
| yt-dlp | Video pages | Unlicense (public domain) | Use freely; yt-dlp-ejs bundles ISC and MIT code, keep notices |
| JavaScript runtime (Deno, Node, QuickJS) | yt-dlp for YouTube | MIT (verify) | Use freely, keep notices |
| rclone | Google Drive | MIT (verify) | Use freely, keep the notice |
| anacrolix/torrent (Go) | Torrent engine (first choice) | MPL-2.0 (per its README) | Fine in a proprietary product if the source of the MPL files stays available |
| libtorrent | Torrent engine (fallback) | BSD (per its site) | Use freely with the licence notice; native C++ dependency |
| Go archive library | Archive extraction | Check the chosen library (permissive preferred) | Verify rar decoding licence terms before choosing |
| ffmpeg | Merge, transcode | LGPL-2.1+ by default; GPL if built with `--enable-gpl` | Run as a separate program; ship an LGPL build or let the user supply it |
| aria2, qBittorrent | Downloaders | GPL family | Do not embed or link; avoid by building the HTTP downloader in-process |
| OpenList, Cloudreve | File managers | AGPL-3.0, GPL-3.0 | Do not use as a base for closed code |

Rules of thumb:

- Permissive licences (MIT, BSD, Apache, Unlicense): use freely with notices.
- MPL-2.0: keep the covered files' source available.
- LGPL: fine when separate or replaceable.
- GPL and AGPL: keep out of closed code; shipping a GPL binary requires offering its source.
- Every release ships a third-party licences file.

### Open or closed source (recommendation, not decided)

Open-core: open-source the server and CLI, keep the paid Android app closed.

- Self-hosters trust software that holds Drive tokens and cookies when they can audit it.
- Comparable tools are open, and open code enables F-Droid and community contributions.
- Money comes from convenience: the app, a Pro unlock, support.
- Licence options for the server: AGPL-3.0 (blocks closed forks and hosted clones; allows selling a commercial licence as copyright holder), MPL-2.0 (middle ground), MIT (maximum adoption, allows closed forks).
- Decide the licence before the first public commit; relicensing later needs contributors' consent.
- Gate Pro features in the app, not the open server, since they can be removed from open code.
- Closed-source remains possible with the dependencies above, at the cost of trust and adoption.

## 12. Monetization (tentative)

- Android app free in a beta; later a paid app or a Pro unlock (Drive copy, cookie sync, multiple servers).
- Sell outside Play too (licence key) to avoid depending on one store.
- Validate demand with a free beta before setting any price.

## 13. Risks

| Risk | Mitigation |
|---|---|
| Play Store rejection or removal | Generic client, no YouTube or Stremio features in the Play build, no bundled search; also distribute through F-Droid and a direct APK |
| YouTube changes break downloads | Self-updating yt-dlp, JS runtime support, clear error messages |
| JS runtime dependency for YouTube | Treat yt-dlp and the runtime as optional tools on tiers 1 and 2; not supported on tier 3 |
| Path traversal or symlink escape in the folder API | Resolve and check every path against the root; test with hostile inputs |
| Provider abuse reports or DMCA | Document provider policies; support running behind a VPN |
| Google Drive quota and verification | Own API client ID, per-user OAuth, check Google's verification rules early |
| SSRF through "add by URL" | Block private and internal address ranges for every fetch |
| Cookie theft if the server is compromised | Encryption at rest, short retention, per-domain scope |
| Stream mode stalls on poorly seeded torrents | Show buffering state; let the user switch to standard mode |
| Source plugin legal exposure | Optional, off by default, no bundled addons, kept out of the Play build, decided after the MVP |
| Low demand for a paid app | Free beta first; charge only after real usage |
| Phone hosting is unreliable | Label it best effort; document wake lock and process-limit setup |
| Embedded torrent engine less proven than libtorrent | Evaluate streaming and stability early; keep the engine behind an interface |
| A bad or malicious update reaches users | Signed releases verified by the update library, previous version kept, confirm-after-boot rollback, server updates default to notify |
| The chosen torrent engine lacks a required MVP feature | Time-boxed spike with a pass or fail checklist before building on it; engine interface allows switching to libtorrent |
| The larger MVP slows delivery | Build in milestones (core, torrent management, integrations, Android); each feature at a basic level first |
| Archive extraction attacks (path escape, decompression bombs) | Path checks against the folder root, size and ratio limits, skip password-protected archives |
| Closed or unreachable ports reduce peers and hurt stream mode | Reachability test, optional UPnP, `doctor` guidance |
| Third-party providers run untrusted code | None in the MVP; later, separate processes or WASM, off by default, with warnings and possibly signatures |
| Drive sign-in on a headless server is awkward | Sign in from the app and pass the token over the paired connection; device-code flow in the CLI; verify with rclone early |
| Provider settings drift from what the apps show | The server generates the settings schema and the apps only render it |

## 14. Proposed architecture (to confirm)

- **One static binary, written in Go (leaning).** It contains the API, the CLI subcommands, the embedded web UI, the HTTP downloader, the torrent engine, and the folder and streaming handlers. It cross-compiles to ARM and x86.
- **Torrent engine:** start with anacrolix/torrent (pure Go, MPL-2.0), kept behind an interface. Before building on it, run a time-boxed spike that marks every item in 5.9, plus stream-mode seeking, as pass, buildable in our own layer, or gap. Decision gate: if more than a couple of must-have items fail and cannot be built cheaply, switch to libtorrent through cgo, accept a less portable build (Docker first, a dynamically linked native build), and keep the interface so the rest of the program is unchanged. rqbit (Rust) was reviewed and set aside for now: its page lists no web seeds, v2, blocklist, or encryption, and it is a beta.
- **Maintenance:** hands-off by design: automated dependency update pull requests, CI with integration tests (including a local test swarm and a yt-dlp smoke test), automatic tagged releases, and the update system in 5.8.
- **Optional external tools,** detected at startup: yt-dlp plus a JS runtime, ffmpeg, rclone. On the native binary, yt-dlp and rclone are downloaded and updated by the program itself into its data folder; ffmpeg is taken from the system. In Docker all of them come in the image.
- **Storage:** SQLite through a pure-Go driver, so the binary stays static and CGO-free.
- **Updates:** an existing Go self-update library for the server binary, the helper tools' own self-update commands, GoReleaser-style release publishing, and the supervisor (systemd or Docker) for restarts. See 5.8.
- **Providers:** Source and Destination interfaces in the same binary; a registry that reports each provider's status and settings schema to the API; rclone behind the drive destinations. See 5.12.
- **Android app** in Kotlin or Flutter, talking to the same API.
- **Packaging:** a Docker image, plus release binaries for Linux x86-64 and ARM.

## 15. Success criteria for the MVP

1. A fresh server comes up from one `docker compose up` and a documented setup.
2. The same server runs as a native binary on a Raspberry Pi or a plain Linux box with no Docker, and `doctor` correctly reports any missing helper tool.
3. A static binary built once runs unchanged on at least two different Linux distributions (for example Debian and Alpine).
4. Every MVP action works from the CLI with `--json` output.
5. Sharing a magnet, .torrent, direct URL, and a YouTube link from Android all start a download on the server.
6. For a YouTube link, the app lists formats and the chosen quality is what gets downloaded.
7. A torrent started in stream mode begins playing before it finishes, and seeking forward works after a short wait.
8. A finished video plays from the folder API with seeking.
9. The Android app browses, renames, moves, deletes, and plays files in the download folder.
10. A finished file copies to Google Drive from the app.
11. The folder API rejects `..` paths and symlinks that leave the root, and the server rejects URLs pointing at internal addresses.
12. An update to a newer release installs, restarts, and resumes running downloads, and a deliberately broken release is rolled back automatically to the previous working version.
13. The engine spike checklist is complete: every item in 5.9 is marked pass, built in our own layer, or recorded as a gap before the engine is final.
14. The peer list, piece map, and availability view show live data for a running torrent and explain a stalled stream.
15. A torrent containing an archive extracts into the download folder with the safety limits applied, a hybrid v1/v2 torrent downloads, and a torrent with web seeds uses them.
16. On a fresh VPS, one install command followed by scanning the pairing QR code gives the Android app a working HTTPS connection.
17. Best-effort target: the binary runs in Termux (without video-site support) and completes a download and a stream test.
18. Switching a provider on or off in the app takes effect without a restart, a provider with a missing tool shows why, and a settings form for a newly added provider appears in the app with no app update.

## 16. Open questions

1. Server language: Go. Decided.
2. Torrent engine: pure-Go library first, with a time-boxed spike against the 5.9 checklist; libtorrent as the fallback (see 14).
3. Open source, closed, or open-core, and which licence?
4. Android app: Kotlin or Flutter?
5. Remote access: Cloudflare Tunnel or Tailscale as the documented default?
6. Stremio-style plugin: pursue at all, and under what distribution rules?
7. Project name and CLI command name.
8. Notifications without Google services (ntfy or UnifiedPush) versus Firebase.
9. Update library choice (go-selfupdate or another), release signing method, and where releases are hosted.
10. HTTPS: which DNS providers to support for the DNS challenge (DuckDNS, Cloudflare), whether a DuckDNS provider exists in the library CertMagic uses, and whether CertMagic supports IP address certificates.
11. Archive library: which Go library, and the licence terms for rar decoding.
12. Providers: how the app passes a drive's OAuth token to the server (verify with rclone), whether external providers are worth building, and which drives come after Google Drive.

## 17. Decisions so far

| Decision | Status |
|---|---|
| Server language: Go | Decided |
| Hosting: private GitHub repository from the first commit | Decided |
| Licence: deferred; no licence file until decided (all rights reserved) | Decided to postpone |
| Single-user, self-hosted | Decided |
| Android app for remote downloads | Decided as an idea |
| Run almost anywhere: Docker, and Termux on Android if possible | Decided as a goal |
| Docker and the native binary are both first-class install paths | Decided |
| Easy, failsafe updates built on existing mechanisms, not a custom system | Decided as a goal; mechanism in 5.8 is proposed |
| Torrent management features (peer and piece view, blocklists, anonymous mode, web seeds, archive extraction, v2 and hybrid) are in the MVP | Decided |
| No built-in WireGuard for now; the server can bind to a VPN interface | Decided |
| Prefer simplicity: proven components, hands-off maintenance, self-updating | Decided as a principle |
| HTTPS access modes, QR pairing, one-command installer, single config file, clean uninstall | Proposed, needs confirmation |
| Torrent engine: pure-Go first, libtorrent as the fallback after a spike | **Decided (2026-10-10): anacrolix/torrent v1.61 kept** after the spike (docs/engine-spike.md); v2-only torrents rejected until fixed upstream |
| Sources and destinations are providers with on/off toggles and server-generated settings; built-in only in the MVP | Proposed, needs confirmation |
| CLI controls | Decided as a goal |
| YouTube links with format and quality choice | Decided as a goal |
| Folder API, with the app acting as a file manager | Decided as a goal |
| Two download modes (standard and stream) and direct streaming of downloaded videos | Decided as a goal |
| YouTube and video sites not required on the Termux tier | Decided |
| Reuse existing tools where licences allow | Decided as a goal |
| Stremio-style plugin that relays from the server | Wanted later; feasible; high risk |
| Charging money for the app | Under consideration |
| Open or closed source | Undecided; open-core recommended |
| API-first, single Go binary, three portability tiers, CLI built first | Proposed, needs confirmation |
| Everything else in sections 5 to 15 | Proposed, needs confirmation |
| Project name `getaload` for now; CLI name `getaload` is a placeholder | Decided to postpone the final name (2026-10-10) |
| Repo private now; public with a licence later | Decided (2026-10-10) |
| Development runs in Docker (Go dev container); nothing installed on the host | Decided (2026-10-10) |
| Goal: IDM-grade HTTP downloader (dynamic segmentation, resume, retry, header passthrough), proven by a benchmark against aria2c | Met for M1 (2026-10-11): tie on a clean network, ~30% faster than aria2c with slow routes (docs/benchmark.md). Techniques: speed-aware stealing, latest-ETA victim, relative slow-connection restart |
| Browser extension (send downloads with cookies and headers, media sniffer) as milestone M5 | Proposed (2026-10-10) |
| Stack: cobra (CLI), koanf (config), huma v2 (API + OpenAPI), modernc.org/sqlite, mholt/archives, creativeprojects/go-selfupdate, code.dny.dev/ssrf, `os.Root` for path safety | Proposed (2026-10-10) |
| Provider settings forms use plain JSON Schema | Proposed (2026-10-10) |
| `serve` does not start the torrent engine until the job layer needs it (M1), so a bare server never joins the public DHT | Decided (2026-10-10) |
| API contract: generated OpenAPI committed at api/openapi.json; a snapshot test fails on any change; /v1 is additive-only | Decided (2026-10-10) |
| Swarm memory: learned trackers (ranked) and a DHT node cache in SQLite, auto-added to new public torrents only, never private ones; no peer IPs kept | Built (2026-10-11): learned_trackers table (success count, 90-day window), DHT node cache file; URLs with queries, credentials or passkey-like path segments are never stored |
| Stack in use: cobra, koanf, huma v2, modernc.org/sqlite, code.dny.dev/ssrf, os.Root, anacrolix/torrent | Decided (2026-10-10), replaces the "Proposed" stack row |
| FTP downloads deferred from the M1 core: rare today, and passive-mode data connections need their own SSRF guard. Build when requested | Proposed (2026-10-11) |
| Web UI is first-class (seedr.cc-style control panel, embedded in the binary), including "Connect Google Drive" via the device flow (code + google.com/device, no redirect URLs) | Decided (2026-10-11) |
| Google OAuth client ID: configurable (gdrive settings), created later in the owner's Google Cloud account; never committed | Decided (2026-10-11) |
| Releases publish to the private repo for now; the repo goes public when the project matures (install.sh and self-update for others work from then) | Decided (2026-10-11); replaces the separate public releases repo idea |
| Repository made public (2026-10-11) at the owner's request, after rewriting history to use the GitHub noreply email and drop personal location details; no licence file yet (all rights reserved) | Decided (2026-10-11) |
| Helper tools (yt-dlp, rclone, deno) are installed and updated by our own verified installer from their official GitHub releases (SHA-256 from the project's checksum files, self-test, previous copy kept, rollback), not by each tool's own self-updater: one path that can always roll back. Daily; updates.tools auto or off | Decided (2026-10-11); refines §5.8 |
| Release signing: ECDSA P-256 key; private key only as the CI secret RELEASE_SIGNING_KEY plus the owner's offline backup; GoReleaser signs checksums.txt with openssl, the server verifies with the embedded public key. Rotate by replacing internal/update/release-key.pub and the secret | Decided (2026-10-11) |
| Android app in Flutter (owner's choice, 2026-10-11), in this repo under app/ for now; built on Windows with the installed Flutter SDK. Licence split (open server, closed app) can move it to its own repo later | Decided (2026-10-11); replaces the Kotlin proposal |
| App work (M4) waits until the server is confirmed working in a real deployment | Decided by the owner (2026-10-11) |
| Testing stays legal and local: no public torrent swarms, trackers or DHT from the dev machine; isolated Docker swarm with self-generated content only (AGENTS.md rule 7) | Decided (2026-10-10) |
| Pairing from the web panel: admin `POST /v1/pair` returns the getaload://pair payload and the QR as a PNG data URL, rendered server-side with rsc.io/qr (BSD, already in the module graph via qrterminal) instead of vendoring a JS QR encoder. The browser sends its own origin as the server URL. Token helpers moved from cmd/getaload to internal/api (AddToken, PairPayload) | Decided (2026-10-11) |
| The app lives in its own private repo (sadatdaniel/getaload_app), not under app/ here: the server repo is public and the app may stay closed | Decided by the owner (2026-10-11) |
| Multiple users (owner decision 2026-10-12, replaces "single user"): users with a name and a 4-digit PIN in SQLite (PIN hashed with PBKDF2-SHA256 + salt; 5 wrong tries lock the user with doubling back-off). Sign-in (owner decision 2026-10-12): server address + user name + PIN issues a new device token bound to that user (one token per device, never typed or shared; a per-user device list revokes them); the device stays signed in. No pairing link needed for normal use (QR/link pairing stays for admin devices). No strict mode. Every request acts as the token's user. Strict separation: each user has downloads/users/<id>/ (os.Root per user), own jobs, files, media, favorites, watch progress and add-ons; nobody sees another user's data, not even the first user. No per-user permissions (adults). The first user is created by install.sh (asks name + PIN) or `getaload user add`; downloads from before users existed are not migrated (owner reinstalls). Same torrent in two accounts: refused while it runs in the other account (ponytail) | Decided by the owner (2026-10-12) |
| Multi-user API details (2026-10-13): `POST /v1/login {name, pin, device}` (no token) stores a token named `u:<user id>:<device>` in the config file and binds its hash in `device_users`; the owner's devices get admin scope (users, providers, updates), everyone else write. Once a user exists, a token that has not signed in (the installer's owner token, `getaload pair`) reaches only health, `/v1/users`, login and server-wide admin routes, never anyone's jobs or files. Job paths are shown relative to the user's folder. Per-user preferences (favorites, watch progress) are a JSON object per user (`/v1/prefs/{key}`, 1 MiB). `getaload user add`, `list`, `pin`, `remove` works on the database directly (as the service user), so the installer can add the first user before the service starts and a forgotten owner PIN can be reset over SSH; `getaload login` signs the CLI in. Add-ons are still one list for the server (owner manages) until they move into prefs | Decided (2026-10-13) |
| Add-ons that block the server (owner decision 2026-10-13): many add-ons (Torrentio, behind Cloudflare) refuse data-centre addresses (403 for any User-Agent from the Oracle VM). Such an add-on can be switched to "Ask from this phone" (`addons.phone`, `PUT /v1/addons/phone`, offered when adding fails with 403): the server stores it but never contacts it; the app fetches only its JSON (manifest, catalogs, meta, the list of sources) over HTTPS and hands a chosen source to `POST /v1/addons/play`. The phone never makes torrent connections (no peers, trackers or DHT); external links open only when they are http(s), and "Copy link" never copies a magnet. Replaces "the app only ever talks to the server" for these add-ons only. Not bundling, suggesting or auto-updating torrent scrapers (Torrentio, MediaFusion, Sootio): users may add any manifest URL themselves | Decided (2026-10-13) |
| Cookies per user (owner decision 2026-10-13): each user imports cookies.txt for their own downloads (data/cookies/users/<id>/, sealed with the user id so a jar copied to another user does not open); yt-dlp and web downloads use only the job owner's cookies; import and delete need a signed-in user (write), no longer admin; removing a user deletes their cookies. The app has Settings, Site sign-ins (import a cookies.txt, site guessed from the file). Typical use: YouTube asking a cloud server to "confirm you're not a bot" | Decided (2026-10-13) |
| Google Drive per user (owner decision 2026-10-13): each user connects their own Drive from the app (device code, approve in the phone's browser); the token is sealed per user in `user_secrets` (deleted with the user); copies and after-download copies use the job owner's Drive; `GET/DELETE /v1/me/drive`; the sign-in endpoints need a signed-in user (write), not admin. The Google client ID stays server-wide (the app's registration), to be built into releases from a CI secret so users never configure it. Helper tools (rclone, yt-dlp, deno) install the moment a feature needs them (tools.OnDemand) instead of waiting for the daily maintenance | Decided (2026-10-13) |
| Stremio add-ons (experimental, off by default): the server is the add-on client (internal/addons; protocol docs/protocol.md of stremio-addon-sdk), fetches through netguard, caches per cacheMaxAge, merges answers; API /v1/addons*. Torrent streams become stream-mode jobs (magnet from infoHash + "tracker:" sources); direct links are returned for the app to play. No add-ons ship; tests use our own local test add-on, plus an opt-in live parse check of Cinemeta and OpenSubtitles (JSON only). Subtitle search goes through add-ons (OpenSubtitles add-on) instead of a separate OpenSubtitles API integration | Decided by the owner (2026-10-12) |
| VPN: only WireGuard profiles for downloads (paste a .conf; userspace wireguard-go + netstack inside the binary, no root), with the existing kill switch. Incoming torrent connections through a provider's tunnel usually need port forwarding from the VPN provider (say so in the UI). No Tailscale, no OpenVPN for now | Decided by the owner (2026-10-12) |
| Exception to AGENTS.md rule 7, granted by the owner: one streaming test with the Creative Commons "Sintel" torrent (Blender Foundation, CC BY) on the owner's Oracle server, not on the dev machine. Every other torrent test stays in the isolated swarm | Decided by the owner (2026-10-12) |
| GET /v1/media lists every video and audio file (newest first) for the app's Media screen; walks the tree per call (ponytail in fsapi) | Decided (2026-10-12) |
| Archives open like folders: GET /v1/archive lists one level, /v1/archive/raw/{path}?entry= streams one file (token or the archive's signed link); read-only, nothing unpacked on disk | Decided (2026-10-11) |
| server.cors (default empty): browser origins allowed to call the API, for testing the app's web build. Tokens are bearer headers, never cookies | Decided (2026-10-11) |
| App: no uploads to the server; the only way in is a queued download (link, magnet, video page, .torrent). File manager works on what the server downloaded | Decided by the owner (2026-10-11) |
| App player: media_kit (libmpv, LGPL dynamic) for tracks, dual subtitles, hwdec, buffer, codecs. Notifications only while the app runs (no push). App UI tested on Flutter web | Decided by the owner (2026-10-11) |
| Android: Kotlin + Compose with a DocumentsProvider; notifications through UnifiedPush/ntfy | Proposed, decide at M4 |

## 18. Build order (handoff to Claude Code)

Work in this order. Do not start a step before the one above it has a written result.

1. **Torrent engine spike (first).** Time-boxed. Using anacrolix/torrent behind a small engine interface, test every item in 5.9 plus stream-mode seeking (Range requests moving piece priority, first and last pieces first). Mark each item pass, buildable in our own layer, or gap, and write the results to `docs/engine-spike.md`. Apply the decision gate in section 14: if more than a couple of must-have items fail and cannot be built cheaply, switch to libtorrent through cgo, keeping the same interface. Check the feature gaps named earlier: peer and piece info, blocklists, rate limits, UPnP, IPv6, web seeds, BitTorrent v2 and hybrid.
2. **Scaffold the Go project (second).** Module layout, one static binary with CLI subcommands (`serve`, `add`, `ls`, `doctor`, `config`, `providers`, and the rest of 5.5 as stubs), the single config file with its precedence rules (5.11), pure-Go SQLite with migrations, the versioned HTTP API with token auth and scopes, the Source and Destination interfaces and registry (5.12), the folder API with path safety (5.4), CI, and GoReleaser. Wire the engine chosen in step 1 behind its interface.
3. **Then the milestones in section 13:** core downloads and streaming, torrent management, integrations (yt-dlp, rclone, HTTPS, installer, updates), Android app.

Rules for the handoff:

- This file is the spec. Where it says "verify", check before relying on it and record the answer here.
- Keep it simple: prefer proven pure-Go components, and keep every external tool optional and detected at startup.
- Record any change to a decision in section 17.


## 19. Verification notes

Answers to "verify" items, checked 2026-10-10 against the projects' own pages. Re-check the licence of each dependency at the version that gets pinned.

| Item | Answer | Consequence |
|---|---|---|
| anacrolix/torrent | MPL-2.0, needs Go 1.25+. The README lists BitTorrent v2, web seeds, protocol encryption, SOCKS5 for outgoing peer connections, and readers with seeking and readahead. Its config has `IPBlocklist`, global `UploadRateLimiter`/`DownloadRateLimiter`, `NoDefaultPortForwarding` (UPnP), `DisableIPv6`, `DisableUTP`, `DisablePEX`, `NoDHT`, `HeaderObfuscationPolicy`, and `HTTPDialContext`/`TrackerDialContext`. | Per-torrent rate limits are not built in: our layer. uTP pulls in cgo `go-libutp`, so the spike must check what `CGO_ENABLED=0` gives. Hybrid torrents are not mentioned: spike item. Spike result (docs/engine-spike.md): hybrid works, v2-only panics, no LSD in v1.61, uTP is pure Go without cgo. |
| anacrolix piece-completion storage | The sqlite path uses a cgo-backed crawshaw library | Use the bolt completion (pure Go) |
| Archive library (open question 11) | mholt/archives: MIT, pure Go, reads zip, tar (+ compressed), 7z and rar | Used (v0.1.5). Checked 2026-10-11: rar via nwaples/rardecode v2.2.0 is BSD-2-Clause, 7z via bodgit/sevenzip v1.6.1 is BSD-3-Clause; both fine |
| Update library (open question 9) | creativeprojects/go-selfupdate: MIT, GitHub/Gitea/GitLab/HTTP sources, SHA-256 checksum file, ECDSA `.sig`, GoReleaser naming, rollback on a failed replace | Use it |
| Private repo and releases | An end user cannot download release assets or images from a private repo | Before M3: a public releases-only repo (or make the main repo public) plus a public GHCR package |
| Google Drive sign-in on a headless server (open question 12) | Google's device-code flow supports only the `drive.file` and `drive.appdata` Drive scopes. `drive.file` is non-sensitive, while full `drive` is restricted and needs a paid security assessment. Clients in "Testing" status get refresh tokens that expire after 7 days. | Device flow (`golang.org/x/oauth2`) with `drive.file`, token written into the rclone config. Verify end to end in M3 |
| SSRF through helper tools | Our dialer guard covers only our own Go connections | yt-dlp goes through `--proxy` to an in-process guarded CONNECT proxy; anacrolix `HTTPDialContext`/`TrackerDialContext` use the guarded dialer |
| Termux with a static binary | Go's pure resolver and cert lookup expect `/etc/resolv.conf` and `/etc/ssl`, which Android lacks | When `$PREFIX` is set, use `$PREFIX/etc/resolv.conf` and `$PREFIX/etc/tls/cert.pem` |
| CertMagic DNS providers (open question 10) | `libdns/cloudflare` and `libdns/duckdns` exist | Both usable for DNS-01 |
| Pure-Go SQLite | modernc.org/sqlite (BSD-3) | Use it, with an embedded migration runner on `PRAGMA user_version` |
| Path safety | Go 1.24+ `os.Root` confines file operations to a directory, including symlink escapes | Use it for the folder API and archive extraction |
