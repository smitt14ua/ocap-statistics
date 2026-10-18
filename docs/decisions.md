# Design decisions

Short records of why the project is built the way it is. Read the relevant entry before changing one of these areas; if you change a decision, update its entry (or add a new one that supersedes it) in the same change.

Format: **Decision** — context and reasons — consequences.

## 1. Process every recording once; serve everything from our own database

OCAP recordings are large JSON files (megabytes to gigabytes) on a server that also serves replays. Computing statistics from them on every request would load OCAP and be slow. Each recording is therefore read once by the syncer, and every API and page request is answered from SQLite.

Consequences: statistics lag OCAP by the sync interval; the syncer must be robust (retries with back-off, `missing` status for recordings that return 404 everywhere, so they are not retried forever).

## 2. Keep a compact digest of each recording, with versions

Between "raw recording" and "per-mission statistics rows" the syncer stores a digest: entities, kills, hits, end frame and vehicle crews, gzipped JSON of about 10 KB. Counting rules change more often than the recording format, and the digest lets a rule change be applied to all history without downloading anything again.

Two version numbers drive this: `digest.Version` (what is extracted — bump it and stored recordings are fetched again) and `stats.ComputeVersion` together with the nickname and assist rules (how it is counted — bump it and missions are recomputed from digests, offline). Positions are not stored; a statistic that needs them requires a digest change.

## 3. SQLite through modernc.org/sqlite (pure Go)

The data set is small (hundreds to thousands of missions), written by one process and read by many requests. SQLite needs no separate service, and the pure-Go driver keeps the build free of cgo, so one static binary cross-compiles for linux/amd64 and linux/arm64 without emulation and the runtime image is a small Alpine image.

Settings: WAL journal, `busy_timeout`, `_txlock=immediate` (a writer takes the lock when its transaction starts, so concurrent writers wait instead of failing on upgrade), a small connection pool. It is the only third-party Go dependency.

## 4. Aggregate in Go, cache per data generation

Per-mission rows are stored in SQL; aggregation over a period (players, squads, achievements, rotation, custom columns) is done in Go by `report.Build`. The rules (squad parsing, attendance, tie-breaks, formulas) are easier to express and test in Go than in SQL, and the data fits in memory.

To keep this cheap, the store has a generation counter that changes whenever statistics change. Aggregations are cached per filter and generation (deduplicated with singleflight), and rendered responses are cached with gzip and ETags. Any handler that needs aggregates must go through `Server.report`.

## 5. Read recordings from the OCAP data directory first

When the service runs next to OCAP, the recordings directory is mounted read-only and read directly; HTTP download is the fallback (logged as a warning). This avoids pushing gigabytes through OCAP's web server and works on hosts where OCAP is not reachable over the network.

## 6. Custom columns use a small formula language evaluated on the server

Administrators define columns such as `kills / max(deaths; 1)`. The formula is parsed and evaluated in Go (`internal/formula`): a fixed set of variables and functions, no reflection, no access to anything else. Server-side evaluation means the API, CSV and web page show identical values, sorting works on custom columns, invalid formulas are rejected with a position when saved, and the browser never needs `eval` (forbidden by the CSP).

## 7. No frontend build step

The web page and admin panel are plain HTML/CSS/JS embedded with `go:embed`. There is little UI, no team of frontend developers to need a framework, and a build step would add a Node toolchain to every contribution and to the Docker build. Node.js is used only by the optional checks (`node --check`, html-validate).

## 8. Strict Content-Security-Policy; dependencies replaced or loaded on demand

The pages send a CSP without `unsafe-inline` scripts or `unsafe-eval`. As a result, the Excel export uses an in-house XLSX writer (`web/static/xlsx.js`) instead of ExcelJS, which needs `eval`; html2canvas (screenshots) and Swagger UI (API docs) are loaded from a CDN only when used, pinned with SRI hashes. DOM is built with `textContent`, never `innerHTML` with data.

## 9. Translations: one file per language, loaded synchronously

All UI strings live in `web/static/i18n/<lang>.js`, loaded with plain script tags before `i18n.js`. Synchronous loading keeps `t()` synchronous and avoids a flash of untranslated text; the files are small, gzipped and cached. One file per language keeps edits small and lets a contributor or agent read only the language they work on. Guard tests keep key sets identical and check that every page loads every language.

## 10. Settings: configuration defaults plus admin overrides

Deployment configuration (JSON file and `OCAPSTATS_*` environment variables) gives the defaults; changes made in the admin panel are stored as one JSON document in the `meta` table and take precedence until reset. The service can be fully configured without the admin panel, and the panel can be used without redeploying. Each change decides whether it needs a recompute (counting rules) or a sync (tags, dates).

## 11. Small, self-contained admin authentication

Users and sessions are stored in SQLite; passwords are hashed with PBKDF2-HMAC-SHA256 (600 000 iterations, standard library). Sessions use a `__Host-` cookie with `SameSite=Strict`, and state-changing requests require an `X-Requested-With` header, so the admin API works only same-origin. Logins are rate limited per IP and per user name. A default administrator can be created from environment variables. The admin API is intentionally not part of the public OpenAPI document and has no CORS.

## 12. Community-neutral open source

The project started for one community but is published for any OCAP server. Community-specific values (URLs, tags, squad names, rotation sides, titles, logos) live only in deployment configuration, never in code, defaults, docs or tests.
