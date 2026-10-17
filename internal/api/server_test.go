package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/store"
)

func TestStaticHandler(t *testing.T) {
	big := strings.Repeat("<p>statistics</p>", 200)
	h, err := newStaticHandler(fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html>" + big)},
		"admin/index.html": {Data: []byte("<!doctype html>admin")},
		"app.css":          {Data: []byte("body{}")},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("index: %d %v", rec.Code, rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(zr); !strings.Contains(string(body), "statistics") {
		t.Error("gzipped body differs")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("site CSP: %q", csp)
	}
	etag := rec.Header().Get("ETag")

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("conditional GET: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/admin/" {
		t.Errorf("/admin redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("admin page can be framed: %v", rec.Header())
	}

	for _, p := range []string{"/missing.js", "/../go.mod", "/admin/../../etc/passwd"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

func TestSecurityHeadersAndCaching(t *testing.T) {
	_, sy, h := setup(t)
	runSync(t, sy)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") == "" {
		t.Errorf("security headers: %v", rec.Header())
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("stats response not gzipped: %v", rec.Header())
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent over plain HTTP")
	}

	// The embedded site is served next to the API, unknown API paths are JSON 404s.
	for path, want := range map[string]int{"/": 200, "/admin/": 200, "/app.js": 200, "/api/nope": 404, "/api/v1/nope": 404} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s: %d, want %d", path, rec.Code, want)
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d", rec.Code)
	}

	// Unknown parameters do not change the cache key.
	if cacheKey("/api/v1/players", url.Values{"sort": {"kd"}, "x": {"1"}}) != cacheKey("/api/v1/players", url.Values{"sort": {"kd"}, "x": {"2"}}) {
		t.Error("unknown parameters change the cache key")
	}
	if cacheKey("/api/v1/players", url.Values{"sort": {"kd"}}) == cacheKey("/api/v1/players", url.Values{"sort": {"kills"}}) {
		t.Error("known parameters do not change the cache key")
	}
}

func TestAcceptsGzipAndETag(t *testing.T) {
	for header, want := range map[string]bool{
		"":                  false,
		"gzip":              true,
		"br, gzip;q=0.5":    true,
		"gzip;q=0":          false,
		"deflate, identity": false,
		"GZIP; q=1.0":       true,
		"gzip;q=0.0, br":    false,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", header)
		if got := acceptsGzip(r); got != want {
			t.Errorf("acceptsGzip(%q) = %v", header, got)
		}
	}
	etag := `W/"1-2"`
	for header, want := range map[string]bool{`W/"1-2"`: true, `"1-2"`: true, `"x", W/"1-2"`: true, `*`: true, `"1-3"`: false, ``: false} {
		if got := etagMatches(header, etag); got != want {
			t.Errorf("etagMatches(%q) = %v", header, got)
		}
	}
}

func TestLoginLimiterPerIP(t *testing.T) {
	l := newLoginLimiter()
	for range maxLoginFailsPerIP {
		l.fail("10.0.0.1", "admin")
	}
	if !l.locked("10.0.0.1", "admin") {
		t.Error("attacker address not locked")
	}
	if l.locked("10.0.0.2", "admin") {
		t.Error("the admin is locked out from another address")
	}
	for range maxLoginFailsPerUser {
		l.fail("10.0.0.3", "admin")
	}
	if !l.locked("10.0.0.2", "admin") {
		t.Error("user name not locked after many failures")
	}
}

func TestClientIP(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.7")
	if got := s.clientIP(r); got != "192.0.2.1" {
		t.Errorf("untrusted proxy headers used: %s", got)
	}
	s.cfg.TrustProxy = true
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Errorf("trusted proxy: %s", got)
	}
}

func TestResultCacheComputesOnce(t *testing.T) {
	c := newResultCache(4)
	f := store.Filter{From: "2026-01-01", To: "2026-12-31"}
	var calls atomic.Int32
	release := make(chan struct{})
	compute := func(context.Context) (built, error) {
		calls.Add(1)
		<-release
		return built{}, nil
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, err := c.get(context.Background(), 1, f, compute); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("computed %d times, want 1", n)
	}
	// A new data generation computes again.
	if _, err := c.get(context.Background(), 2, f, func(context.Context) (built, error) { calls.Add(1); return built{}, nil }); err != nil || calls.Load() != 2 {
		t.Errorf("new generation: calls=%d err=%v", calls.Load(), err)
	}
}

// TestOpenAPIMatchesRoutes keeps the published spec honest: it must be valid JSON, list no
// admin endpoints, and every documented path must be served.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openapiSpec, &spec); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	_, sy, h := setup(t)
	runSync(t, sy)
	if len(spec.Paths) < 10 {
		t.Fatalf("only %d documented paths", len(spec.Paths))
	}
	for path, ops := range spec.Paths {
		if strings.HasPrefix(path, "/api/admin/") {
			t.Errorf("admin endpoint %s is documented publicly", path)
		}
		if _, ok := ops["get"]; !ok {
			continue
		}
		url := strings.NewReplacer("{name}", "Smith", "{id}", "1").Replace(path)
		if strings.HasPrefix(path, "/api/v1/squads/") {
			url = "/api/v1/squads/MILF"
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("documented GET %s answered %d", url, rec.Code)
		}
	}
}
