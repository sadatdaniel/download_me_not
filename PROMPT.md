# Handoff prompt

Paste this into a new agent session started in the repo root
(`C:\Users\sadat\Documents\Web projects\download_me_not`):

---

You are continuing work on **GetALoad** (technical name `getaload`; renamed from download_me_not on 2026-10-14), a
self-hosted download server in Go (web downloads, video sites via yt-dlp, torrents, folder/streaming API, Google
Drive per user, Stremio add-ons, multi-user with name + PIN) and its Flutter client (Android, web, later Linux).
Server repo (public): https://github.com/sadatdaniel/getaload, local folder `download_me_not`. App repo (public):
https://github.com/sadatdaniel/getaload_app, local folder `..\download_me_not_app`.

1. Read `AGENTS.md` (rules, commands, layout, releasing) and follow it strictly: smallest complete change, pure Go / no
   cgo, offline and legal testing only (rule 7), never touch Docker resources this project didn't create, never commit
   or print secrets, tokens, keys, pairing links or personal data (gitleaks with our token rule runs in CI; run it
   yourself before making anything public). Commit as `sadatdaniel <13930943+sadatdaniel@users.noreply.github.com>`.
2. Read `docs/STATUS.md`: "Current state", "On main, not released yet" and "Next action" say exactly where the last
   session stopped. Read `docs/architecture.md` before designing, and `docs/scope.md` §17 (decisions) as needed.
3. Read the app repo's `README.md` and `docs/DEVELOPMENT.md` before touching the app. The app is built with the
   Flutter SDK on the Windows host (`C:\dev\flutter`, 3.47.5), never in Docker. Release APKs are built and signed by
   the app repo's CI (tag `vX.Y.Z`); local builds use the debug key. **Local Android builds on this PC** cannot reach
   dl.google.com (the owner's network stalls on it): build with a temporary local Maven repo for androidx.webkit 1.15.0
   (see STATUS) and restore `android/build.gradle.kts` afterwards. The owner's phone is reachable over adb wireless
   debugging when they switch it on (`adb mdns services`, then `adb -s <device> install -r`).
4. Run `git status` and `git log --oneline -10` in both repos. Confirm the toolchain:
   `docker compose -f compose.dev.yaml run --rm test go test -p 2 ./cmd/... ./internal/...` and, in the app repo,
   `flutter analyze` and `flutter test`.
5. Work through STATUS "Next action" in order. Step 1 is testing the full web client in the browser (preview config
   `webui-users`, port 18090), then releasing app v0.2.1 before server v0.2.1.
6. Release only after the owner approves each release; the release workflows must be green.
7. Before you stop, even mid-task: update `docs/STATUS.md` (state, next action, open items, a session-log line), and
   commit and push both repos. Keep chat replies short and plain; put details in the docs.

Context about the owner: runs the server on an Oracle Cloud ARM VM (1 core, 6 GB), reached by SSH tunnel on
127.0.0.1:8080 and by public IP on port 8080; uses the Android app on their phone; works on Windows with Docker
Desktop (no Go on the host; Flutter SDK installed). Wants product-voice docs (like Seedr/TorBox, not code
descriptions), plain wording, no jumping ahead without agreement on bigger decisions, and to be asked before
releases, before anything touching the real server or the public internet, and before deleting anything. Likes a
GNOME/macOS-style, light, website-like UI. Target browsers: latest Chrome and Firefox only.

---
