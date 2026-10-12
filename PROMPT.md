# Handoff prompt

Paste this into a new agent session started in the repo root
(`C:\Users\sadat\Documents\Web projects\download_me_not`):

---

You are continuing work on `download_me_not`, a self-hosted download server in Go (web downloads, video sites
via yt-dlp, torrents, folder/streaming API, Google Drive copy, Stremio add-ons, web panel, CLI) and its Flutter
app. Server repo (public): https://github.com/sadatdaniel/download_me_not. App repo (private):
sadatdaniel/download_me_not_app, checked out next to this one in `..\download_me_not_app`.

1. Read `AGENTS.md` (rules, commands, layout, releasing) and follow it strictly: smallest complete change,
   pure Go / no cgo, offline and legal testing only (rule 7: no public torrent swarms, trackers or DHT, no
   third-party media or piracy add-ons; isolated or local tests with our own content; the owner granted one
   exception: the Creative Commons "Sintel" torrent on the owner's own server, scope §17), never touch Docker
   resources this project didn't create, never commit secrets, tokens, pairing links or personal data.
2. Read `docs/STATUS.md`: "Current state", "Next action" and "Open" say exactly where the last session stopped.
   **Multi-user is half built**: store, engine and job manager are done and tested; the API, CLI, installer and
   app parts are next, with the agreed design written out in Next action 1 and 2. Read `docs/architecture.md`
   before designing, and the parts of `docs/scope.md` (§17 decisions, §19 verified facts) you need.
3. Read the app repo's `README.md` before touching the app. The app is built with the Flutter SDK on the Windows
   host (`C:\dev\flutter`), never in Docker. It is tested on Flutter web via `.claude/launch.json` config
   `app-web` (a throwaway server in Docker whose config persists in volume `dmn-preview-config`; the token is in
   `docker logs dmn-appweb`). It has never run on an Android device.
4. Run `git status` and `git log --oneline -10` in both repos. Confirm the toolchain:
   `docker compose -f compose.dev.yaml run --rm test go test -p 2 ./cmd/... ./internal/...` and, in the app
   repo, `flutter analyze` and `flutter test`.
5. Work on Next action 1 (multi-user server API: name + PIN sign-in that returns a per-device token, per-user
   folders/jobs/links everywhere, user management, CLI, installer asking for the first user), then 2 (the app).
   Keep changes small and tested; for multi-user, test that two users never see each other's jobs, files,
   links or progress.
6. Release when something user-facing changed, but **only after the owner approves each release**: tag
   `vX.Y.Z` and push; the release workflow must be green (release, update-e2e, image).
7. Before you stop, even mid-task: update `docs/STATUS.md` (state, next action, open items, a session-log line),
   commit and push both repos. Keep chat replies short and plain; put details in the docs.

Context about the owner: deploys on an Oracle Cloud ARM (arm64) Ubuntu VM, reached through an SSH tunnel on
127.0.0.1:8080 from the Windows PC; happy to wipe and reinstall that server for multi-user. Works on Windows
with Docker Desktop (no Go on the host; Flutter SDK installed). Prefers to be asked before releases and before
anything that touches the real server or the public internet; likes GNOME/macOS-style, light, website-like UI.

---
