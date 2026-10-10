# AGENTS.md

Self-hosted download server (web links, video sites via yt-dlp, torrents) with a folder/streaming API, a CLI, and later an Android app. One static Go binary. Single user.

- **Spec:** [docs/scope.md](docs/scope.md) is the source of truth. Section 17 is the decision log, and section 19 holds the verified answers.
- **Progress:** [docs/STATUS.md](docs/STATUS.md) shows where we are and what comes next.
- **Handoff:** [PROMPT.md](PROMPT.md) is the prompt to start a new agent session.

## Rules

1. Smallest complete change. Prefer the stdlib, then an existing dependency, then a new one. Never add a dependency for a few lines.
2. Pure Go, `CGO_ENABLED=0`. The binary must stay static.
3. Licences: MIT/BSD/Apache/ISC/MPL/Unlicense are fine. Never embed or link GPL/AGPL code. External GPL tools may only be run as separate programs (for example aria2c as a benchmark yardstick, never shipped).
4. Every external tool (yt-dlp, deno, rclone, ffmpeg) is optional. Detect it at startup and switch the feature off cleanly when it's missing.
5. Security: SSRF guard on every user-supplied URL (including those that yt-dlp and torrent web seeds or trackers fetch), `os.Root` for every file operation, and tokens default to the read scope.
6. **Never commit** secrets, tokens, cookies, `.env` files, real torrents or downloaded media, or any personal data (phone numbers, emails other than git authorship, IPs of real machines). The repo will become public later with its full history. gitleaks runs in CI.
7. A change to a decision goes in scope §17. A "verify" item that has been answered goes in scope §19.
8. A shortcut with a known limit gets a `// ponytail: <limit, when to upgrade>` comment.
9. The CLI name `dmn` is a **placeholder**. Keep it in one place (`cmd/dmn`, `.goreleaser.yaml`) so the rename is a single commit.

## Dev environment (Docker only, nothing installed on the host)

```bash
docker compose -f compose.dev.yaml run --rm go go test ./...
docker compose -f compose.dev.yaml run --rm go go build ./...
docker compose -f compose.dev.yaml run --rm go go vet ./...
```

- Compose project `dmn-dev`, volumes `dmn-gomod` and `dmn-gocache`.
- The host also runs unrelated containers (`a50-*`, `oci-*`, omniroute). **Never touch, prune or delete Docker resources this project didn't create.** No `docker system prune`, no `docker volume prune`.
- The host is Windows. Line endings are forced to LF by `.gitattributes`.

## Layout

```
docs/          scope.md (spec), STATUS.md (progress), engine-spike.md (step 1 result)
spike/engine/  throwaway torrent-engine spike (step 1)
cmd/dmn/       the binary: `serve` + CLI subcommands          (step 2)
internal/      config, store, api, fsapi, provider, engine, netguard, tools   (step 2)
```

## Definition of done (every session)

- Build and tests pass in the dev container.
- `docs/STATUS.md` is updated: what's done, the next action, any blockers, and a line in the session log.
- Commit with a clear message and push to `origin main` (or to a branch with a PR for larger changes).
