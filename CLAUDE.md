# CLAUDE.md

Guidance for Claude Code (and other contributors) working in this repository.

## What this is

OCAP Statistics ingests recordings from an [OCAP2](https://github.com/OCAP2/OCAP) web server (Arma 3 replays), computes player, squad and rotation statistics and serves them through a REST API, a web page and an admin panel. Its purpose is to take load off OCAP: every recording is processed **once**, and all requests are answered from the service's own SQLite database.

The project is open source and community-neutral: no defaults, docs or examples tied to a specific Arma community. Community-specific values (OCAP URL, tags, rotation sides, logo, title) belong in deployment configuration, never in the repository.

## Commands

```bash
make test     # go test -race ./...  (needs cgo; on Windows: CGO_ENABLED=1)
make lint     # go vet + staticcheck
make vuln     # govulncheck
make build    # ./ocap-stats with version ldflags
go run ./cmd/ocap-stats -once   # one sync and exit (needs OCAPSTATS_OCAP_URL)
```

Before committing: `gofmt -l .`, `go vet ./...`, staticcheck, `go test -race ./...`, `node --check` on every file in `web/static`, and for markup changes `npx html-validate@9` with the `html-validate:recommended` + `html-validate:a11y` presets (`no-inline-style` off). Check UI changes in a browser and run axe-core (WCAG 2.2 AA + best practices): the pages' CSP blocks CDN scripts, so serve `axe.min.js` from `web/static` temporarily in a throwaway build and never commit it.

CI (`.github/workflows/ci.yml`) runs vet, race tests, staticcheck and govulncheck, then publishes a multi-arch image (`linux/amd64`, `linux/arm64`) to `ghcr.io/<owner>/ocap-statistics`.

## Architecture

```
cmd/ocap-stats      entry point: config, logging, HTTP server, graceful shutdown
internal/config     JSON config + OCAPSTATS_* environment overrides (env wins)
internal/ocap       OCAP client: operations list, recording from data dir or HTTP (ErrNotFound on 404)
internal/digest     streaming JSON recording parser -> compact Digest (entities, kills, hits, end, vehicle crews)
internal/stats      per-mission counting rules (Compute) and nickname parsing (Rules)
internal/store      SQLite (modernc, pure Go): missions, digests, mission_players, kills, users, sessions, meta
internal/report     aggregation over a period: players, squads, achievements, rotation, custom columns
internal/formula    formula language of custom columns (lexer, parser, evaluator)
internal/settings   runtime settings: config defaults overridden by values saved in the admin panel
internal/syncer     task loop: sync, recompute, resync (one task at a time)
internal/auth       PBKDF2 passwords, session tokens, default admin from the environment
internal/api        REST API, admin API, caches, static files, security headers, OpenAPI spec
web/static          site (index.html, app.js), admin panel (admin/), i18n.js, xlsx.js, app.css
```

### Data flow and versioning

1. The syncer lists operations (filtered by tags/since), upserts metadata and deletes missions no longer listed.
2. Each pending recording is read from `ocap.dataDir` (preferred) or downloaded over HTTP, parsed into a `digest.Digest`, gzipped and stored, then `stats.Compute` writes `mission_players` and `kills` rows.
3. `digest.Version` — bump when extraction changes; stored recordings are fetched again.
   `stats.ComputeVersion` and the nickname rules form the compute key — bump the version when counting rules change; missions are recomputed from stored digests without touching OCAP.
4. Recordings OCAP has no file for get status `missing` and are not retried automatically.
5. Schema changes need a migration in `store.migrate` (existing databases are upgraded in place).

### API layer

- Statistics endpoints go through `Server.cached`: responses are cached per data generation (`store.Generation`, bumped on every change), keyed only by known query parameters (`cacheParams`), gzipped, with weak ETags.
- Aggregations are cached per `store.Filter` in `resultCache`; concurrent identical requests share one computation. Do not aggregate the database per request outside this path.
- Errors are `{"error", "code"}`; the admin UI translates codes (`err.<code>` keys in `i18n.js`).
- The OpenAPI spec (`internal/api/openapi.json`) documents only the public `/api/v1` endpoints; admin endpoints are intentionally not listed. Keep the spec in sync with response shapes.

### Statistics rules (keep README in sync)

- Player = entity with `isPlayer`; entities with the same clean name are merged per mission. Squad = capture group 1 of the nickname regex (default `\[~?(.*?)~?\]\s*`, so recruits `[~X~]` count in squad X); aliases merge spellings.
- Kill: `killed` event whose killer is a player other than the victim; vehicle victims count as destroyed vehicles. Team kill: same side. Death: player victim.
- Kill assist: a non-team kill made by another crew member of the vehicle the player was in (vehicle crews come from vehicle positions `[pos, dir, alive, [crew ids], [from, to]]`).
- K/D = kills / deaths (kills when there are no deaths). Missions shorter than `minDuration` are not counted.
- Custom columns: formulas over the variables in `report.Variables`; evaluated server-side in `Result.ApplyColumns`; NaN/Inf become null.

## Frontend conventions

- No build step, no frameworks: plain scripts (no modules) wrapped in IIFEs.
- Strict CSP: no inline scripts, no `eval`, no new script origins. Third-party scripts only on demand and pinned with SRI (currently just html2canvas). XLSX export uses our own `xlsx.js`.
- Build DOM with the `el()` helper and `textContent`; never `innerHTML` with data.
- Every user-visible string goes through `i18n.js` in **all five languages** (en, cs, de, pl, uk) with identical key sets. Static markup uses `data-i18n*` attributes.
- Accessibility: interactive things are buttons/links, sortable headers use `aria-sort`, tabs follow the WAI-ARIA pattern, modals make `.page` inert, text colours keep 4.5:1 contrast (tokens in `app.css`), user-defined colours are never used for text.

## Security conventions

- Admin API: session cookie (`__Host-` over HTTPS, `SameSite=Strict`) or `Authorization: Bearer <adminToken>`; state-changing cookie requests need `X-Requested-With`; no CORS on `/api/admin/`.
- Login is rate limited per client IP and per user name; password hashing goes through `Server.hashSlots`.
- `X-Forwarded-*` headers are trusted only with `trustProxy`.
- Validate anything from settings that reaches HTML/CSS (e.g. rotation colours must be hex).
- Never commit secrets, `.env`, databases or third-party reference material.

## Writing style

- Code comments, docs, commit messages and API messages in technical English.
- Commit messages: imperative subject, wrapped body explaining what and why.
