package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/smitt14ua/ocap-statistics/internal/ocap"
	"github.com/smitt14ua/ocap-statistics/internal/report"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

func privateGet(h http.Handler, url, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPrivatePlayerBySteamID(t *testing.T) {
	fake, sy, h := setup(t, func(s *Server) { s.cfg.APIToken, s.cfg.AdminToken = "api-token", "admin-token" })
	fake.mu.Lock()
	// As in TestPlayersMergedBySteam: Smith is Smithy in the latest mission and played an older
	// one before OCAP recorded Steam ids.
	fake.files = map[string][]byte{
		"Coop_2_20261002": bytes.ReplaceAll(fake.recording, []byte("[MILF] Smith"), []byte("[MILF] Smithy")),
		"Coop_0_20260930": bytes.ReplaceAll(fake.recording, []byte(`"playerUid": "`+testSteamID+`", `), nil),
	}
	fake.ops = append(fake.ops, ocap.Operation{ID: 4, Filename: "Coop_0_20260930", MissionName: "Coop_0", WorldName: "altis",
		Date: "2026-09-30", Tag: "COOP"})
	fake.mu.Unlock()
	runSync(t, sy)

	url := "/api/private/v1/players/" + testSteamID
	for _, tc := range []struct {
		token string
		code  int
	}{{"", http.StatusUnauthorized}, {"wrong", http.StatusUnauthorized}, {"api-token", http.StatusOK}, {"admin-token", http.StatusOK}} {
		rec := privateGet(h, url, tc.token)
		if rec.Code != tc.code {
			t.Errorf("token %q: %d %s", tc.token, rec.Code, rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("token %q: headers %v", tc.token, rec.Header())
		}
	}

	// The missions with the Steam id under both names and the older one under the name only
	// this account used, as on the site.
	var p struct {
		SteamID  string
		Names    []store.SteamName
		Player   report.Player
		Missions []missionRow
		Victims  []countItem
		Trend    []report.TrendPoint
	}
	rec := privateGet(h, url, "api-token")
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.SteamID != testSteamID || p.Player.Name != "Smithy" || len(p.Missions) != 3 || len(p.Trend) != 2 {
		t.Errorf("profile: %s %+v, %d missions, trend %+v", p.SteamID, p.Player, len(p.Missions), p.Trend)
	}
	if len(p.Names) != 2 || p.Names[0].Name != "Smithy" || p.Names[1].Name != "Smith" || p.Names[1].Missions != 1 {
		t.Errorf("names: %+v", p.Names)
	}
	if !slices.Contains(p.Victims, countItem{Name: "Rookie", Count: 3}) {
		t.Errorf("victims: %+v", p.Victims)
	}

	// The period filter applies; the trend still covers all time.
	rec = privateGet(h, url+"?from=2026-10-02", "api-token")
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || len(p.Missions) != 1 || len(p.Trend) != 2 {
		t.Errorf("period: %d %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct{ url, code string }{
		{"/api/private/v1/players/123", "bad_request"},
		{"/api/private/v1/players/76561190000000099", "unknown_steam_id"},
		{url + "?from=2026-11-01", "no_missions"},
		{url + "?from=yesterday", "bad_request"},
	} {
		var e struct{ Code string }
		rec := privateGet(h, tc.url, "api-token")
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Code != tc.code {
			t.Errorf("%s: %d %s, want %s", tc.url, rec.Code, rec.Body, tc.code)
		}
	}
}

func TestPrivateAPIDisabled(t *testing.T) {
	_, sy, h := setup(t)
	runSync(t, sy)
	if rec := privateGet(h, "/api/private/v1/players/"+testSteamID, "anything"); rec.Code != http.StatusNotFound {
		t.Errorf("without tokens: %d %s", rec.Code, rec.Body)
	}
}
