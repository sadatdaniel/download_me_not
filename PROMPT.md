# Handoff prompt

Paste this into a new agent session started in the repo root
(`C:\Users\sadat\Documents\Web projects\download_me_not`):

---

You are continuing work on **GetALoad** (technical name `getaload`), a self-hosted download server in Go (web
downloads, video sites via yt-dlp, torrents, folder/streaming API, Google Drive per user, Stremio add-ons, family
accounts with name + PIN, server control by admin key) and its Flutter client (Android app, and the same app as the
web client built into the server; Linux later). Server repo (public): https://github.com/sadatdaniel/getaload, local
folder `download_me_not`. App repo (public): https://github.com/sadatdaniel/getaload_app, local folder
`..\download_me_not_app`. Latest releases: server v0.2.7, app v0.2.5.

1. Read `AGENTS.md` (rules, commands, layout, releasing) and follow it strictly: smallest complete change, pure Go / no
   cgo, offline and legal testing only (rule 7: never run torrents or third-party downloads on this PC), never touch
   Docker resources this project didn't create, never commit or print secrets, tokens, keys or personal data (gitleaks
   runs in CI). Commit as `sadatdaniel <13930943+sadatdaniel@users.noreply.github.com>`.
2. Read `docs/STATUS.md` first: "Current state", "Next action", "Open" and especially **"Working notes for the next
   session"** (how to run tests without the hanging compose prompt, the web preview, the flaky browser pane, how to
   look at the owner's server only with their go-ahead, the release order). Read `docs/architecture.md` before
   designing, and `docs/scope.md` §17 (decisions) and §19 (verified facts) as needed.
3. Read the app repo's `README.md` and `docs/DEVELOPMENT.md` before touching the app. The app is built with the
   Flutter SDK on the Windows host (`C:\dev\flutter`, 3.47.5), never in Docker. Release APKs are built and signed by
   the app repo's CI (tag `vX.Y.Z`). The owner's phone and tablet run the release-signed app and update themselves
   (Settings, About this app); adb over wireless debugging works when the owner switches it on, but isn't needed for
   updates.
4. Run `git status` and `git log --oneline -10` in both repos. Confirm the toolchain with the plain `docker run`
   command from STATUS "Working notes" (Go tests, vet, gofmt) and, in the app repo, `flutter analyze` and
   `flutter test`.
5. Work through STATUS "Next action" in order. Step 1 is the README drafts in `docs/drafts/` (the owner is editing
   them; ask before changing their wording, and settle the WatchHub point).
6. Release only after the owner approves each release; app first, then `internal/webui/APP_VERSION` and the server;
   the release workflows must be green.
7. Before you stop, even mid-task: update `docs/STATUS.md` (state, next action, open items, a session-log line),
   record decisions in scope §17 and verified facts in §19, and commit and push both repos. Keep chat replies short
   and plain; put details in the docs.

Context about the owner: runs the server on an Oracle Cloud ARM VM (1 core, 6 GB), reached by SSH tunnel on
127.0.0.1:8080 and by public IP on port 8080; uses the Android app on their phone and a tablet, and the web client in
a browser; works on Windows with Docker Desktop (no Go on the host; Flutter SDK installed). Sees SSH access as the
real authority over the server. Wants product-voice docs (like Seedr/TorBox, not code descriptions), plain wording,
no jumping ahead without agreement on bigger decisions, and to be asked before releases, before anything touching
the real server or the public internet, and before deleting anything. Likes a GNOME/macOS-style, light,
website-like UI (GNOME Settings is the model for settings). Target browsers: latest Chrome and Firefox only.

---
