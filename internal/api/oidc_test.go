package api

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/auth"
	"github.com/smitt14ua/ocap-statistics/internal/config"
)

const (
	testClientID     = "stats-client"
	testClientSecret = "client-secret-for-tests"
	testRedirectURI  = "http://example.com/api/auth/oidc/callback"
)

// fakeOIDC is an OpenID Connect provider: discovery, JWKS, token endpoint with PKCE and client
// authentication, userinfo and an end_session_endpoint. Users are claim sets by subject.
type fakeOIDC struct {
	*httptest.Server
	t   *testing.T
	key *rsa.PrivateKey

	mu     sync.Mutex
	secret string                    // "" = public client
	users  map[string]map[string]any // userinfo claims (besides sub) by subject
	grants map[string]fakeGrant      // by authorization code, used once
	access map[string]string         // subject by access token
	// scope is reported by the token endpoint; "" leaves it out.
	scope string
	// Misbehaviour the site must reject.
	wrongNonce, wrongUserinfoSub bool
	// secrets lists every code and token issued, which must never appear in the site's log.
	secrets     []string
	lastIDToken string
}

type fakeGrant struct{ sub, nonce, challenge, redirect string }

func newFakeOIDC(t *testing.T, secret string) *fakeOIDC {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{t: t, key: key, secret: secret, users: map[string]map[string]any{},
		grants: map[string]fakeGrant{}, access: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"userinfo_endpoint":                     f.URL + "/userinfo",
			"jwks_uri":                              f.URL + "/jwks",
			"end_session_endpoint":                  f.URL + "/logout",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "none"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		sub, ok := f.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}
		claims := map[string]any{"sub": sub}
		if f.wrongUserinfoSub {
			claims["sub"] = "someone-else"
		}
		for k, v := range f.users[sub] {
			claims[k] = v
		}
		writeTestJSON(w, claims)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randomString() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b64(b)
}

// token redeems a code: one use, the same redirect_uri, the PKCE verifier of the challenge and
// the client's authentication (client_secret_basic, or none for a public client).
func (f *fakeOIDC) token(w http.ResponseWriter, r *http.Request) {
	fail := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
	}
	if r.ParseForm() != nil || r.PostForm.Get("grant_type") != "authorization_code" {
		fail("invalid_request")
		return
	}
	id, secret, basic := r.BasicAuth()
	if f.secret != "" {
		if !basic || id != testClientID || secret != f.secret || r.PostForm.Has("client_secret") {
			f.t.Errorf("token request without client_secret_basic: basic=%v form=%v", basic, r.PostForm.Has("client_secret"))
			fail("invalid_client")
			return
		}
	} else if basic || r.PostForm.Get("client_id") != testClientID || r.PostForm.Has("client_secret") {
		f.t.Errorf("public client sent credentials or no client_id")
		fail("invalid_client")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	code := r.PostForm.Get("code")
	g, ok := f.grants[code]
	delete(f.grants, code)
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || r.PostForm.Get("redirect_uri") != g.redirect || b64(sum[:]) != g.challenge {
		fail("invalid_grant")
		return
	}
	nonce := g.nonce
	if f.wrongNonce {
		nonce = "another-nonce"
	}
	access := randomString()
	f.access[access] = g.sub
	now := time.Now()
	idToken := f.sign(map[string]any{
		"iss": f.URL, "sub": g.sub, "aud": testClientID, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		// Stale profile: the site must prefer the userinfo.
		"picture": "https://cdn.example.com/stale.png",
	})
	f.lastIDToken = idToken
	f.secrets = append(f.secrets, code, access, idToken)
	resp := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 300, "id_token": idToken}
	if f.scope != "" {
		resp["scope"] = f.scope
	}
	writeTestJSON(w, resp)
}

func (f *fakeOIDC) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	payload, _ := json.Marshal(claims)
	input := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return input + "." + b64(sig)
}

func (f *fakeOIDC) setUser(sub string, claims map[string]any) {
	f.mu.Lock()
	f.users[sub] = claims
	f.mu.Unlock()
}

