# AGENTS.md

Self-hosted download server (web links, video sites via yt-dlp, torrents) with a folder/streaming API, a CLI, and later an Android app. One static Go binary. Single user.

- **Spec:** [docs/scope.md](docs/scope.md) is the source of truth. Section 17 is the decision log, and section 19 holds the verified answers.
- **Architecture:** [docs/architecture.md](docs/architecture.md) is the breakage map and self-healing rules. Read it before designing anything new.
- **Progress:** [docs/STATUS.md](docs/STATUS.md) shows where we are and what comes next.
- **Handoff:** [PROMPT.md](PROMPT.md) is the prompt to start a new agent session.

## Rules

1. Smallest complete change. Prefer the stdlib, then an existing dependency, then a new one. Never add a dependency for a few lines.
2. Pure Go, `CGO_ENABLED=0`. The binary must stay static.
3. Licences: MIT/BSD/Apache/ISC/MPL/Unlicense are fine. Never embed or link GPL/AGPL code. External GPL tools may only be run as separate programs (for example aria2c as a benchmark yardstick, never shipped).
4. Every external tool (yt-dlp, deno, rclone, ffmpeg) is optional. Detect it at startup and switch the feature off cleanly when it's missing.
5. Security: SSRF guard on every user-supplied URL (including those that yt-dlp and torrent web seeds or trackers fetch), `os.Root` for every file operation, and tokens default to the read scope.
6. **Never commit** secrets, tokens, cookies, `.env` files, real torrents or downloaded media, or any personal data (phone numbers, emails other than git authorship, IPs of real machines). The repo will become public later with its full history. gitleaks runs in CI.
7. **Legal testing only. Torrent uploads of protected content are pursued in many countries.** Never join a public torrent swarm, never use public trackers, DHT, PEX, LSD or UPnP, and never fetch third-party torrents or media from this machine. Torrent tests run only in the isolated Docker swarm (`internal: true` network, no internet), using content we generate ourselves (random bytes, an ffmpeg `testsrc` video) and `.torrent` files we create locally, including v2 and hybrid ones. The same goes for HTTP and yt-dlp tests: local test servers serving our own files. If a test seems to need the public internet, stop and ask the user.
8. A change to a decision goes in scope §17. A "verify" item that has been answered goes in scope §19.
9. A shortcut with a known limit gets a `// ponytail: <limit, when to upgrade>` comment.
10. The CLI name `dmn` is a **placeholder**. Keep it in one place (`cmd/dmn`, `.goreleaser.yaml`) so the rename is a single commit.

## Dev environment (Docker only, nothing installed on the host)

```bash
docker compose -f compose.dev.yaml run --rm test go test ./...   # offline: network_mode none
docker compose -f compose.dev.yaml run --rm go go mod tidy       # online: module downloads only
docker compose -f compose.dev.yaml run --rm test go vet ./...
```

- Compose project `dmn-dev`, volumes `dmn-gomod` and `dmn-gocache`.
- Docker resources this project created (the only ones you may remove): volumes `dmn-gomod` and `dmn-gocache`; images `golang:1.26`, `alpine:3.22`, `goreleaser/goreleaser:latest` and `dmn:local`, plus `dmn-dev:latest` (Dockerfile.dev: Go, ffmpeg, aria2c, yt-dlp; rebuild with `docker compose -f compose.dev.yaml build go`). `ubuntu:24.04` existed before; use it but never remove it.
- Server image: `docker build -t dmn:local .`, then see `compose.yaml`. The API contract is `api/openapi.json`; regenerate it with `UPDATE_OPENAPI=1` (see `internal/api`).
- Don't call `python` on the host (it's a Store stub that hangs). Use Docker or perl.
- The host also runs unrelated containers (`a50-*`, `oci-*`, omniroute). **Never touch, prune or delete Docker resources this project didn't create.** No `docker system prune`, no `docker volume prune`.
- The host is Windows. Line endings are forced to LF by `.gitattributes`.

## Layout

```
docs/              scope.md (spec + decisions §17 + verified facts §19), STATUS.md (progress, next step),
                   architecture.md (breakage map, self-healing rules), engine-spike.md (torrent evidence),
                   benchmark.md (downloader vs aria2c)
cmd/dmn/           the one binary: `serve` + CLI (cobra); unbuilt commands are stubs naming their milestone
internal/api       huma /v1 API, token scopes read<write<admin, SSE, signed /v1/raw streaming
internal/config    koanf: flags > env DMN_* > file > defaults; atomic Set
internal/store     SQLite (modernc), embedded migrations on user_version, backup before migrating
internal/fsapi     folder API on os.Root (blocks ../symlink escape), Range, HMAC links
internal/engine    torrent Engine interface + anacrolix implementation (spike lessons inside)
internal/httpdl    IDM-style downloader: speed-aware work stealing, slow-connection watchdog, resume, If-Range
internal/jobs      crash-only job manager: DB holds desired state, reconcile loop runs runners (recover per job)
internal/selftest  offline end-to-end checks behind `dmn doctor --selftest`
internal/netguard  SSRF-guarded dialer/client + guarded proxy for yt-dlp
internal/provider  built-in sources/destinations, JSON Schema settings, live toggles
internal/tools     helper tool detection (doctor)
api/openapi.json   committed API contract (snapshot test fails on any change)
spike/engine/      step-1 spike, kept as evidence; not imported
spike/bench/       httpdl vs aria2c benchmark (docs/benchmark.md)
```

## How to approach a problem

1. **Conventional first.** Find how mature projects solve it (qBittorrent, aria2, Transmission, rclone, Caddy, Syncthing, Kubernetes) and copy the pattern, not the code (licences, rule 3).
2. **KISS and reuse.** Stdlib, then an existing dependency, then a small proven library. Write our own only when nothing fits; the HTTP downloader is our own because stream mode needs it.
3. **Avoid pitfalls up front.** Before building, add the failure modes to `docs/architecture.md` (breakage map) with how they are contained, detected and repaired. Things that break often (yt-dlp, Google auth, upstream bugs) sit behind an interface, get a self-test, and can fail without taking the server down.
4. **Verify, don't assume.** Check library APIs in the module source (`go list -m -f {{.Dir}} <module>`) and prove behaviour with an offline test. The spike caught three wrong assumptions this way.
5. **Record the why.** Decisions go in scope §17 with the reason; facts you verified go in §19.
6. **No C dependencies.** libtorrent (C++ via cgo) was rejected because it breaks the static build and portability. It's a last-resort fallback behind `internal/engine` only.

## Definition of done (every session)

- `gofmt -l .` prints nothing, `go vet ./...` is clean, and tests pass in the dev container (CI checks all three).
- `docs/STATUS.md` is updated: what's done, the next action, any blockers, and a line in the session log.
- Commit with a clear message and push to `origin main` (or to a branch with a PR for larger changes).
