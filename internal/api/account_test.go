package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/config"
)

const testSteamID = "76561190000000001" // Smith's playerUid in the fixture recording

// fakeSteam is an OpenID 2.0 provider like Steam: it signs positive assertions and, for
// check_authentication, confirms only an assertion it issued, unchanged.
type fakeSteam struct {
	*httptest.Server
	mu     sync.Mutex
	issued map[string]url.Values // by signature
}

func newFakeSteam(t *testing.T) *fakeSteam {
	f := &fakeSteam{issued: map[string]url.Values{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.ParseForm() != nil || r.PostForm.Get("openid.mode") != "check_authentication" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		want, ok := f.issued[r.PostForm.Get("openid.sig")]
		f.mu.Unlock()
		valid := ok
		for _, field := range strings.Split(want.Get("openid.signed"), ",") {
			valid = valid && r.PostForm.Get("openid."+field) == want.Get("openid."+field)
		}
		_, _ = w.Write([]byte("ns:http://specs.openid.net/auth/2.0\nis_valid:" + map[bool]string{true: "true", false: "false"}[valid] + "\n"))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSteam) endpoint() string { return f.URL + "/openid/login" }

// assert returns the query Steam redirects back with after a successful sign-in.
func (f *fakeSteam) assert(returnTo, steamID string) url.Values {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := "https://steamcommunity.com/openid/id/" + steamID
	q := url.Values{
		"openid.ns":             {"http://specs.openid.net/auth/2.0"},
		"openid.mode":           {"id_res"},
		"openid.op_endpoint":    {f.endpoint()},
		"openid.claimed_id":     {id},
		"openid.identity":       {id},
		"openid.return_to":      {returnTo},
		"openid.response_nonce": {time.Now().UTC().Format(time.RFC3339) + hex.EncodeToString(b)},
		"openid.assoc_handle":   {"1234567890"},
		"openid.signed":         {"signed,op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle"},
		"openid.sig":            {hex.EncodeToString(b)},
	}
	f.reissue(q)
	return q
}

// reissue signs q as it is now, so only the server's own checks can reject it.
func (f *fakeSteam) reissue(q url.Values) {
	f.mu.Lock()
	f.issued[q.Get("openid.sig")] = q
	f.mu.Unlock()
}

// browser keeps cookies between requests like a browser does for one site.
type browser struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]*http.Cookie
}

func (b *browser) do(method, target string, csrf bool) *httptest.ResponseRecorder {
	b.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if csrf {
		req.Header.Set(csrfHeader, "ocap-stats")
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return rec
}

// startLogin follows the sign-in button and returns the return_to address Steam got.
func (b *browser) startLogin(steam *fakeSteam, ret string) string {
	b.t.Helper()
	rec := b.do(http.MethodGet, "/api/auth/steam?return="+url.QueryEscape(ret), false)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), steam.endpoint()+"?") {
		b.t.Fatalf("login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	q := loc.Query()
	if q.Get("openid.realm") != "http://example.com/" || q.Get("openid.mode") != "checkid_setup" {
		b.t.Fatalf("login query: %v", q)
	}
	if c := b.cookies[stateCookie]; c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		b.t.Fatalf("state cookie: %+v", c)
	}
	return q.Get("openid.return_to")
}

// callback returns from Steam with query q and reports where the site sent the browser.
func (b *browser) callback(returnTo string, q url.Values) string {
	b.t.Helper()
	u, _ := url.Parse(returnTo)
	rec := b.do(http.MethodGet, u.Path+"?state="+url.QueryEscape(u.Query().Get("state"))+"&"+q.Encode(), false)
	if rec.Code != http.StatusSeeOther {
		b.t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	return rec.Header().Get("Location")
}

type accountResp struct {
	SignedIn bool   `json:"signedIn"`
	SteamID  string `json:"steamId"`
	Name     string `json:"name"`
	Picture  string `json:"picture"`
	Names    []struct {
		Name     string `json:"name"`
		Missions int    `json:"missions"`
	} `json:"names"`
}

func (b *browser) account() accountResp {
	b.t.Helper()
	rec := b.do(http.MethodGet, "/api/auth/me", false)
	var a accountResp
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		b.t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		b.t.Errorf("me is cacheable: %q", rec.Header().Get("Cache-Control"))
	}
	return a
}

