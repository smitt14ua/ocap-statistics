package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/smitt14ua/ocap-statistics/internal/report"
	"github.com/smitt14ua/ocap-statistics/internal/settings"
	"github.com/smitt14ua/ocap-statistics/internal/syncer"
)

type adminClient struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func (c *adminClient) do(method, url string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, url, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	req.Header.Set(csrfHeader, "ocap-stats")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, h http.Handler, user, password string) *adminClient {
	t.Helper()
	c := &adminClient{t: t, h: h}
	rec := c.do(http.MethodPost, "/api/admin/login", map[string]string{"username": user, "password": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie {
			c.cookie = ck
		}
	}
	if c.cookie == nil || !c.cookie.HttpOnly {
		t.Fatalf("session cookie: %+v", c.cookie)
	}
	return c
}

func TestAdminAuth(t *testing.T) {
	_, _, h := setup(t)
	anon := &adminClient{t: t, h: h}
	if rec := anon.do(http.MethodGet, "/api/admin/state", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("state without login: %d", rec.Code)
	}
	if rec := anon.do(http.MethodPost, "/api/admin/login", map[string]string{"username": "admin", "password": "wrong-password"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", rec.Code)
	}

	c := login(t, h, "ADMIN", "secret-password") // user names are case-insensitive
	if rec := c.do(http.MethodGet, "/api/admin/me", nil); !strings.Contains(rec.Body.String(), `"admin"`) {
		t.Errorf("me: %s", rec.Body)
	}

	// State-changing requests need the CSRF header.
	req := httptest.NewRequest(http.MethodPost, "/api/admin/tasks/sync", nil)
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF header: %d", rec.Code)
	}

	// The admin API has no CORS headers.
	if rec := c.do(http.MethodGet, "/api/admin/state", nil); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("admin API allows cross-origin requests")
	}

	// Users: create, log in as them, cannot delete yourself.
	if rec := c.do(http.MethodPost, "/api/admin/users", map[string]string{"username": "smith", "password": "short"}); rec.Code != http.StatusBadRequest {
		t.Errorf("short password: %d", rec.Code)
	}
	if rec := c.do(http.MethodPost, "/api/admin/users", map[string]string{"username": "smith", "password": "long-enough"}); rec.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body)
	}
	smith := login(t, h, "smith", "long-enough")
	if rec := smith.do(http.MethodDelete, "/api/admin/users/smith", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("delete self: %d", rec.Code)
	}
	// Changing a password signs the user out.
	if rec := c.do(http.MethodPut, "/api/admin/users/smith/password", map[string]string{"password": "another-password"}); rec.Code != http.StatusNoContent {
		t.Fatalf("set password: %d %s", rec.Code, rec.Body)
	}
	if rec := smith.do(http.MethodGet, "/api/admin/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("session survived password change: %d", rec.Code)
	}
	if rec := c.do(http.MethodDelete, "/api/admin/users/smith", nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete user: %d", rec.Code)
	}

	// Logout invalidates the session.
	c.do(http.MethodPost, "/api/admin/logout", nil)
	if rec := c.do(http.MethodGet, "/api/admin/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", rec.Code)
	}
}

func TestAdminLoginLockout(t *testing.T) {
	_, _, h := setup(t)
	anon := &adminClient{t: t, h: h}
	for range maxLoginFailsPerIP {
		anon.do(http.MethodPost, "/api/admin/login", map[string]string{"username": "admin", "password": "wrong-password"})
	}
	rec := anon.do(http.MethodPost, "/api/admin/login", map[string]string{"username": "admin", "password": "secret-password"})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("after %d failures: %d", maxLoginFailsPerIP, rec.Code)
	}
}

