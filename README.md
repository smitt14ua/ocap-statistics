# OCAP Statistics

A statistics service for [OCAP2](https://github.com/OCAP2/OCAP) (Operation Capture And Playback for Arma 3). It ingests recordings from an OCAP web server, computes player, squad and rotation statistics and serves them through a public REST API and a web page.

Scripts that build statistics in the browser download every recording of a period (tens of megabytes each) on every page load, which puts a lot of load on the OCAP server. This service downloads each recording **once**, keeps a compact digest and the computed statistics in SQLite, and answers every request from its own database.

```
OCAP ──(every 10 min: list of recordings)──► ocap-stats ──► SQLite ──► REST API / web page / admin panel
     ──(each new recording, once)─────────►
```

## Features

- **Automatic ingestion.** New recordings are picked up on a schedule; recordings deleted or re-tagged in OCAP disappear from the statistics. Recordings can be read straight from OCAP's data directory when the service runs on the same host.
- **Statistics:** kills, deaths, team kills, destroyed vehicles, kill assists (kills by other crew members of your vehicle), accuracy, attendance, K/D, achievements, squad leaderboard, rotation score between groups of squads, per-player profiles (mission history, favourite weapons, victims and killers), per-mission details.
- **Squads from nicknames.** `[ALPHA] John` is player `John` in squad `ALPHA`; recruits (`[~ALPHA~] John`) count in their squad like everyone else. The nickname format, squad aliases and ignored names are configurable.
- **REST API** with OpenAPI documentation at `/api/docs`, CSV export, ETag caching and CORS.
- **Web page** with filters, player/squad views, Excel export, top-10 and rotation screenshots to the clipboard and links to the replays in OCAP.
- **Custom columns:** define your own columns with spreadsheet-like formulas in the admin panel (e.g. `round(killAssists / attendance, 2)`); they appear on the site, in the API, CSV and Excel.
- **Admin panel** (`/admin/`) with user accounts: ingestion status, sync / recompute / full resync / retry tasks, settings, nickname format with a live preview, custom columns with a formula preview, default sort of the tables (any column, including custom ones).
- **Localized UI:** English, Czech, German, Polish and Ukrainian, picked from the browser language and switchable manually.
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
| `stats.squadPattern` | `OCAPSTATS_SQUAD_PATTERN` | `\[~?(.*?)~?\]\s*` | Regex for the squad in a nickname; group 1 is the squad, the whole match is cut from the name |
| `stats.squadAliases` | — | — | Squad spellings merged into one squad, e.g. `{"A.L.P.H.A.": "ALPHA"}` |
| `stats.ignoreNames` | — | — | Player names never counted (headless clients and `__SERVER__` are always skipped) |
| `rotation.since` | `OCAPSTATS_ROTATION_SINCE` | — | First day of the rotation report |
| `rotation.sides` | — | — | Rotation sides: name, colour and squads |
| `ui.title`, `ui.logoUrl` | `OCAPSTATS_UI_TITLE`, `OCAPSTATS_UI_LOGO_URL` | `OCAP Statistics` | Site title and logo |
| `ui.ocapUrl` | `OCAPSTATS_UI_OCAP_URL` | `ocap.url` | Public OCAP URL for replay links |
| `trustProxy` | `OCAPSTATS_TRUST_PROXY` | `false` | Trust `X-Forwarded-*` headers from a reverse proxy |
| `log.level`, `log.format` | `OCAPSTATS_LOG_LEVEL`, `OCAPSTATS_LOG_FORMAT` | `info`, `text` | Logging (`debug`/`info`/`warn`/`error`, `text`/`json`) |
| `listen` | `OCAPSTATS_LISTEN` | `127.0.0.1:5080` (`0.0.0.0:5080` in Docker) | HTTP listen address |
| `db` | `OCAPSTATS_DB` | `data/stats.db` (`/data/stats.db` in Docker) | SQLite database |

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
| `GET /api/v1/squads/{name}` | A squad and its members |
| `GET /api/v1/achievements` | Achievements of a period |
| `GET /api/v1/rotation` | Rotation score and match log |
| `GET /api/v1/missions` | Processed missions of a period |
| `GET /api/v1/missions/{id}` | One mission (OCAP recording id) |
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
- **Team kill:** a kill of a player on the killer's side.
- **Death:** a `killed` event whose victim is a player (suicides included).
- **Accuracy:** hits by the player on non-vehicle targets / shots fired × 100.
- **Attendance:** number of counted missions (missions shorter than the minimum duration are not counted).

Over a period:

- **K/D:** kills / deaths (kills when there are no deaths).
- **Kill assist:** a kill (not a team kill) made by another crew member of the vehicle the player sat in at that moment, so drivers and commanders get credit for their gunner's kills. Crews are taken from the vehicle positions in the recording.
- **Squads:** a player's results count towards the squad they played in during each mission. Squad names are merged case-insensitively and shown in their most common spelling.
- **Best squad award:** an attendance-weighted K/D pulled towards the average for squads with few deaths.
- **Rotation:** a rotation side wins a mission when the winner of the `endMission` event names the in-game side (EAST, WEST, …) most of its players were on; any other mission is a draw.

### Custom columns

Formulas use the variables `kills`, `deaths`, `teamKills`, `vehiclesDestroyed`, `killAssists`, `shotsFired`, `hits`, `accuracy`, `kd`, `attendance`, `players`, `squadMissions`, `maxAttendance`, `missions` and `isSquad`; the operators `+ - * / % ^`, comparisons and `&& || !`; and the functions `abs sqrt exp ln log log10 floor ceil round min max pow if`. For squads the statistics are squad totals and `attendance` is the average per player. A division by zero gives an empty value. The admin panel has a full reference and checks formulas against real data before saving.

When the counting rules or the nickname format change, statistics are recomputed from the stored digests without downloading recordings again.

## Development

```bash
make test    # go test -race ./...
make lint    # go vet + staticcheck
make vuln    # govulncheck
make build   # ./ocap-stats with version information
```

`ocap-stats -version` prints the build version; `GET /api/v1/status` reports it as well.

Contributors and AI coding agents: start with [AGENTS.md](AGENTS.md) — architecture, where to change what, and the definition of done.

```
cmd/ocap-stats      entry point
internal/ocap       OCAP client (recordings list, recording download / local read)
internal/digest     streaming recording parser → compact digest
internal/stats      per-mission counting rules, nickname parsing
internal/store      SQLite: missions, digests, per-mission statistics, kills, users
internal/report     aggregation over a period: players, squads, achievements, rotation
internal/settings   runtime settings (config defaults + admin overrides)
internal/syncer     task loop: sync, recompute, resync
internal/auth       admin passwords and sessions
internal/api        REST API, admin API, caches, static files, security headers, OpenAPI
internal/formula    formula language of custom columns
web/static          web page, admin panel, translations (i18n.js), XLSX writer
```

## License

[MIT](LICENSE)