func TestSteamLogin(t *testing.T) {
	steam := newFakeSteam(t)
	_, sy, h := setup(t, func(s *Server) {
		s.cfg.Login = config.LoginSteam
		s.steam.Endpoint = steam.endpoint()
		s.steam.Client = steam.Client()
	})
	runSync(t, sy)
	var cfg struct{ SteamLogin bool }
	get(t, h, "/api/v1/config", &cfg)
	if !cfg.SteamLogin {
		t.Error("config does not announce the Steam sign-in")
	}

	b := &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
	if a := b.account(); a.SignedIn {
		t.Fatalf("signed in without a session: %+v", a)
	}
	returnTo := b.startLogin(steam, "?period=year")
	if !strings.HasPrefix(returnTo, "http://example.com/api/auth/steam/callback?state=") {
		t.Fatalf("return_to = %s", returnTo)
	}
	assertion := steam.assert(returnTo, testSteamID)
	if loc := b.callback(returnTo, assertion); loc != "http://example.com/?period=year" {
		t.Fatalf("after sign-in: %s", loc)
	}
	c := b.cookies[playerCookie]
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("player cookie: %+v", c)
	}
	a := b.account()
	if !a.SignedIn || a.SteamID != testSteamID || len(a.Names) != 1 || a.Names[0].Name != "Smith" || a.Names[0].Missions != 2 {
		t.Fatalf("me = %+v, want Smith with 2 missions", a)
	}
	// A player session is not an admin session.
	if rec := b.do(http.MethodGet, "/api/admin/state", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("admin state with a player session: %d", rec.Code)
	}

	// Answers that must not sign anybody in. Each starts a fresh sign-in in a new browser.
	fresh := func() (*browser, string) {
		nb := &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
		return nb, nb.startLogin(steam, "")
	}
	tamper := func(q url.Values, key, value string) url.Values {
		q2 := url.Values{}
		for k, v := range q {
			q2[k] = append([]string(nil), v...)
		}
		q2.Set(key, value)
		return q2
	}
	otherID := "https://steamcommunity.com/openid/id/76561190000000002"
	cases := map[string]func() (*browser, string, url.Values){
		"replayed assertion": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			// The old answer, re-addressed to the new state: only the used nonce is wrong
			// (the provider would confirm it again).
			q := steam.assert(rt, testSteamID)
			q.Set("openid.response_nonce", assertion.Get("openid.response_nonce"))
			steam.reissue(q)
			return nb, rt, q
		},
		"other identity": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			q := tamper(steam.assert(rt, testSteamID), "openid.claimed_id", otherID)
			q.Set("openid.identity", otherID)
			return nb, rt, q
		},
		"other provider": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			return nb, rt, tamper(steam.assert(rt, testSteamID), "openid.op_endpoint", "https://evil.example/openid/login")
		},
		"answer for another sign-in": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			_, other := fresh()
			return nb, rt, steam.assert(other, testSteamID)
		},
		"no state cookie": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			delete(nb.cookies, stateCookie)
			return nb, rt, steam.assert(rt, testSteamID)
		},
		"unsigned return_to": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			return nb, rt, tamper(steam.assert(rt, testSteamID), "openid.signed", "op_endpoint,claimed_id,identity,response_nonce,assoc_handle")
		},
		"stale nonce": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			q := steam.assert(rt, testSteamID)
			q.Set("openid.response_nonce", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)+"abc")
			steam.reissue(q)
			return nb, rt, q
		},
		"forged signature": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			return nb, rt, tamper(steam.assert(rt, testSteamID), "openid.sig", "00")
		},
		"not a Steam id": func() (*browser, string, url.Values) {
			nb, rt := fresh()
			id := "https://steamcommunity.com/openid/id/123"
			q := tamper(steam.assert(rt, testSteamID), "openid.claimed_id", id)
			q.Set("openid.identity", id)
			return nb, rt, q
		},
	}
	for name, mk := range cases {
		nb, rt, q := mk()
		if loc := nb.callback(rt, q); loc != "http://example.com/?login=failed" {
			t.Errorf("%s: redirected to %s", name, loc)
		}
		if nb.cookies[playerCookie] != nil || nb.account().SignedIn {
			t.Errorf("%s: signed in", name)
		}
	}

	// Cancelling on Steam returns to the page without a message.
	nb, rt := fresh()
	if loc := nb.callback(rt, url.Values{"openid.mode": {"cancel"}, "openid.ns": {"http://specs.openid.net/auth/2.0"}}); loc != "http://example.com/" {
		t.Errorf("cancel: redirected to %s", loc)
	}

	// Only a page query is accepted as the place to return to.
	for _, ret := range []string{"//evil.example", "https://evil.example/", "/admin/"} {
		nb := &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
		rt := nb.startLogin(steam, ret)
		if loc := nb.callback(rt, steam.assert(rt, testSteamID)); loc != "http://example.com/" {
			t.Errorf("return %q: redirected to %s", ret, loc)
		}
	}

	if rec := b.do(http.MethodPost, "/api/auth/logout", false); rec.Code != http.StatusForbidden {
		t.Errorf("logout without the CSRF header: %d", rec.Code)
	}
	if rec := b.do(http.MethodPost, "/api/auth/logout", true); rec.Code != http.StatusOK {
		t.Errorf("logout: %d %s", rec.Code, rec.Body)
	}
	b.cookies[playerCookie] = c // a stolen copy of the old cookie no longer works
	if b.account().SignedIn {
		t.Error("session still valid after logout")
	}
}

func TestSteamLoginDisabled(t *testing.T) {
	_, _, h := setup(t)
	var cfg struct{ SteamLogin bool }
	get(t, h, "/api/v1/config", &cfg)
	if cfg.SteamLogin {
		t.Error("Steam sign-in announced although disabled")
	}
	for _, u := range []string{"/api/auth/steam", "/api/auth/steam/callback", "/api/auth/me"} {
		if rec := get(t, h, u, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d", u, rec.Code)
		}
	}
}