// authorize follows the sign-in button to the provider and returns its query, checking that
// the site asks for the Authorization Code flow with PKCE S256.
func (f *fakeOIDC) authorize(b *browser, ret string, scope string) url.Values {
	f.t.Helper()
	rec := b.do(http.MethodGet, "/api/auth/oidc?return="+url.QueryEscape(ret), false)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), f.URL+"/authorize?") {
		f.t.Fatalf("login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	q := loc.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": testClientID, "redirect_uri": testRedirectURI,
		"code_challenge_method": "S256", "scope": scope,
	} {
		if q.Get(k) != want {
			f.t.Errorf("authorize %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if len(q.Get("code_challenge")) != 43 || q.Get("state") == "" || q.Get("nonce") == "" {
		f.t.Errorf("authorize query: %v", q)
	}
	if c := b.cookies[oidcStateCookie]; c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		f.t.Fatalf("state cookie: %+v", c)
	}
	return q
}

// grant signs sub in at the provider for the authorize query q and returns the callback query.
func (f *fakeOIDC) grant(q url.Values, sub string) url.Values {
	code := randomString()
	f.mu.Lock()
	f.grants[code] = fakeGrant{sub: sub, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	f.mu.Unlock()
	return url.Values{"code": {code}, "state": {q.Get("state")}}
}

// signIn runs the whole sign-in as sub and returns where the site sends the browser.
func (f *fakeOIDC) signIn(b *browser, sub, ret string) string {
	f.t.Helper()
	return b.oidcCallback(f.grant(f.authorize(b, ret, "openid profile"), sub))
}

func (b *browser) oidcCallback(q url.Values) string {
	b.t.Helper()
	rec := b.do(http.MethodGet, "/api/auth/oidc/callback?"+q.Encode(), false)
	if rec.Code != http.StatusSeeOther {
		b.t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	return rec.Header().Get("Location")
}

// lockedBuffer collects the site's log.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// setupOIDC builds the site with the OpenID Connect sign-in against f; adjust changes the
// configuration (claim names, scopes) as the OIDC_* variables would.
func setupOIDC(t *testing.T, f *fakeOIDC, adjust func(*config.OIDC)) (http.Handler, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	_, sy, h := setup(t, func(s *Server) {
		s.cfg.Login = config.LoginOIDC
		s.cfg.OIDC = config.Default().OIDC
		s.cfg.OIDC.Issuer = f.URL
		s.cfg.OIDC.ClientID = testClientID
		s.cfg.OIDC.ClientSecret = f.secret
		if adjust != nil {
			adjust(&s.cfg.OIDC)
		}
		s.oidc = auth.NewOIDC(s.cfg.OIDC)
		s.oidc.Client = f.Client()
		s.log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	runSync(t, sy)
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := logs.String()
		for _, s := range f.secrets {
			if strings.Contains(out, s) {
				t.Errorf("a code or token was logged:\n%s", out)
			}
		}
	})
	return h, logs
}

func newBrowser(t *testing.T, h http.Handler) *browser {
	return &browser{t: t, h: h, cookies: map[string]*http.Cookie{}}
}

func TestOIDCLogin(t *testing.T) {
	f := newFakeOIDC(t, testClientSecret)
	h, _ := setupOIDC(t, f, nil)
	var cfg struct {
		Login, LoginName string
		SteamLogin       bool
	}
	get(t, h, "/api/v1/config", &cfg)
	if cfg.Login != "oidc" || cfg.LoginName != "SSO" || cfg.SteamLogin {
		t.Errorf("config = %+v, want the OIDC sign-in named SSO", cfg)
	}
	// Only the chosen method is served.
	for _, u := range []string{"/api/auth/steam", "/api/auth/steam/callback"} {
		if rec := get(t, h, u, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with login=oidc: %d", u, rec.Code)
		}
	}

	f.setUser("user-1", map[string]any{"steam_id": testSteamID, "name": "Smith (SSO)", "picture": "https://cdn.example.com/a.png"})
	b := newBrowser(t, h)
	if loc := f.signIn(b, "user-1", "?period=year"); loc != "http://example.com/?period=year" {
		t.Fatalf("after sign-in: %s", loc)
	}
	if c := b.cookies[playerCookie]; c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("player cookie: %+v", c)
	}
	a := b.account()
	if !a.SignedIn || a.SteamID != testSteamID || a.Name != "Smith (SSO)" || a.Picture != "https://cdn.example.com/a.png" ||
		len(a.Names) != 1 || a.Names[0].Name != "Smith" {
		t.Fatalf("me = %+v", a)
	}
	if rec := b.do(http.MethodGet, "/api/admin/state", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("admin state with a player session: %d", rec.Code)
	}

	// The profile is refreshed at every sign-in; the user stays the same subject.
	f.setUser("user-1", map[string]any{"steam_id": "76561190000000002", "name": "Smith 2", "picture": "https://cdn.example.com/b.png"})
	b2 := newBrowser(t, h)
	f.signIn(b2, "user-1", "")
	for _, br := range []*browser{b, b2} {
		if a := br.account(); a.SteamID != "76561190000000002" || a.Name != "Smith 2" || a.Picture != "https://cdn.example.com/b.png" {
			t.Errorf("refreshed profile = %+v", a)
		}
	}

	// Logout goes on to the provider's end_session_endpoint with the ID token as hint.
	idToken := f.lastIDToken
	rec := b2.do(http.MethodPost, "/api/auth/logout", true)
	var out struct{ Redirect string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	u, _ := url.Parse(out.Redirect)
	if !strings.HasPrefix(out.Redirect, f.URL+"/logout?") || u.Query().Get("id_token_hint") != idToken ||
		u.Query().Get("post_logout_redirect_uri") != "http://example.com/" || u.Query().Get("client_id") != testClientID {
		t.Errorf("logout redirect = %s", out.Redirect)
	}
	if b2.account().SignedIn {
		t.Error("signed in after logout")
	}
	if !b.account().SignedIn {
		t.Error("logout ended the session of another browser")
	}
}

func TestOIDCPublicClient(t *testing.T) {
	f := newFakeOIDC(t, "") // the fake rejects any client secret or Basic auth
	h, _ := setupOIDC(t, f, nil)
	f.setUser("user-1", map[string]any{"steam_id": testSteamID, "name": "Smith"})
	b := newBrowser(t, h)
	if loc := f.signIn(b, "user-1", ""); loc != "http://example.com/" {
		t.Fatalf("after sign-in: %s", loc)
	}
	if a := b.account(); !a.SignedIn || a.SteamID != testSteamID {
		t.Fatalf("me = %+v", a)
	}
}

func TestOIDCProfiles(t *testing.T) {
	f := newFakeOIDC(t, testClientSecret)
	h, _ := setupOIDC(t, f, nil)

	// Without a linked Steam account the player is signed in and told so.
	f.setUser("no-steam", map[string]any{"name": "Newbie", "picture": "https://cdn.example.com/n.png"})
	b := newBrowser(t, h)
	if loc := f.signIn(b, "no-steam", "?period=year"); loc != "http://example.com/?period=year&login=nosteam" {
		t.Errorf("without Steam: redirected to %s", loc)
	}
	if a := b.account(); !a.SignedIn || a.SteamID != "" || len(a.Names) != 0 || a.Name != "Newbie" {
		t.Errorf("without Steam: me = %+v", a)
	}

	// Without an avatar the page shows its placeholder; an unusable URL counts as none.
	for sub, pic := range map[string]any{"no-picture": nil, "http-picture": "http://cdn.example.com/a.png", "bad-picture": "javascript:alert(1)"} {
		claims := map[string]any{"steam_id": testSteamID, "name": "Smith"}
		if pic != nil {
			claims["picture"] = pic
		}
		f.setUser(sub, claims)
		b := newBrowser(t, h)
		if loc := f.signIn(b, sub, ""); loc != "http://example.com/" {
			t.Errorf("%s: redirected to %s", sub, loc)
		}
		// The ID token carries a stale picture, which must not take the place of none.
		if a := b.account(); !a.SignedIn || a.SteamID != testSteamID || a.Picture != "" {
			t.Errorf("%s: me = %+v", sub, a)
		}
	}

	// The nickname falls back to preferred_username, then nickname.
	f.setUser("fallback", map[string]any{"steam_id": testSteamID, "nickname": "Nick", "preferred_username": "Preferred"})
	b = newBrowser(t, h)
	f.signIn(b, "fallback", "")
	if a := b.account(); a.Name != "Preferred" {
		t.Errorf("name fallback = %q, want preferred_username", a.Name)
	}
}

func TestOIDCClaimNamesFromConfig(t *testing.T) {
	f := newFakeOIDC(t, testClientSecret)
	h, _ := setupOIDC(t, f, func(c *config.OIDC) {
		c.SteamIDClaim, c.PictureClaim, c.NameClaim = "steamid64", "avatar_url", "nick"
	})
	f.setUser("user-1", map[string]any{
		// A number of 17 digits must keep every digit.
		"steamid64": json.Number(testSteamID), "avatar_url": "https://cdn.example.com/custom.png", "nick": "Custom",
		// The default claim names are ignored.
		"steam_id": "76561190000000002", "picture": "https://cdn.example.com/default.png", "name": "Default",
	})
	b := newBrowser(t, h)
	f.signIn(b, "user-1", "")
	if a := b.account(); a.SteamID != testSteamID || a.Picture != "https://cdn.example.com/custom.png" || a.Name != "Custom" {
		t.Errorf("me = %+v, want the configured claims", a)
	}
}

func TestOIDCMissingScopes(t *testing.T) {
	f := newFakeOIDC(t, testClientSecret)
	h, logs := setupOIDC(t, f, func(c *config.OIDC) { c.Scopes = []string{"openid", "profile", "steam"} })
	// The client may not use "steam" and "profile": the provider leaves them out silently.
	f.scope = "openid"
	f.setUser("user-1", map[string]any{})
	b := newBrowser(t, h)
	if loc := b.oidcCallback(f.grant(f.authorize(b, "", "openid profile steam"), "user-1")); loc != "http://example.com/?login=claims" {
		t.Errorf("redirected to %s", loc)
	}
	if b.account().SignedIn {
		t.Error("signed in with an empty profile")
	}
	if out := logs.String(); !strings.Contains(out, "granted=openid") || !strings.Contains(out, `requested="openid profile steam"`) {
		t.Errorf("log does not name the granted scopes:\n%s", out)
	}

	// With the Steam id, fewer scopes are only logged.
	f.setUser("user-2", map[string]any{"steam_id": testSteamID})
	b = newBrowser(t, h)
	if loc := b.oidcCallback(f.grant(f.authorize(b, "", "openid profile steam"), "user-2")); loc != "http://example.com/" {
		t.Errorf("with Steam id: redirected to %s", loc)
	}
	if a := b.account(); !a.SignedIn || a.SteamID != testSteamID {
		t.Errorf("with Steam id: me = %+v", a)
	}
}

func TestOIDCRejected(t *testing.T) {
	f := newFakeOIDC(t, testClientSecret)
	h, _ := setupOIDC(t, f, nil)
	f.setUser("user-1", map[string]any{"steam_id": testSteamID, "name": "Smith"})

	cases := map[string]func(*browser) url.Values{
		"no state cookie": func(b *browser) url.Values {
			q := f.grant(f.authorize(b, "", "openid profile"), "user-1")
			delete(b.cookies, oidcStateCookie)
			return q
		},
		"answer for another sign-in": func(b *browser) url.Values {
			f.authorize(b, "", "openid profile")
			return f.grant(f.authorize(newBrowser(t, h), "", "openid profile"), "user-1")
		},
		"wrong state": func(b *browser) url.Values {
			q := f.grant(f.authorize(b, "", "openid profile"), "user-1")
			q.Set("state", "forged")
			return q
		},
		"unknown code": func(b *browser) url.Values {
			q := f.grant(f.authorize(b, "", "openid profile"), "user-1")
			q.Set("code", "forged")
			return q
		},
		"provider error": func(b *browser) url.Values {
			q := f.authorize(b, "", "openid profile")
			return url.Values{"state": {q.Get("state")}, "error": {"server_error"}}
		},
	}
	for name, mk := range cases {
		b := newBrowser(t, h)
		if loc := b.oidcCallback(mk(b)); loc != "http://example.com/?login=failed" {
			t.Errorf("%s: redirected to %s", name, loc)
		}
		if b.account().SignedIn {
			t.Errorf("%s: signed in", name)
		}
	}

	// The provider's misbehaviour.
	for name, set := range map[string]*bool{"wrong nonce": &f.wrongNonce, "userinfo of another user": &f.wrongUserinfoSub} {
		f.mu.Lock()
		*set = true
		f.mu.Unlock()
		b := newBrowser(t, h)
		if loc := f.signIn(b, "user-1", ""); loc != "http://example.com/?login=failed" {
			t.Errorf("%s: redirected to %s", name, loc)
		}
		if b.account().SignedIn {
			t.Errorf("%s: signed in", name)
		}
		f.mu.Lock()
		*set = false
		f.mu.Unlock()
	}

	// A code works once.
	b := newBrowser(t, h)
	q := f.grant(f.authorize(b, "", "openid profile"), "user-1")
	f.mu.Lock()
	g := f.grants[q.Get("code")]
	f.mu.Unlock()
	b.oidcCallback(q)
	b2 := newBrowser(t, h)
	q2 := f.authorize(b2, "", "openid profile")
	f.mu.Lock()
	f.grants[q.Get("code")] = g // as if the provider accepted it again: the nonce no longer fits
	f.mu.Unlock()
	if loc := b2.oidcCallback(url.Values{"code": {q.Get("code")}, "state": {q2.Get("state")}}); loc != "http://example.com/?login=failed" {
		t.Errorf("replayed code: redirected to %s", loc)
	}

	// Cancelling at the provider returns to the page without a message.
	b = newBrowser(t, h)
	q = f.authorize(b, "?period=year", "openid profile")
	if loc := b.oidcCallback(url.Values{"state": {q.Get("state")}, "error": {"access_denied"}}); loc != "http://example.com/?period=year" {
		t.Errorf("cancel: redirected to %s", loc)
	}
}

// A session of one sign-in method does not count once the site uses the other.
func TestLoginSwitch(t *testing.T) {
	f := newFakeOIDC(t, testClientSecret)
	f.setUser("user-1", map[string]any{"steam_id": testSteamID, "name": "Smith"})
	var srv *Server
	_, sy, h := setup(t, func(s *Server) {
		srv = s
		s.cfg.Login = config.LoginOIDC
		s.cfg.OIDC = config.Default().OIDC
		s.cfg.OIDC.Issuer, s.cfg.OIDC.ClientID, s.cfg.OIDC.ClientSecret = f.URL, testClientID, f.secret
		s.oidc = auth.NewOIDC(s.cfg.OIDC)
		s.oidc.Client = f.Client()
	})
	runSync(t, sy)
	b := newBrowser(t, h)
	f.signIn(b, "user-1", "")
	if !b.account().SignedIn {
		t.Fatal("not signed in")
	}
	srv.cfg.Login = config.LoginSteam // as after a restart with OCAPSTATS_LOGIN=steam
	if b.account().SignedIn {
		t.Error("an OIDC session counts for the Steam sign-in")
	}
	if rec := get(t, h, "/api/auth/oidc", nil); rec.Code != http.StatusNotFound {
		t.Errorf("OIDC sign-in served with login=steam: %d", rec.Code)
	}
	srv.cfg.Login, srv.cfg.OIDC.Issuer = config.LoginOIDC, "https://other-issuer.example.com"
	if b.account().SignedIn {
		t.Error("a session of another issuer counts")
	}
}
