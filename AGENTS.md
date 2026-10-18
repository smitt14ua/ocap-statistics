# AGENTS.md

Instructions for AI coding agents (and humans) working on this repository. Read this first; it is meant to save you from exploring the code to find where things are.

**TL;DR:** run `make check` before you say you are done (Windows: `CGO_ENABLED=1 make check`). Frontend work: read `web/AGENTS.md` first. UI text only through `web/static/i18n/*.js` (7 languages). Before changing how something fundamental works, read `docs/decisions.md`. Keep the repo community-neutral. Keep the guides true — `docs_test.go` fails when they mention files, make targets or identifiers that no longer exist.

## Project in one paragraph

OCAP Statistics ingests recordings from an [OCAP2](https://github.com/OCAP2/OCAP) web server (Arma 3 replays), computes player, squad and rotation statistics and serves them through a REST API, a web page and an admin panel. Its reason to exist is to take load off OCAP: each recording is processed **once** into a compact digest stored in SQLite, and every request is answered from that database. Go 1.26, no cgo (pure-Go SQLite), no frontend build step.

The project is open source and community-neutral. Never put community-specific values (OCAP URLs, tags, squad names, rotation sides, logos, titles) into code, defaults, docs, tests or examples — they belong in deployment configuration.

## Commands

| Task | Command |
|---|---|
| **All checks** | `make check` → gofmt, vet, staticcheck, race tests, `make check-web` |
| Web checks | `make check-web` → `node --check` on scripts + html-validate (`.htmlvalidate.json`) |
| Build | `make build` (or `go build ./cmd/ocap-stats`) |
| Tests | `make test` → `go test -race ./...` (race needs cgo; on Windows `CGO_ENABLED=1`) |
| Quick tests | `go test ./...` |
| Lint | `make lint` → `go vet ./...` + staticcheck |
| Vulnerabilities | `make vuln` → govulncheck |
| Run once | `OCAPSTATS_OCAP_URL=https://ocap.example.com go run ./cmd/ocap-stats -once` |
| Run server | `OCAPSTATS_OCAP_URL=... OCAPSTATS_ADMIN_PASSWORD=... go run ./cmd/ocap-stats` (site on :5080) |
| One package | `go test ./internal/report/` (fast feedback while iterating) |

Definition of done: `make check` green; UI changes also checked in a browser with axe-core (WCAG 2.2 AA, 0 violations) — the pages' CSP blocks CDN scripts, so copy `axe.min.js` into `web/static` for a throwaway build and delete it before committing; README/OpenAPI/AGENTS.md updated when behaviour, API or structure changed.

Guard tests that encode project rules (prefer adding a test over adding prose here):

| Test | Enforces |
|---|---|
| `web/web_test.go` | all 7 languages have identical keys; every page loads every language file; every key used in markup/scripts exists; every formula variable has a `var.*` help text |
| `docs_test.go` | files, `make` targets and `pkg.Identifier`s mentioned in this file and `web/AGENTS.md` exist |
| `internal/api/server_test.go` `TestOpenAPIMatchesRoutes` | every documented path is served; no admin endpoint is documented |
| `internal/api/server_test.go` `TestStaticHandler` | CSP and framing headers, gzip/ETag, no path traversal |
| `internal/stats/compute_test.go` | counting rules on the fixture `internal/stats/testdata/recording.json` |

CI (`.github/workflows/ci.yml`) runs the same checks and publishes `ghcr.io/<owner>/ocap-statistics` for linux/amd64 and linux/arm64 on pushes to `main`. It compiles the binaries outside Docker (with a cached Go build) and builds the image with `--target prebuilt` of the `Dockerfile`; the default target still builds from source.

## Repository map

```
cmd/ocap-stats/main.go        entry point: config, logging, HTTP server timeouts, graceful shutdown
internal/config/config.go     Config struct, defaults, OCAPSTATS_* env overrides (env wins over JSON)
internal/ocap/client.go       OCAP client: ListOperations, OpenMission (data dir first, HTTP fallback, ErrNotFound)
internal/digest/digest.go     streaming JSON recording parser → Digest (entities, kills, hits, end, vehicle crews)
internal/stats/compute.go     per-mission rules: Compute, PlayerRow, KillRow, nickname Rules/ParseName
internal/store/store.go       SQLite schema, migrate(), missions/digests/rows/kills queries, Filter
internal/store/users.go       admin users and sessions
internal/report/report.go     aggregation over a period: Player, Squad, Totals, achievements, rotation, sorting
internal/report/columns.go    custom columns: Column, Variables, NormalizeColumns, ApplyColumns, Preview
internal/formula/formula.go   formula language: lexer, parser, evaluator (no reflection, no eval)
internal/settings/settings.go runtime settings: config defaults + admin overrides stored in meta table
internal/syncer/syncer.go     task loop: sync / recompute / resync, retries, missing recordings
internal/auth/                PBKDF2 password hashing, session tokens, default admin from env
internal/api/server.go        Server, routes, error helpers, cached handler, gzip/ETag, middleware
internal/api/stats.go         public /api/v1 handlers, filters, CSV
internal/api/admin.go         /api/admin handlers: login, settings, previews, tasks, users
internal/api/cache.go         response cache + per-filter aggregation cache (singleflight)
internal/api/static.go        embedded static files, ETag, gzip, per-page CSP
internal/api/openapi.json     public API spec (admin API intentionally not documented)
web/AGENTS.md                 frontend guide: files, CSP/DOM/i18n/a11y rules, adding a language, checks
web/static/index.html, app.js site (stats table, achievements, modals, export, screenshots)
web/static/admin/             admin panel (index.html, admin.js)
web/static/i18n.js            language detection/switching, t(); strings live in i18n/<lang>.js
web/static/i18n/              UI strings, one file per language (en, be, cs, de, kk, pl, uk)
web/static/xlsx.js            dependency-free XLSX writer
web/static/app.css            shared design tokens and components
docs/decisions.md             why things are built the way they are (SQLite, digests, CSP, ...)
.claude/hooks/check-edit.mjs  Claude Code hook: gofmt / node --check after every edit
```

## How it works

1. **Sync** (`syncer.sync`): list operations from OCAP (settings: tags, since), upsert metadata, delete missions that are no longer listed, then fetch pending ones newest first.
2. **Ingest** (`syncer.processOne`): read `<dataDir>/<file>.json.gz` or download it, `digest.Parse` → `digest.Encode` (gzip JSON, ~10 KB) → `stats.Compute` → `store.SaveResult` (rows in `mission_players` and `kills`). 404 everywhere → status `missing`, other errors → retries with back-off.
3. **Serve**: `api.Server.report(filter)` loads rows for a period, `report.Build` aggregates, `ApplyColumns` adds custom columns; cached per data generation.
4. **Versions**: bump `digest.Version` when extraction changes (stored recordings are fetched again); bump `stats.ComputeVersion` when counting rules change (recomputed from stored digests, no network). Schema changes need `store.migrate` for existing databases.

## Recipes

**Add a per-player statistic** (example: kill assists was added this way)
1. `stats.PlayerRow` field + counting in `stats.Compute` (+ test in `compute_test.go`, fixture in `internal/stats/testdata/recording.json`); bump `ComputeVersion`. If it needs new recording data, extend `digest` and bump `digest.Version`.
2. Store: column in `schema`, `migrate()` for old databases, `writeResult` insert, `scanRows` + the `SELECT` lists in `Rows`/`MissionRows`.
3. Report: fields on `Player`, `Squad`, maybe `Totals`; sums in `Build` and `buildSquads`; sort keys in `SortPlayers`/`SortSquads` and the allowed lists `report.PlayerSortFields`/`report.SquadSortFields`; formula variable in `report.Variables` + `playerVars`/`squadVars`, the variable list in `admin.js` `renderVarList` and a `var.*` help string (the web test enforces it); the default sort choices in `admin.js` `renderSortOptions`.
4. API: `missionRow`/`missionPlayer` in `stats.go`, CSV headers/rows, `openapi.json` schemas.
5. UI: `<th>` in `index.html` (+ summary row colspan), `row()`, `headerLabels()`, Excel rows/widths, detail tiles and tables in `app.js`; strings in all `web/static/i18n/*.js` files; README "How statistics are computed".

**Add a UI string**: add the key to every file in `web/static/i18n/` (en, be, cs, de, kk, pl, uk — key sets must be identical), use `data-i18n` / `data-i18n-placeholder` / `data-i18n-title` / `data-i18n-aria` in markup or `t('key', {var})` in JS. More in `web/AGENTS.md`.

**Add a setting editable in the admin panel**: field in `settings.Settings` (+ `FromConfig` default from `config`, validation in `Normalize`), decide in `Manager.apply` whether it needs a recompute or sync, form field + `fillForms`/`collectSettings` in `admin.js`, strings in `web/static/i18n/*.js`.

**Add a public endpoint**: handler in `stats.go` returning `(response, error)` wrapped with `s.cached(...)` in `server.go`; add query parameters that affect the result to `cacheParams`; document it in `openapi.json`; add tests in `api_test.go`.

## Conventions and constraints

- **Go**: standard library first; the only dependency is `modernc.org/sqlite`. Errors wrap with `%w`; API errors use `apiError`/`withCode` and are written by `Server.fail`. Comment exported identifiers and non-obvious decisions, not the obvious.
- **Performance**: never aggregate the database per request outside `Server.report` (it is cached and deduplicated). Keep the digest small; don't store positions.
- **Security**: strict CSP (no inline scripts, no `eval`, no new script origins; third-party scripts only on demand with SRI); build DOM with `el()` + `textContent`, never `innerHTML` with data (details in `web/AGENTS.md`); validate settings that reach HTML/CSS; trust `X-Forwarded-*` only with `trustProxy`; admin API stays same-origin with the `X-Requested-With` CSRF header. See `SECURITY.md`.
- **Accessibility**: real buttons/links for interactive elements, `aria-sort` on sortable headers, WAI-ARIA tabs, modals make `.page` inert, text contrast ≥ 4.5:1 using tokens in `app.css`, user-defined colours never used for text.
- **Language**: code, comments, docs, commit messages and API messages in technical English. Commits: imperative subject, wrapped body with what and why, no AI attribution lines.

## Working efficiently here

- Start from the repository map and the recipes; open the named file/function instead of searching the whole tree. `internal/api/stats.go` and `web/static/app.js` are the usual entry points for "show X on the site".
- Translations: read only `web/static/i18n/en.js` to find keys; add a key to the other four files with targeted edits, don't read them whole.
- Claude Code runs `.claude/hooks/check-edit.mjs` after every edit: Go files are gofmt'ed in place (re-read before editing the same region again), and a Go or JS syntax error is reported back immediately. Other agents: run `gofmt -w` and `node --check` yourself.
- `report`, `store`, `settings` and `syncer` are tested through `internal/api` integration tests (`setup()` builds a fake OCAP, a temp database and the full handler); add cases there rather than mocking.
- For UI work, build once, run with `OCAPSTATS_SYNC_ENABLED=false` against a copied database, and drive the page in a browser; the API is fastest to check with curl.
- When you learn something non-obvious about this codebase, add it under Gotchas (one line) in the same change.

## Gotchas

- `http.ServeMux`: a method-less pattern (`/api/`) conflicts with `GET /` — the static handler is registered on `/` and rejects non-GET itself. Tests build the server with the embedded site, so a conflict panics in `go test`.
- OCAP lists recordings by tag with `LIKE`; the syncer keeps exact (case-insensitive) matches only.
- `killed` events come in two layouts (`[f, "killed", victim, [killer, weapon], dist]` and `[f, "killed", source, target, weapon, dist]`); `causedById` 0 is a real entity id.
- OCAP writes the crew of a destroyed vehicle, drownings and falls as suicides (killer = victim), without a killer or as killed by a wreck (a vehicle entity); `stats.Compute` credits such deaths to the enemy who damaged the victim last (`KillRow.Cause`).
- A vehicle weapon kill is attributed to the gunner (a unit), not the vehicle; crews come from vehicle positions `[pos, dir, alive, [crew ids], [from, to]]`.
- Saved admin settings override config/env until reset; tests that change config must account for that.
- The default sort (`settings.Sort`) may point to a custom column (`custom.<id>`); `SortOrder.normalize` falls back to kills when that column is deleted, and `app.js` `defaultSort` does the same on the client.
- `sync.since` / the admin "process recordings from" date also removes older recordings from the database (it mirrors OCAP filtered by the settings).
- In the web scripts, `el()` flattens its children one level only: build nested lists with `flatMap`, otherwise an inner array is rendered as "[object HTMLSpanElement]".
- `Server.report` hides players and squads below the "minimum missions" setting from `Players`/`Squads`; look a player or squad up with `FindPlayer`/`FindSquad`, which also see hidden ones.
