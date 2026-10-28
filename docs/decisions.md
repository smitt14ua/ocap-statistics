# Design decisions

Short records of why the project is built the way it is. Read the relevant entry before changing one of these areas; if you change a decision, update its entry (or add a new one that supersedes it) in the same change.

Format: **Decision** — context and reasons — consequences.

## 1. Process every recording once; serve everything from our own database

OCAP recordings are large JSON files (megabytes to gigabytes) on a server that also serves replays. Computing statistics from them on every request would load OCAP and be slow. Each recording is therefore read once by the syncer, and every API and page request is answered from SQLite.

Consequences: statistics lag OCAP by the sync interval; the syncer must be robust (retries with back-off, `missing` status for recordings that return 404 everywhere, so they are not retried forever; they are retried only when OCAP reports a different conversion status for them). Recordings OCAP is still receiving over its WebSocket ingest (`streaming`) are not taken until they are saved, as they have no file yet.

## 2. Keep a compact digest of each recording, with versions

Between "raw recording" and "per-mission statistics rows" the syncer stores a digest: entities, kills, hits, end frame and vehicle crews, gzipped JSON of about 10 KB. Counting rules change more often than the recording format, and the digest lets a rule change be applied to all history without downloading anything again.

Two version numbers drive this: `digest.Version` (what is extracted — bump it and stored recordings are fetched again) and `stats.ComputeVersion` together with the nickname and assist rules (how it is counted — bump it and missions are recomputed from digests, offline). Positions are not stored; a statistic that needs them requires a digest change.

## 3. SQLite through modernc.org/sqlite (pure Go)

The data set is small (hundreds to thousands of missions), written by one process and read by many requests. SQLite needs no separate service, and the pure-Go driver keeps the build free of cgo, so one static binary cross-compiles for linux/amd64 and linux/arm64 without emulation and the runtime image is a small Alpine image.

Settings: WAL journal, `busy_timeout`, `_txlock=immediate` (a writer takes the lock when its transaction starts, so concurrent writers wait instead of failing on upgrade), a small connection pool. It was the only third-party Go dependency until the OpenID Connect sign-in (17).

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

## 13. Medical statistics from ACE activity logs; Steam ids stored, not published

OCAP records no "X treated Y" event. Recent versions write `medicalSnapshot` events with the patient's ACE activity log: localized text lines such as "Name has bandaged patient", of which ACE keeps only the last few. The digest therefore keeps each new log line (found by overlapping consecutive snapshots) with the medic resolved by name, and `ace3Unconscious` changes; counting stays independent of the game language because every line counts as one treatment. Medical points are weights over treatments and revives applied in `report.Build`, so tuning them in the admin panel needs no recompute.

Players' Steam ids (`playerUid` of the snapshots) are stored per mission for the Steam sign-in (14) and for merging a player's names (15), but no public endpoint, CSV or page shows them.

## 14. Steam sign-in only recognises the viewer; statistics stay public

Players can sign in with Steam (OpenID 2.0, optional, `steam.login`) to see their own statistics highlighted. OpenID needs no API key or registration with Steam, which keeps deployment as simple as before. Steam's answer is trusted only after Steam itself confirms the signature (`check_authentication`), and only when it is addressed to the sign-in this browser started (state cookie), signs the identity and return address, and carries a fresh, unused nonce — the claimed id in the URL alone proves nothing.

A Steam account maps to the names it played under in recordings that carry Steam ids (`mission_players.steam_id`); since those names are merged into one player (15), the page highlights the account's most recent name. Player sessions are separate from admin sessions, grant no rights, and `/api/auth/me` is the only response that contains a Steam id, the viewer's own; it is not part of the public API.

## 15. A player's names are merged by Steam account

Players rename themselves, and every name used to be a player of its own. Since recordings carry Steam ids (13), the names one Steam account played under are merged into one player, shown under the account's most recent name; the others are listed as aliases, and any of them opens the player. Missions recorded before OCAP wrote Steam ids join that player by name when the name belongs to one Steam account only; a name several accounts played under stays separate there, because it cannot be told whose it was. Two accounts that now play under the same name stay merged, as before.

The merge (`report.Identities`) is applied when the statistics are read, over all processed missions, so it needs no recompute and follows renames at once; `mission_players` keeps the names as recorded. A mission page shows the names played under in that mission, with the current one next to a renamed player and linking to the profile: it is a record of that game. It publishes that the merged names belong to one account, but never the Steam id itself.

## 16. Link previews from the page's translations, pictures in the browser

Discord, Telegram and the like preview a link from its Open Graph tags and run no scripts, so the server fills the tags of `index.html` for links to a player (`?player=`) or a mission (`?mission=`) with the name and key numbers. The texts reuse the page's translation files (`web/static/i18n`, read once on the server) in the language of the `lang` parameter the page adds to links it copies, else `Accept-Language`, so there is one set of strings. A preview carries no rendered image: drawing text into a PNG on the server needs a font library and an embedded font, a second dependency the project avoids. A picture for chats is made in the browser instead ("Copy card", html2canvas as for the top 10).

## 17. OpenID Connect sign-in through a vetted library; one sign-in method at a time

A community may run its own identity provider that already knows each member's Steam account. `login=oidc` signs players in there instead of on Steam (14); `login` chooses one method, so the page has one button and a session always means the same thing. The provider is configured only through `OIDC_*` variables: issuer (endpoints from discovery), client, scopes and the names of the claims with the SteamID64, avatar and nickname, since providers name them differently.

Token handling is security-critical and easy to get subtly wrong, so it is left to `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2` (discovery, Authorization Code flow with PKCE `S256`, `client_secret_basic` or a public client, ID token signature, issuer, audience and expiry) instead of the standard library alone; the dependencies are imported only by `internal/auth`. The site adds state and nonce checks, requires the userinfo to name the ID token's `sub`, and takes the profile from the userinfo, which is current, not from the ID token.

Users are bound by issuer and `sub` only. The Steam id, nickname and avatar are profile data, refreshed at every sign-in: a user may link Steam later or change the avatar, whose URL is never treated as permanent. A user without a linked Steam account is signed in and told so; when the provider shares neither a Steam id nor a name (typically because it silently granted fewer scopes than requested), the sign-in fails rather than showing an empty profile, and the granted scopes are logged. Codes and tokens are never logged; the ID token is stored with the session only for `id_token_hint` at the provider's logout.

Sessions are server-side rows, so the provider's back-channel logout (OpenID Connect Back-Channel Logout 1.0) simply deletes them with their ID tokens: all sessions of the `sub`, or of the provider session `sid` (kept from the ID token) when the logout token names no subject. go-oidc verifies the logout token; the site adds a recent `iat` and rejects a `jti` it has processed (kept in memory: a replay after a restart could only end sessions that the provider already ended).
