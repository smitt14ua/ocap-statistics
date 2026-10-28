# web — frontend guide

The site (`static/index.html` + `static/app.js`) and the admin panel (`static/admin/index.html` + `static/admin/admin.js`) are plain HTML, CSS and JavaScript, embedded into the binary by `web.go` (`go:embed static` — everything in `static/` is served publicly, so keep notes and docs out of it). There is no build step, bundler, framework or npm dependency: edit a file, rebuild the Go binary, reload. The root `AGENTS.md` covers the backend and project-wide rules.

## Files

| File | Contents |
|---|---|
| `static/index.html` | page markup and page-specific CSS; script order: `static/i18n/*.js` → `i18n.js` → `xlsx.js` (defer) → `app.js` |
| `static/app.js` | `StatsApp`: period filter, players/squads table (`renderHeaders`, `row`, `render`), achievements, player/squad/rotation modals, missions list and mission view (`openMissions`, `openMission`, `missionFeed`; the detail modal keeps a Back stack in `showDetail`, an open mission is `?mission=<id>` and an open player `?player=<name>` in the address), sharing (`shareBar`: `copyLink` with `shareURL`, and `playerCard`/`missionCard` pictures through `capture`), squad vs squad heat map (`openMatrix`, `matrixTable`), monthly trend charts in the player profile (`trend`, `trendChart`; inline SVG built with `svg()`, one series and one scale per chart, a table below), Excel export (`exportExcel`), screenshots (`capture`; html2canvas loaded on demand by `loadLib`), Steam sign-in (`loadAccount`, `renderAccount`, the "My statistics" card `renderMe`; rows, awards and squads of the signed-in player's names in `this.mine` get the class `mine` and a screen-reader-only label from `youBadge`) |
| `static/admin/index.html`, `static/admin/admin.js` | `Admin`: login, tabs (status, settings, nicknames, columns, access), forms (`fillForms` / `collectSettings`), previews, users |
| `static/i18n.js` | language detection and switching, `t()`, `apply()`, `relTime()`; exposes `window.i18n` |
| `static/i18n/en.js` … `uk.js` | UI strings, one file per language, identical key sets; English is the fallback |
| `static/xlsx.js` | dependency-free XLSX writer (`window.xlsx`) |
| `static/app.css` | design tokens (`:root` custom properties) and shared components |
| `web_test.go` | translation guard tests |

## Rules

- **CSP** (set in `internal/api/static.go`): no inline `<script>`, no inline event handlers, no `eval`/`new Function`, no new script origins. Third-party code only on demand through `loadLib` with an SRI hash. Inline `style` attributes are allowed.
- **DOM**: build elements with `el(tag, props, ...children)` and set text through `textContent`; never `innerHTML` with data. User-defined colours (rotation sides) are used for swatches, never for text.
- **Strings**: every visible string goes through translations — `data-i18n`, `data-i18n-placeholder`, `data-i18n-title`, `data-i18n-aria` attributes in markup, `t('key', {var})` in JS. Add the key to all seven `static/i18n/*.js` files (only the file of the language you are working on needs reading; copy the key layout from `en.js`). `web_test.go` fails on missing or extra keys, on keys used but not defined, and on a language file a page does not load.
- **Accessibility** (WCAG 2.2 AA): real `<button>`/`<a>` for interactive elements, `type` on buttons; `aria-sort` on sortable headers; WAI-ARIA tabs in the admin panel; modals make `.page` inert and return focus; scrollable regions are focusable (`tabindex="0"`); colours come from `app.css` tokens, which keep text contrast ≥ 4.5:1.
- **Classes**: in `StatsApp` and `Admin`, fields and methods share one namespace — a field named like a method replaces it silently.

## Adding a language

Copy `static/i18n/en.js` to `static/i18n/<code>.js`, change the assignment to `.<code>` and translate the values; add the code and native name to `LANGS` in `static/i18n.js`; add `<script src="i18n/<code>.js">` to `static/index.html` and `<script src="../i18n/<code>.js">` to `static/admin/index.html`, before `i18n.js`. The web tests check all three steps.

## Checking a change

1. `make check-web` (`node --check` on every script, html-validate with `.htmlvalidate.json`) and `go test ./web`.
2. Build and run with `OCAPSTATS_SYNC_ENABLED=false` against a copy of a database, open the page in a browser, check the console.
3. Run axe-core: the CSP blocks CDN scripts, so copy `axe.min.js` into `static/` for a throwaway build, load it with a `<script>` tag, run `axe.run()`, and delete both before committing. Expect 0 violations.
