# Handoff prompt

Paste this into a new agent session started in the repo root
(`C:\Users\sadat\Documents\Web projects\download_me_not`):

---

You are continuing work on `download_me_not`, a self-hosted download server in Go (web downloads,
video sites via yt-dlp, torrents, folder/streaming API, Google Drive copy, web panel, CLI). The
repo is public: https://github.com/sadatdaniel/download_me_not

1. Read `AGENTS.md` (rules, commands, layout, releasing). Follow it strictly, especially:
   smallest complete change, pure Go / no cgo, offline and legal testing only (rule 7: never public
   torrent swarms, trackers or DHT, never third-party media; isolated Docker tests), never touch
   Docker resources this project didn't create, never commit secrets or personal data.
2. Read `docs/STATUS.md`: "Current state", "Next action" and "Open" tell you exactly where the last
   session stopped and what comes next. Read `docs/architecture.md` before designing anything.
3. Read only the parts of `docs/scope.md` (the spec; §17 decisions, §19 verified facts) and
   `docs/deploy.md` that the next action needs. If the spec and reality disagree, say so and record
   the decision in scope §17.
4. Run `git status` and `git log --oneline -10`. Then confirm the toolchain:
   `docker compose -f compose.dev.yaml run --rm test go test -p 2 ./cmd/... ./internal/...`
5. Work on the first item of "Next action". Keep changes small and tested (offline tests in the
   dev container; `test/sim/run.sh` for install changes; `test/acme/run.sh` for TLS changes).
6. Release when something user-facing changed: tag `vX.Y.Z` and push; the release workflow must be
   green (release, update-e2e, image).
7. Before you stop, even mid-task: update `docs/STATUS.md` (state, next action, open items, a
   session-log line), commit, push. Keep chat replies short; put details in the docs.

Context about the owner: deploys on an Oracle Cloud ARM (arm64) Ubuntu VM; works on Windows with
Docker Desktop (no Go on the host; Flutter SDK is installed for the later app work). The Android
app (Flutter) is paused until the server is confirmed working in real use.

---
