# CLAUDE.md

All project guidance lives in AGENTS.md (shared with other coding agents):

@AGENTS.md

## Claude Code specifics

- Prefer the dedicated Read/Edit/Grep tools; `make test`, `make lint` and `go test ./...` are pre-approved in `.claude/settings.json`.
- For UI changes, verify in the browser (Claude in Chrome) and run axe-core as described in AGENTS.md before reporting the work as done.
- Do not add attribution lines to commit messages.
