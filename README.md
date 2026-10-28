# OCAP Statistics

A statistics service for [OCAP2](https://github.com/OCAP2/OCAP) (Operation Capture And Playback for Arma 3). It ingests recordings from an OCAP web server, computes player, squad and rotation statistics and serves them through a public REST API and a web page.

Scripts that build statistics in the browser download every recording of a period (tens of megabytes each) on every page load, which puts a lot of load on the OCAP server. This service downloads each recording **once**, keeps a compact digest and the computed statistics in SQLite, and answers every request from its own database.

```
OCAP ──(every 10 min: list of recordings)──► ocap-stats ──► SQLite ──► REST API / web page / admin panel
     ──(each new recording, once)─────────►
```

## Features

- **Automatic ingestion.** New recordings are picked up on a schedule; recordings deleted or re-tagged in OCAP disappear from the statistics. Recordings can be read straight from OCAP's data directory when the service runs on the same host.
- **Statistics:** kills, deaths, team kills, destroyed vehicles, kill assists (kills by other crew members of your vehicle, deaths of enemies you hit shortly before), medical points (ACE treatments of other players and revives), accuracy, attendance, K/D, achievements, squad leaderboard, squad vs squad (a heat map of who killed whom), rotation score between groups of squads, per-player profiles (mission history, favourite weapons, victims and killers, monthly trends of missions, kills per mission and K/D), a list of missions with a page for each: result, sides, players and squads, a kill feed showing who killed whom and who got the assists, and a link to the replay in OCAP.
- **Squads from nicknames.** `[ALPHA] John` is player `John` in squad `ALPHA`; recruits (`[~ALPHA~] John`) count in their squad like everyone else. The nickname format, squad aliases and ignored names are configurable.
- **REST API** with OpenAPI documentation at `/api/docs`, CSV export, ETag caching and CORS.
- **Web page** with filters, player/squad views, Excel export, top-10 and rotation screenshots to the clipboard and links to the replays in OCAP.
- **Sharing:** a player's profile and a mission have a link (`?player=…`, `?mission=…`) that Discord, Telegram and other chats preview with the name and key numbers, in the language of whoever copied it, and a "Copy card" button that puts a picture of them on the clipboard.
- **Custom columns:** define your own columns with spreadsheet-like formulas in the admin panel (e.g. `round(killAssists / attendance, 2)`); they appear on the site, in the API, CSV and Excel.
- **Sign in** with Steam or an OpenID Connect provider (optional): players see their own statistics for the period and find themselves highlighted in every table and award.
- **Admin panel** (`/admin/`) with user accounts: ingestion status, sync / recompute / full resync / retry tasks, settings, nickname format with a live preview, custom columns with a formula preview, default sort of the tables (any column, including custom ones).
- **Localized UI:** English, Belarusian, Czech, German, Kazakh, Polish and Ukrainian, picked from the browser language and switchable manually.
- **Secure and light by default:** strict Content-Security-Policy, rate-limited admin login, gzip and ETags everywhere, no web fonts or trackers, no third-party code on page load.
- Single static binary (Go, embedded pure-Go SQLite), multi-arch Docker image (amd64, arm64).

## Quick start

### Docker Compose

```bash
cp .env.example .env    # set OCAPSTATS_OCAP_URL and the admin password
docker compose up -d
```

The site is at <http://localhost:5080>, the admin panel at <http://localhost:5080/admin/>, the API documentation at <http://localhost:5080/api/docs>.

On the first start the service works through the archive (newest recordings first, two at a time with a short pause between downloads). Set `OCAPSTATS_SYNC_SINCE` to limit how far back it goes.

### From source

Requires Go 1.26+.

```bash
go build -o ocap-stats ./cmd/ocap-stats
OCAPSTATS_OCAP_URL=https://ocap.example.com OCAPSTATS_ADMIN_PASSWORD=change-me-please ./ocap-stats
```

`-config path` loads a JSON config file (default `config.json`, optional). `-once` runs a single sync and exits.

### Running next to OCAP

If the service runs on the same host as OCAP, mount OCAP's data directory (the one with `*.json.gz` recordings) **read-only** and point `OCAPSTATS_OCAP_DATA_DIR` at it. Recordings are then read from disk and OCAP only serves the cheap list of recordings. A recording missing on disk falls back to an HTTP download.

```yaml
services:
  ocap-stats:
    image: ghcr.io/smitt14ua/ocap-statistics:latest
    env_file: .env
    environment:
      OCAPSTATS_OCAP_DATA_DIR: /ocap-data
    volumes:
      - ocap-stats-data:/data
      - /path/to/ocap/data:/ocap-data:ro
```

The container runs as UID 10001 and only needs read access to the recordings.

### Behind a reverse proxy

When the service is only reachable through a reverse proxy (Traefik, nginx, Caddy, Dokploy, Coolify, ...), set `OCAPSTATS_TRUST_PROXY=true`. Client addresses for login rate limiting and HTTPS detection for secure cookies and HSTS then come from the `X-Forwarded-*` headers. Do not enable it when the port is exposed directly. See [SECURITY.md](SECURITY.md).

## Configuration

Settings come from a JSON file ([`config.example.json`](config.example.json)) and `OCAPSTATS_*` environment variables ([`.env.example`](.env.example)); environment variables take precedence.

Statistics settings (tags, start date, minimum duration, nickname format, aliases, ignored names, rotation) can also be changed in the admin panel; custom columns and the default sort exist only there. Once saved there, they override the file/environment values until reset from the panel.

| Setting | Environment variable | Default | Description |
|---|---|---|---|
| `ocap.url` | `OCAPSTATS_OCAP_URL` | — (required) | OCAP web server URL |
| `ocap.dataDir` | `OCAPSTATS_OCAP_DATA_DIR` | — | Read recordings from this directory instead of HTTP |
| `ocap.token` | `OCAPSTATS_OCAP_TOKEN` | — | Bearer token for OCAP instances that are not public |
| `admin.username` | `OCAPSTATS_ADMIN_USERNAME` | `admin` | First admin user |
| `admin.password` | `OCAPSTATS_ADMIN_PASSWORD` | — | Its password (at least 8 characters). Without it and without users the admin panel is unusable |
| `admin.resetPassword` | `OCAPSTATS_ADMIN_PASSWORD_RESET` | `false` | Overwrite the user's password with the configured one on start |
| `admin.sessionTTL` | `OCAPSTATS_ADMIN_SESSION_TTL` | `168h` | Admin session lifetime |
| `adminToken` | `OCAPSTATS_ADMIN_TOKEN` | — | Bearer token for `POST /api/v1/sync` (scripts, webhooks) |
| `sync.enabled` | `OCAPSTATS_SYNC_ENABLED` | `true` | Periodic sync |
| `sync.interval` | `OCAPSTATS_SYNC_INTERVAL` | `10m` | How often to check OCAP for new recordings |
| `sync.tags` | `OCAPSTATS_SYNC_TAGS` | all | OCAP tags to ingest (comma-separated, exact match) |
| `sync.since` | `OCAPSTATS_SYNC_SINCE` | all | Ingest recordings dated on or after this day |
| `sync.concurrency` | `OCAPSTATS_SYNC_CONCURRENCY` | `2` | Recordings processed in parallel |
| `sync.requestDelay` | `OCAPSTATS_SYNC_REQUEST_DELAY` | `500ms` | Pause after each download |
| `sync.maxAttempts`, `sync.retryDelay` | — | `5`, `30m` | Retries of failing recordings (exponential back-off) |
| `stats.defaultTag` | `OCAPSTATS_DEFAULT_TAG` | all | Tag used when a request has no `tag` |
| `stats.minDuration` | `OCAPSTATS_MIN_DURATION` | `1200` | Missions shorter than this (seconds) are not counted |
| `stats.minMissions` | `OCAPSTATS_MIN_MISSIONS` | `0` | Players and squads with fewer missions in the selected period are not listed (their missions still count for their squads) |
| `stats.assists.hits` | `OCAPSTATS_HIT_ASSISTS` | `true` | Give a kill assist for hitting an enemy shortly before its death |
| `stats.assists.window` | `OCAPSTATS_ASSIST_WINDOW` | `300` | How long before the death (seconds, 1–3600) a hit counts for an assist |
| `stats.medical.treatment`, `stats.medical.revive` | — | `1`, `5` | Medical points per treatment of another player and per revive (0–1000) |
| `stats.squadPattern` | `OCAPSTATS_SQUAD_PATTERN` | `\[~?(.*?)~?\]\s*` | Regex for the squad in a nickname; group 1 is the squad, the whole match is cut from the name |
| `stats.squadAliases` | — | — | Squad spellings merged into one squad, e.g. `{"A.L.P.H.A.": "ALPHA"}` |
| `stats.ignoreNames` | — | — | Player names never counted (headless clients and `__SERVER__` are always skipped) |
| `rotation.since` | `OCAPSTATS_ROTATION_SINCE` | — | First day of the rotation report |
| `rotation.sides` | — | — | Rotation sides: name, colour and squads |
| `ui.title`, `ui.logoUrl` | `OCAPSTATS_UI_TITLE`, `OCAPSTATS_UI_LOGO_URL` | `OCAP Statistics` | Site title and logo |
| `ui.ocapUrl` | `OCAPSTATS_UI_OCAP_URL` | `ocap.url` | Public OCAP URL for replay links |
| `login` | `OCAPSTATS_LOGIN` | off | How players sign in: `steam`, `oidc` or `off` (see [Player sign-in](#player-sign-in)) |
| `steam.login` | `OCAPSTATS_STEAM_LOGIN` | `false` | Older switch for `login=steam`, used when `login` is not set |
| `steam.sessionTTL` | `OCAPSTATS_STEAM_SESSION_TTL` | `720h` | How long a player stays signed in (either method) |
| `oidc.issuer` | `OIDC_ISSUER` | — | Issuer of the OpenID Connect provider; endpoints come from `<issuer>/.well-known/openid-configuration` |
| `oidc.clientId` | `OIDC_CLIENT_ID` | — | Client ID |
| `oidc.clientSecret` | `OIDC_CLIENT_SECRET` | — | Client secret (`client_secret_basic`); empty = public client, PKCE only |
| `oidc.scopes` | `OIDC_SCOPES` | `openid profile` | Scopes, separated by spaces (`openid` is always added) |
| `oidc.providerName` | `OIDC_PROVIDER_NAME` | `SSO` | Name on the "Sign in with …" button |
| `oidc.steamIdClaim` | `OIDC_STEAM_ID_CLAIM` | `steam_id` | Claim with the SteamID64 |
| `oidc.pictureClaim` | `OIDC_PICTURE_CLAIM` | `picture` | Claim with the avatar URL |
| `oidc.nameClaim` | `OIDC_NAME_CLAIM` | `name` | Claim with the nickname (falls back to `preferred_username`, then `nickname`) |
| `publicUrl` | `OCAPSTATS_PUBLIC_URL` | from the request | Address users open the site at, e.g. `https://stats.example.com`; the sign-in sends players back there |
| `trustProxy` | `OCAPSTATS_TRUST_PROXY` | `false` | Trust `X-Forwarded-*` headers from a reverse proxy |
| `log.level`, `log.format` | `OCAPSTATS_LOG_LEVEL`, `OCAPSTATS_LOG_FORMAT` | `info`, `text` | Logging (`debug`/`info`/`warn`/`error`, `text`/`json`) |
| `listen` | `OCAPSTATS_LISTEN` | `127.0.0.1:5080` (`0.0.0.0:5080` in Docker) | HTTP listen address |
| `db` | `OCAPSTATS_DB` | `data/stats.db` (`/data/stats.db` in Docker) | SQLite database |

### Player sign-in

Players can sign in to see a card with their own statistics for the selected period, find their rows, squad and awards highlighted everywhere, and get a "My profile" button. Nothing else changes: signed-in players get no extra rights, and statistics stay public. `OCAPSTATS_LOGIN` picks one of two methods:

- `steam`: "Sign in with Steam" (OpenID 2.0, no API key or registration needed).
- `oidc`: an OpenID Connect provider of your own (Keycloak, Authentik, Zitadel, …) that knows the user's Steam account and returns its SteamID64 in a claim (`OIDC_STEAM_ID_CLAIM`). The service uses the Authorization Code flow with PKCE (`S256`) and authenticates with `client_secret_basic` when `OIDC_CLIENT_SECRET` is set; without it, it is a public client. Register at the provider:
  - redirect URI `https://<your site>/api/auth/oidc/callback` (for local development e.g. `http://localhost:8765/api/auth/oidc/callback`);
  - post-logout redirect URI `https://<your site>/` (signing out also signs out at the provider when it has an `end_session_endpoint`).

  Users are identified by issuer and `sub`; the Steam id, nickname and avatar are read from the userinfo endpoint at every sign-in. A user without a linked Steam account is signed in and told to link it at the provider. When the provider shares neither a Steam id nor a name — usually because the client may not use the requested scopes, which providers drop silently — the sign-in fails and the log names the scopes the provider granted.

Players are recognised by the Steam id that recent OCAP versions write into recordings (`playerUid`), so only missions recorded with such a version link a Steam account to a name; older missions of the same name count too (see "Players" under [How statistics are computed](#how-statistics-are-computed)). Steam ids are never shown on the site or in the API, except to the signed-in player themselves.

Set `OCAPSTATS_PUBLIC_URL` when the address cannot be derived from the request (behind a proxy without `OCAPSTATS_TRUST_PROXY`, or under a path prefix); the OIDC redirect URI is built from it.

## API

Interactive documentation: **`/api/docs`** (OpenAPI spec: `/api/openapi.json`).

Common parameters of the statistics endpoints:

| Parameter | Description |
|---|---|
| `from`, `to` | Period, `YYYY-MM-DD`, inclusive. Unbounded by default |
| `tag` | Mission tag; without it the default tag is used, `*` selects all tags |
| `min_duration` | Minimum mission duration in seconds |

| Endpoint | Returns |
|---|---|
| `GET /api/v1/stats` | Everything for a period: totals, players, squads, achievements |
| `GET /api/v1/players` | Player leaderboard. `search`, `squad=ALPHA,BRAVO` (`-` = no squad), `sort`, `order`, `limit`, `offset`, `format=csv` |
| `GET /api/v1/players/{name}` | Player profile (name without the squad prefix) |
| `GET /api/v1/squads` | Squad leaderboard (same list parameters) |
| `GET /api/v1/squads/{name}` | A squad, its members and the squads it killed most and was killed by most |
| `GET /api/v1/squad-matrix` | Kills of players by players between every pair of squads |
| `GET /api/v1/achievements` | Achievements of a period |
| `GET /api/v1/rotation` | Rotation score and match log |
| `GET /api/v1/missions` | Processed missions of a period |
| `GET /api/v1/missions/{id}` | One mission (OCAP recording id): players, squads, kills and kill assists |
| `GET /api/v1/status` | Ingestion status |
| `POST /api/v1/sync` | Queue a sync now (`Authorization: Bearer <adminToken>`) |

Examples:

```bash
# Top 10 players of October by K/D
curl "http://localhost:5080/api/v1/players?from=2026-10-01&to=2026-10-31&sort=kd&limit=10"

# Members of squad ALPHA as CSV
curl -o alpha.csv "http://localhost:5080/api/v1/players?squad=ALPHA&format=csv"
```

Responses are cached in memory until the data changes and carry an `ETag`, so clients polling the API can send `If-None-Match` and get `304 Not Modified`. Errors look like `{"error": "message", "code": "machine_readable_code"}`.

## How statistics are computed

Per mission:

- **Kill:** a `killed` event whose killer is a player other than the victim. Destroying a vehicle counts as a destroyed vehicle, not a kill.
- **Death without a killer:** OCAP writes the crew of a destroyed vehicle, drownings, falls and the like as suicides, without a killer or as killed by a wreck. Such a death goes to the enemy (player or AI) who last damaged the victim within the assist window and after its previous death: who last hit it, or who destroyed the vehicle it sat in (then the destroyer's own crew get crew assists for it). Damage by team mates does not count; without enemy damage the death stays a suicide. Such kills do not count for the longest kill.
- **Team kill:** a kill of a player on the killer's side.
- **Death:** a `killed` event whose victim is a player (suicides included).
- **Accuracy:** hits by the player on non-vehicle targets / shots fired × 100.
- **Treatment:** an entry another player wrote into a player's ACE medical activity log (bandage, tourniquet, splint, medication, IV, CPR, …), taken from the `medicalSnapshot` events of recent OCAP versions. Treating oneself does not count. ACE keeps only the last entries of the log and OCAP writes snapshots every few seconds, so a burst of more treatments than the log holds between two snapshots is counted partly. Only players have snapshots: treating AI is not counted.
- **Revive:** an unconscious player (`ace3Unconscious`) woke up without dying in between, after another player treated them. It goes to the last such medic before the wake-up, or to the first one seen within 15 seconds after it (the log reaches the recording with the next snapshot).
- **Medical points:** treatments × `stats.medical.treatment` + revives × `stats.medical.revive` (1 and 5 by default, editable in the admin panel; applied when the statistics are shown, so changing them needs no recompute).
- **Attendance:** number of counted missions (missions shorter than the minimum duration are not counted).

Over a period:

- **Players:** a player is a name without the squad prefix. Names a Steam account played under (recent OCAP versions record Steam ids) are one player, shown under the most recent name, with the others as aliases; any of them opens the player. A mission's page keeps the names played under in it and shows a renamed player's current name next to the old one. Older missions without Steam ids count for that player when only one Steam account ever played under the name. Steam ids are never published.
- **K/D:** kills / deaths (kills when there are no deaths).
- **Kill assist:** a kill (not a team kill) made by another crew member of the vehicle the player sat in at that moment, so drivers and commanders get credit for their gunner's kills (crews are taken from the vehicle positions in the recording); and, unless turned off, the death of an enemy (player or AI) the player hit, or whose vehicle the player destroyed, in the last 5 minutes (`stats.assists.window`) before it, whoever or whatever killed it. Hits before the victim's previous death and hits on vehicles do not count; a player gets at most one assist per death and none for their own kill.
- **Squads:** a player's results count towards the squad they played in during each mission, so a player who leaves a squad does not take its past results along. The players table shows the squad of the player's latest mission in the period (none if they last played without one). Squad names are merged case-insensitively and shown in their most common spelling.
- **Squad attendance:** the average number of missions per squad player as a share of the period's missions (the site shows it in %; the API returns the average as `avgAttendance`). **Players per mission** (`avgPlayers`): the squad's player-missions divided by the missions it took part in.
- **Best squad award:** an attendance-weighted K/D pulled towards the average for squads with few deaths.
- **Rotation:** a rotation side wins a mission when the winner of the `endMission` event names the in-game side (EAST, WEST, …) most of its players were on; any other mission is a draw.

### Custom columns

Formulas use the variables `kills`, `deaths`, `teamKills`, `vehiclesDestroyed`, `killAssists`, `shotsFired`, `hits`, `accuracy`, `kd`, `attendance`, `players`, `squadMissions`, `avgPlayers`, `maxAttendance`, `missions`, `isSquad`, `treatments`, `revives` and `medicalPoints`; the operators `+ - * / % ^`, comparisons and `&& || !`; and the functions `abs sqrt exp ln log log10 floor ceil round min max pow if`. For squads the statistics are squad totals, `attendance` is the average per player and `avgPlayers` the players per mission the squad took part in. A division by zero gives an empty value. The admin panel has a full reference and checks formulas against real data before saving.

When the counting rules or the nickname format change, statistics are recomputed from the stored digests without downloading recordings again.

## Development

```bash
make test    # go test -race ./...
make lint    # go vet + staticcheck
make vuln    # govulncheck
make build   # ./ocap-stats with version information
make check   # everything CI runs, plus HTML/JS checks (needs Node.js)
```

`ocap-stats -version` prints the build version; `GET /api/v1/status` reports it as well.

Contributors and AI coding agents: start with [AGENTS.md](AGENTS.md) — architecture, where to change what, and the definition of done. Frontend specifics are in [web/AGENTS.md](web/AGENTS.md); the reasons behind the main design choices are in [docs/decisions.md](docs/decisions.md).

```
cmd/ocap-stats      entry point
internal/ocap       OCAP client (recordings list, recording download / local read)
internal/digest     streaming recording parser → compact digest
internal/stats      per-mission counting rules, nickname parsing
internal/store      SQLite: missions, digests, per-mission statistics, kills, assists, users
internal/report     aggregation over a period: players, squads, achievements, rotation
internal/settings   runtime settings (config defaults + admin overrides)
internal/syncer     task loop: sync, recompute, resync
internal/auth       admin passwords and sessions
internal/api        REST API, admin API, caches, static files, security headers, OpenAPI
internal/formula    formula language of custom columns
web/static          web page, admin panel, translations (i18n/<lang>.js), XLSX writer
```

## License

[MIT](LICENSE)
