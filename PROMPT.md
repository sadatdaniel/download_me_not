# Handoff prompt

Paste this into a new agent session started in the repo root:

---

You are continuing work on `download_me_not`, a self-hosted download server written in Go.

1. Read `AGENTS.md` (rules, commands, layout). Follow it strictly.
2. Read `docs/STATUS.md`. "Current state", "Next action" and "Blockers" tell you where the last session stopped.
3. Read only the sections of `docs/scope.md` that the next action needs. The spec wins over your own assumptions. If the spec is wrong or unclear, say so and record the decision in scope §17 instead of silently diverging.
4. Run `git status` and `git log --oneline -10`. If uncommitted work exists, find out what it is before you change anything.
5. Work on the first unchecked item in STATUS. Keep changes small and tested.
6. Before you stop, even mid-task: update `docs/STATUS.md` (state, next action, blockers, a session-log line), commit, and push. The next agent knows only what is in the repo.

Never delete files, Docker containers, images or volumes that this project didn't create.

---
