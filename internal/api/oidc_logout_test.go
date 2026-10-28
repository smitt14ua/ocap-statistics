package api

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const logoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// logoutClaims are the claims of a valid back-channel logout token for sub.
func (f *fakeOIDC) logoutClaims(sub string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": f.URL, "aud": testClientID, "sub": sub, "jti": randomString(),
		"iat": now.Unix(), "exp": now.Add(2 * time.Minute).Unix(),
		"events": map[string]any{logoutEvent: map[string]any{}},
	}
}

// logoutToken signs claims; the token must never reach the site's log either.
func (f *fakeOIDC) logoutToken(claims map[string]any) string {
	tok := f.sign(claims)
	f.mu.Lock()
	f.secrets = append(f.secrets, tok)
	f.mu.Unlock()
	return tok
}

// backchannelLogout posts like the provider's server: a form, no cookies, no CSRF header.
func backchannelLogout(h http.Handler, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/oidc/backchannel-logout", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func logoutForm(token string) string { return url.Values{"logout_token": {token}}.Encode() }

const formType = "application/x-www-form-urlencoded"

func TestOIDCBackchannelLogout(t *testing.T) {
	f := newFakeOIDC(t, testClientSecret)
	h, _ := setupOIDC(t, f, nil)
	f.setUser("user-1", map[string]any{"steam_id": testSteamID, "name": "Smith"})
	f.setUser("user-2", map[string]any{"steam_id": "76561190000000002", "name": "Other"})
	b1, b2, other := newBrowser(t, h), newBrowser(t, h), newBrowser(t, h)
	f.signIn(b1, "user-1", "")
	f.signIn(b2, "user-1", "")
	f.signIn(other, "user-2", "")

	forger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	with := func(change func(map[string]any)) string {
		c := f.logoutClaims("user-1")
		change(c)
		return f.logoutToken(c)
	}
	rejected := map[string]struct{ contentType, body string }{
		"foreign signature": {formType, logoutForm(f.signWith(forger, f.logoutClaims("user-1")))},
		"other audience":    {formType, logoutForm(with(func(c map[string]any) { c["aud"] = "another-client" }))},
		"other issuer":      {formType, logoutForm(with(func(c map[string]any) { c["iss"] = "https://evil.example.com" }))},
		"no events":         {formType, logoutForm(with(func(c map[string]any) { delete(c, "events") }))},
		"other event": {formType, logoutForm(with(func(c map[string]any) {
			c["events"] = map[string]any{"http://schemas.openid.net/event/other": map[string]any{}}
		}))},
		"nonce (an ID token)": {formType, logoutForm(with(func(c map[string]any) { c["nonce"] = "n" }))},
		"no sub or sid":       {formType, logoutForm(with(func(c map[string]any) { delete(c, "sub") }))},
		"stale iat": {formType, logoutForm(with(func(c map[string]any) {
			c["iat"] = time.Now().Add(-time.Hour).Unix()
		}))},
		"no iat":          {formType, logoutForm(with(func(c map[string]any) { delete(c, "iat") }))},
		"expired":         {formType, logoutForm(with(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }))},
		"not a JWT":       {formType, logoutForm("not-a-token")},
		"no logout_token": {formType, ""},
		"JSON body":       {"application/json", `{"logout_token":"` + f.logoutToken(f.logoutClaims("user-1")) + `"}`},
	}
	for name, req := range rejected {
		rec := backchannelLogout(h, req.contentType, req.body)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d, Cache-Control %q", name, rec.Code, rec.Header().Get("Cache-Control"))
		}
		if !b1.account().SignedIn {
			t.Fatalf("%s: ended the session", name)
		}
	}

	// A valid token ends every session of that user, and only theirs.
	token := f.logoutToken(f.logoutClaims("user-1"))
	rec := backchannelLogout(h, formType, logoutForm(token))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout: %d %s, Cache-Control %q", rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
	}
	if b1.account().SignedIn || b2.account().SignedIn {
		t.Error("a session of the user survived the back-channel logout")
	}
	if !other.account().SignedIn {
		t.Error("the back-channel logout ended another user's session")
	}
	// The same token is not processed twice.
	if rec := backchannelLogout(h, formType, logoutForm(token)); rec.Code != http.StatusBadRequest {
		t.Errorf("replayed token: %d", rec.Code)
	}

	// With only sid, the sessions of that provider session end.
	second := newBrowser(t, h)
	f.signIn(second, "user-2", "")
	c := f.logoutClaims("")
	delete(c, "sub")
	f.mu.Lock()
	c["sid"] = f.lastSID
	f.mu.Unlock()
	if rec := backchannelLogout(h, formType, logoutForm(f.logoutToken(c))); rec.Code != http.StatusOK {
		t.Fatalf("logout by sid: %d %s", rec.Code, rec.Body)
	}
	if second.account().SignedIn {
		t.Error("the session of the sid survived")
	}
	if !other.account().SignedIn {
		t.Error("logout by sid ended another provider session of the user")
	}
}

func TestOIDCBackchannelLogoutDisabled(t *testing.T) {
	_, _, h := setup(t)
	if rec := backchannelLogout(h, formType, logoutForm("x")); rec.Code != http.StatusNotFound {
		t.Errorf("without login=oidc: %d", rec.Code)
	}
}