func TestAdminSettingsAndTasks(t *testing.T) {
	fake, sy, h := setup(t)
	runSync(t, sy)
	c := login(t, h, "admin", "secret-password")

	var state struct {
		Settings settings.Settings
		Custom   bool
	}
	if err := json.Unmarshal(c.do(http.MethodGet, "/api/admin/state", nil).Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Custom || state.Settings.Nickname.SquadPattern == "" {
		t.Fatalf("initial state: %+v", state)
	}

	// Preview a nickname format.
	rec := c.do(http.MethodPost, "/api/admin/preview-names", map[string]any{
		"nickname": map[string]any{"squadPattern": `\[(.*?)\]\s*`, "aliases": map[string]string{"milf": "Alpha"}},
		"names":    []string{"[MILF] Smith", "HC"},
	})
	if !strings.Contains(rec.Body.String(), `"squad":"Alpha"`) || !strings.Contains(rec.Body.String(), `"counted":false`) {
		t.Errorf("preview: %s", rec.Body)
	}
	if rec := c.do(http.MethodPost, "/api/admin/preview-names", map[string]any{"nickname": map[string]any{"squadPattern": `[`}}); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid pattern preview: %d", rec.Code)
	}

	// Saving an alias queues a recompute; the stats change without downloading again.
	set := state.Settings
	set.Nickname.Aliases = map[string]string{"milf": "MILF-1"}
	rec = c.do(http.MethodPut, "/api/admin/settings", set)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rules":true`) {
		t.Fatalf("save settings: %d %s", rec.Code, rec.Body)
	}
	if q := sy.Status().Queued; len(q) != 1 || q[0] != syncer.TaskRecompute {
		t.Fatalf("queued: %v", q)
	}
	downloads := fake.downloads.Load()
	if err := sy.RunTask(context.Background(), syncer.TaskRecompute); err != nil {
		t.Fatal(err)
	}
	if fake.downloads.Load() != downloads {
		t.Error("recompute downloaded recordings")
	}
	var squads struct{ Squads []struct{ Name string } }
	get(t, h, "/api/v1/squads?sort=name&order=asc", &squads)
	if squads.Squads[0].Name != "MILF-1" {
		t.Errorf("alias not applied: %+v", squads.Squads)
	}

	// Invalid settings are rejected.
	bad := set
	bad.Since = "yesterday"
	if rec := c.do(http.MethodPut, "/api/admin/settings", bad); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid settings: %d", rec.Code)
	}
	bad = set
	bad.Assists.Window = 0
	if rec := c.do(http.MethodPut, "/api/admin/settings", bad); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid assist window: %d", rec.Code)
	}

	// Assist settings are counting rules too: changing them recomputes.
	if !set.Assists.Hits || set.Assists.Window != 300 {
		t.Errorf("default assists: %+v", set.Assists)
	}
	set.Assists.Hits = false
	if rec := c.do(http.MethodPut, "/api/admin/settings", set); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rules":true`) {
		t.Errorf("save assists: %d %s", rec.Code, rec.Body)
	}

	// A full resync downloads everything again.
	if rec := c.do(http.MethodPost, "/api/admin/tasks/resync", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("resync: %d", rec.Code)
	}
	if err := sy.RunTask(context.Background(), syncer.TaskResync); err != nil {
		t.Fatal(err)
	}
	if got := fake.downloads.Load() - downloads; got != 2 {
		t.Errorf("resync downloads = %d, want 2", got)
	}

	// Reset goes back to the config values.
	rec = c.do(http.MethodDelete, "/api/admin/settings", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rules":true`) {
		t.Errorf("reset: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodPost, "/api/admin/tasks/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown task: %d", rec.Code)
	}
}

func TestCustomColumns(t *testing.T) {
	_, sy, h := setup(t)
	runSync(t, sy)
	c := login(t, h, "admin", "secret-password")

	var state struct{ Settings settings.Settings }
	if err := json.Unmarshal(c.do(http.MethodGet, "/api/admin/state", nil).Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	set := state.Settings

	// Preview before saving.
	rec := c.do(http.MethodPost, "/api/admin/preview-column", map[string]any{"formula": "kills / max(deaths, 1)", "decimals": 1})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Smith"`) {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	rec = c.do(http.MethodPost, "/api/admin/preview-column", map[string]any{"formula": "kills / "})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"invalid_formula"`) {
		t.Errorf("invalid preview: %d %s", rec.Code, rec.Body)
	}

	set.Columns = []report.Column{
		{Name: "KPM", Formula: "kills / attendance", Decimals: 2, Target: "players"},
		{Name: "Zero", Formula: "kills / 0", Target: "both"},
	}
	rec = c.do(http.MethodPut, "/api/admin/settings", set)
	if rec.Code != http.StatusOK {
		t.Fatalf("save columns: %d %s", rec.Code, rec.Body)
	}
	var saved struct{ Settings settings.Settings }
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	if len(saved.Settings.Columns) != 2 || saved.Settings.Columns[0].ID == "" {
		t.Fatalf("saved columns: %+v", saved.Settings.Columns)
	}
	kpm, zero := saved.Settings.Columns[0].ID, saved.Settings.Columns[1].ID

	var players struct {
		Players []struct {
			Name   string
			KD     float64
			Custom map[string]*float64
		}
	}
	get(t, h, "/api/v1/players?sort=custom."+kpm, &players)
	smith := players.Players[0]
	if smith.Name != "Smith" || smith.Custom[kpm] == nil || *smith.Custom[kpm] != 3 || smith.Custom[zero] != nil {
		t.Errorf("custom values: %+v", smith)
	}
	if smith.KD != 3 { // 6 kills, 2 deaths
		t.Errorf("kd = %v, want 3", smith.KD)
	}
	if rec := get(t, h, "/api/v1/players?format=csv", nil); !strings.Contains(rec.Body.String(), "KPM") || !strings.Contains(rec.Body.String(), "kill_assists") {
		t.Errorf("csv without custom columns: %s", rec.Body)
	}
	var cfg struct{ Columns []struct{ ID, Name string } }
	get(t, h, "/api/v1/config", &cfg)
	if len(cfg.Columns) != 2 {
		t.Errorf("public config columns: %+v", cfg.Columns)
	}

	// Unknown variables are rejected when saving.
	set.Columns = []report.Column{{Name: "Bad", Formula: "kils * 2"}}
	if rec := c.do(http.MethodPut, "/api/admin/settings", set); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown variable") {
		t.Errorf("bad formula saved: %d %s", rec.Code, rec.Body)
	}
}

