package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/auth"
	"github.com/smitt14ua/ocap-statistics/internal/config"
	"github.com/smitt14ua/ocap-statistics/internal/ocap"
	"github.com/smitt14ua/ocap-statistics/internal/settings"
	"github.com/smitt14ua/ocap-statistics/internal/store"
	"github.com/smitt14ua/ocap-statistics/internal/syncer"
	"github.com/smitt14ua/ocap-statistics/web"
)

// fakeOCAP serves an operations list and gzipped recordings like OCAP2 web does.
type fakeOCAP struct {
	mu        sync.Mutex
	ops       []ocap.Operation
	recording []byte
	downloads atomic.Int32
}

func (f *fakeOCAP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/v1/operations":
		f.mu.Lock()
		defer f.mu.Unlock()
		tag := r.URL.Query().Get("tag")
		out := []ocap.Operation{}
		for _, op := range f.ops {
			if strings.Contains(op.Tag, tag) {
				out = append(out, op)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(r.URL.Path, "/data/"):
		f.downloads.Add(1)
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(f.recording)
		_ = zw.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buf.Bytes())
	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T) (*fakeOCAP, *syncer.Syncer, http.Handler) {
	t.Helper()
	rec, err := os.ReadFile("../stats/testdata/recording.json")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOCAP{recording: rec, ops: []ocap.Operation{
		{ID: 1, Filename: "Coop_1_20261001", MissionName: "Coop_1", WorldName: "altis", Date: "2026-10-01", Tag: "COOP"},
		{ID: 2, Filename: "Coop_2_20261002", MissionName: "Coop_2", WorldName: "altis", Date: "2026-10-02", Tag: "COOP"},
		{ID: 3, Filename: "TvT_20261003", MissionName: "TvT", WorldName: "altis", Date: "2026-10-03", Tag: "COOP_TVT"},
	}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	cfg := config.Default()
	cfg.OCAP.URL = srv.URL
	cfg.Sync.RequestDelay = 0
	cfg.Sync.Tags = []string{"COOP"}
	cfg.Stats.DefaultTag = "COOP"
	cfg.Rotation = config.Rotation{Since: "2026-01-01", Sides: []config.RotationSide{
		{Name: "Side 1", Color: "#3b82f6", Squads: []string{"MILF"}},
		{Name: "Side 2", Color: "#ef4444", Squads: []string{"SOKIL"}},
	}}
	st, err := store.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	sm, err := settings.NewManager(ctx, st, settings.FromConfig(cfg), log)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Admin.Password = "secret-password"
	if err := auth.EnsureAdmin(ctx, st, cfg.Admin, log); err != nil {
		t.Fatal(err)
	}
	sy := syncer.New(cfg, ocap.NewClient(srv.URL, "", "", time.Minute, log), st, sm, log)
	h, err := New(cfg, BuildInfo{Version: "test"}, st, sm, sy, web.FS(), log).Handler()
	if err != nil {
		t.Fatal(err)
	}
	return fake, sy, h
}

func get(t *testing.T, h http.Handler, url string, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if out != nil {
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", url, rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
	}
	return rec
}

func runSync(t *testing.T, sy *syncer.Syncer) {
	t.Helper()
	if err := sy.RunTask(context.Background(), syncer.TaskSync); err != nil {
		t.Fatal(err)
	}
}

func TestSyncAndAPI(t *testing.T) {
	fake, sy, h := setup(t)
	runSync(t, sy)
	if st := sy.Status(); st.LastError != "" || st.LastProcessed != 2 {
		t.Fatalf("sync status: %+v", st)
	}
	if n := fake.downloads.Load(); n != 2 {
		t.Fatalf("downloads = %d, want 2 (other tags must be skipped)", n)
	}

	// A second sync must not download anything again.
	runSync(t, sy)
	if n := fake.downloads.Load(); n != 2 {
		t.Fatalf("downloads after resync = %d, want 2", n)
	}

	var stats struct {
		Totals  struct{ Missions, Players, Kills int }
		Players []struct {
			Name       string
			Kills      int
			Attendance int
			KDM        float64
		}
		Achievements struct {
			TopKiller   struct{ Name string }
			LongestKill struct {
				Name  string
				Value float64
			}
		}
	}
	get(t, h, "/api/v1/stats?from=2026-10-01&to=2026-10-31", &stats)
	if stats.Totals.Missions != 2 || stats.Totals.Players != 3 || stats.Totals.Kills != 6 {
		t.Errorf("totals: %+v", stats.Totals)
	}
	if stats.Achievements.TopKiller.Name != "Smith" {
		t.Errorf("top killer: %+v", stats.Achievements)
	}
	// Javelin (900 m) hits a vehicle and does not count; the 300 m rifle kill does.
	if stats.Achievements.LongestKill.Value != 300 {
		t.Errorf("longest kill: %+v", stats.Achievements.LongestKill)
	}
	for _, p := range stats.Players {
		if p.Name == "Smith" && (p.Kills != 6 || p.Attendance != 2) {
			t.Errorf("Smith: %+v", p)
		}
	}

	// Filters: a range with one mission.
	get(t, h, "/api/v1/stats?from=2026-10-02&to=2026-10-02", &stats)
	if stats.Totals.Missions != 1 {
		t.Errorf("one-day range: %+v", stats.Totals)
	}

	// Squads are merged case-insensitively ("MILF" and recruits "[~milf~]").
	var squads struct {
		Squads []struct {
			Name    string
			Players int
		}
	}
	get(t, h, "/api/v1/squads?sort=name&order=asc", &squads)
	if len(squads.Squads) != 2 || squads.Squads[0].Name != "MILF" || squads.Squads[0].Players != 2 {
		t.Errorf("squads: %+v", squads.Squads)
	}

	var player struct {
		Player   struct{ Name string }
		Missions []struct{ MissionID int64 }
		Weapons  []struct {
			Name  string
			Count int
		}
	}
	get(t, h, "/api/v1/players/Smith", &player)
	if len(player.Missions) != 2 || len(player.Weapons) == 0 || player.Weapons[0].Count != 6 {
		t.Errorf("player detail: %+v", player)
	}
	if rec := get(t, h, "/api/v1/players/Nobody", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown player: %d", rec.Code)
	}

	var rot struct {
		Rotation struct {
			Sides []struct{ Wins int }
			Draws int
		}
	}
	get(t, h, "/api/v1/rotation", &rot)
	if rot.Rotation.Sides[0].Wins != 2 || rot.Rotation.Draws != 0 {
		t.Errorf("rotation: %+v", rot.Rotation)
	}

	rec := get(t, h, "/api/v1/players?format=csv&limit=1", nil)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") || !strings.Contains(rec.Body.String(), "Smith") {
		t.Errorf("csv: %s %q", ct, rec.Body.String())
	}

	// ETag: unchanged data answers 304.
	first := get(t, h, "/api/v1/squads", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/squads", nil)
	req.Header.Set("If-None-Match", first.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	h.ServeHTTP(cached, req)
	if cached.Code != http.StatusNotModified {
		t.Errorf("conditional request: %d", cached.Code)
	}

	if rec := get(t, h, "/api/v1/players?from=yesterday", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad date: %d", rec.Code)
	}

	// A recording deleted in OCAP disappears from the statistics.
	fake.mu.Lock()
	fake.ops = fake.ops[1:]
	fake.mu.Unlock()
	runSync(t, sy)
	get(t, h, "/api/v1/stats", &stats)
	if stats.Totals.Missions != 1 {
		t.Errorf("after deletion: %+v", stats.Totals)
	}
}

func TestSyncRequiresToken(t *testing.T) {
	_, _, h := setup(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sync", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("sync without admin token: %d", rec.Code)
	}
}
