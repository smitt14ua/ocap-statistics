package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