func TestDefaultSort(t *testing.T) {
	_, sy, h := setup(t)
	runSync(t, sy)
	c := login(t, h, "admin", "secret-password")
	var state struct{ Settings settings.Settings }
	_ = json.Unmarshal(c.do(http.MethodGet, "/api/admin/state", nil).Body.Bytes(), &state)
	set := state.Settings
	if set.Sort.Players.Field != "kills" || set.Sort.Squads.Order != "desc" {
		t.Fatalf("default sort: %+v", set.Sort)
	}

	set.Columns = []report.Column{{ID: "deaths2", Name: "D2", Formula: "deaths * 2", Target: "players"}}
	set.Sort.Players = settings.SortOrder{Field: "custom.deaths2", Order: "asc"}
	set.Sort.Squads = settings.SortOrder{Field: "name", Order: "asc"}
	if rec := c.do(http.MethodPut, "/api/admin/settings", set); rec.Code != http.StatusOK {
		t.Fatalf("save sort: %d %s", rec.Code, rec.Body)
	}
	var players struct{ Players []struct{ Name string } }
	get(t, h, "/api/v1/players", &players)
	// deaths*2 ascending: Rookie 4, Smith 4 (ties by name), Enemy 8.
	if len(players.Players) != 3 || players.Players[0].Name != "Rookie" || players.Players[2].Name != "Enemy" {
		t.Errorf("players not sorted by the custom column: %+v", players.Players)
	}
	var squads struct{ Squads []struct{ Name string } }
	get(t, h, "/api/v1/squads", &squads)
	if squads.Squads[0].Name != "" && squads.Squads[0].Name > squads.Squads[len(squads.Squads)-1].Name {
		t.Errorf("squads not sorted by name asc: %+v", squads.Squads)
	}

	// Removing the column used for sorting falls back to kills instead of failing.
	set.Columns = nil
	rec := c.do(http.MethodPut, "/api/admin/settings", set)
	var saved struct{ Settings settings.Settings }
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	if rec.Code != http.StatusOK || saved.Settings.Sort.Players.Field != "kills" {
		t.Errorf("fallback: %d %+v", rec.Code, saved.Settings.Sort)
	}
	set.Sort.Players = settings.SortOrder{Field: "bogus"}
	if rec := c.do(http.MethodPut, "/api/admin/settings", set); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown sort field accepted: %d", rec.Code)
	}
}

func TestMinMissions(t *testing.T) {
	_, sy, h := setup(t)
	runSync(t, sy)
	c := login(t, h, "admin", "secret-password")
	var state struct{ Settings settings.Settings }
	_ = json.Unmarshal(c.do(http.MethodGet, "/api/admin/state", nil).Body.Bytes(), &state)
	set := state.Settings

	// Nobody has 99 missions: the leaderboards are empty, profiles still open.
	set.MinMissions = 99
	if rec := c.do(http.MethodPut, "/api/admin/settings", set); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	var stats struct {
		Totals       report.Totals
		Players      []report.Player
		Squads       []report.Squad
		Achievements report.Achievements
	}
	get(t, h, "/api/v1/stats", &stats)
	if len(stats.Players) != 0 || stats.Totals.Players != 0 || stats.Totals.Squads != 0 || stats.Achievements.TopKiller != nil {
		t.Errorf("hidden players listed: %+v", stats)
	}
	for _, s := range stats.Squads {
		if s.Name != "" {
			t.Errorf("hidden squad listed: %+v", s)
		}
	}
	if stats.Totals.Kills == 0 {
		t.Error("totals lost the kills of hidden players")
	}
	if rec := get(t, h, "/api/v1/players/Smith", nil); rec.Code != http.StatusOK {
		t.Errorf("profile of a hidden player: %d", rec.Code)
	}
	var cfg struct{ MinMissions int }
	get(t, h, "/api/v1/config", &cfg)
	if cfg.MinMissions != 99 {
		t.Errorf("public config minMissions = %d", cfg.MinMissions)
	}

	set.MinMissions = 0
	if rec := c.do(http.MethodPut, "/api/admin/settings", set); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	get(t, h, "/api/v1/stats", &stats)
	if len(stats.Players) != 3 {
		t.Errorf("players with minMissions 0: %d, want 3", len(stats.Players))
	}

	set.MinMissions = -1
	if rec := c.do(http.MethodPut, "/api/admin/settings", set); rec.Code != http.StatusBadRequest {
		t.Errorf("negative minMissions accepted: %d", rec.Code)
	}
}
