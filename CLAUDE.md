# CLAUDE.md

All project guidance lives in AGENTS.md (shared with other coding agents):

@AGENTS.md

## Claude Code specifics

- Run `make check` (Windows: `CGO_ENABLED=1 make check`) before reporting work as done; it and the other build/test commands are pre-approved in `.claude/settings.json`.
- Prefer the dedicated Read/Edit/Grep tools; open the files named in AGENTS.md instead of exploring the tree.
- For UI changes, verify in the browser (Claude in Chrome) and run axe-core as described in AGENTS.md before reporting the work as done.
- Do not add attribution lines to commit messages.
