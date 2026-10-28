package api

import (
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/smitt14ua/ocap-statistics/internal/auth"
	"github.com/smitt14ua/ocap-statistics/internal/config"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// Players sign in through Steam (OpenID 2.0) or an OpenID Connect provider that knows their
// Steam account (config.Login). A player session only tells the site who is looking at it,
// so the page can highlight their statistics; it grants no other access.
const (
	playerCookie = "ocapstats_player"
	// stateCookie binds Steam's answer to the browser that started the sign-in, so nobody
	// can sign a visitor in to another account (login CSRF).
	stateCookie = "ocapstats_steam_state"
	// oidcStateCookie does the same for the OpenID Connect sign-in and keeps its nonce and
	// PKCE verifier.
	oidcStateCookie = "ocapstats_oidc_state"
	stateTTL        = 10 * time.Minute
	// maxReturnQuery limits the page query a sign-in returns to.
	maxReturnQuery = 1024
)

func (s *Server) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/steam", s.steamLogin)
	mux.HandleFunc("GET /api/auth/steam/callback", s.steamCallback)
	mux.HandleFunc("GET /api/auth/oidc", s.oidcLogin)
	mux.HandleFunc("GET /api/auth/oidc/callback", s.oidcCallback)
	mux.HandleFunc("GET /api/auth/me", s.account)
	mux.HandleFunc("POST /api/auth/logout", s.playerLogout)
}

// siteURL is the address of the site as the browser sees it, without a trailing slash.
func (s *Server) siteURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if s.cfg.TrustProxy {
		if h, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ","); strings.TrimSpace(h) != "" {
			host = strings.TrimSpace(h)
		}
	}
	return scheme + "://" + host
}

// setCookie sets (or, with an empty value, clears) an HttpOnly cookie for the whole site;
// over HTTPS it gets the "__Host-" prefix.
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, name, value string, ttl time.Duration, sameSite http.SameSite) {
	secure := s.isHTTPS(r)
	if secure {
		name = "__Host-" + name
	}
	maxAge := int(ttl.Seconds())
	if value == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: sameSite,
	})
}

func cookieValue(r *http.Request, name string) string {
	for _, n := range []string{"__Host-" + name, name} {
		if c, err := r.Cookie(n); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// returnQuery accepts the query of the page to come back to ("?period=year"), nothing else,
// so the sign-in cannot redirect anywhere but the site.
func returnQuery(v string) string {
	if v == "" || v[0] != '?' || len(v) > maxReturnQuery || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return ""
	}
	return v
}

// steamLogin sends the browser to Steam.
func (s *Server) steamLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Login != config.LoginSteam {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	state, _, err := auth.NewToken()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ret := base64.RawURLEncoding.EncodeToString([]byte(returnQuery(r.URL.Query().Get("return"))))
	// Lax: the cookie must come back with Steam's cross-site redirect to the callback.
	s.setCookie(w, r, stateCookie, state+"."+ret, stateTTL, http.SameSiteLaxMode)
	site := s.siteURL(r)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.steam.LoginURL(site+"/api/auth/steam/callback?state="+state, site+"/"), http.StatusFound)
}

// steamCallback receives Steam's answer, verifies it and signs the player in.
func (s *Server) steamCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Login != config.LoginSteam {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	site := s.siteURL(r)
	cookieState, encodedRet, _ := strings.Cut(cookieValue(r, stateCookie), ".")
	s.setCookie(w, r, stateCookie, "", 0, http.SameSiteLaxMode) // one sign-in per state
	ret, _ := base64.RawURLEncoding.DecodeString(encodedRet)
	back := site + "/" + returnQuery(string(ret))

	state := r.URL.Query().Get("state")
	if cookieState == "" || subtle.ConstantTimeCompare([]byte(cookieState), []byte(state)) != 1 {
		s.log.Warn("steam sign-in: state does not match", "ip", s.clientIP(r))
		http.Redirect(w, r, site+"/?login=failed", http.StatusSeeOther)
		return
	}
	steamID, err := s.steam.Verify(r.Context(), r.URL.Query(), site+"/api/auth/steam/callback?state="+state)
	if errors.Is(err, auth.ErrSteamCancelled) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		s.log.Warn("steam sign-in rejected", "ip", s.clientIP(r), "err", err)
		http.Redirect(w, r, site+"/?login=failed", http.StatusSeeOther)
		return
	}
	token, hash, err := auth.NewToken()
	if err == nil {
		err = s.store.CreatePlayerSession(r.Context(), hash, steamID, time.Now().Add(s.cfg.Steam.SessionTTL.D()))
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setCookie(w, r, playerCookie, token, s.cfg.Steam.SessionTTL.D(), http.SameSiteLaxMode)
	s.log.Info("player signed in through Steam")
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// playerSession returns the signed-in player. Sessions of a sign-in method (or an OpenID
// Connect issuer) that is no longer configured do not count.
func (s *Server) playerSession(r *http.Request) (store.PlayerSession, bool, error) {
	token := cookieValue(r, playerCookie)
	if token == "" {
		return store.PlayerSession{}, false, nil
	}
	a, err := s.store.PlayerSession(r.Context(), auth.HashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	switch s.cfg.Login {
	case config.LoginSteam:
		return a, a.Issuer == "", nil
	case config.LoginOIDC:
		return a, a.Issuer == s.cfg.OIDC.Issuer, nil
	}
	return a, false, nil
}

// account tells the page who is signed in: the player's own Steam id and the names it played
// under, the most recent first; for the OpenID Connect sign-in also the provider's nickname
// and avatar. The Steam id is empty when the user has not linked Steam at the provider.
func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.cfg.Login == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	a, ok, err := s.playerSession(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"signedIn": false})
		return
	}
	names := []store.SteamName{}
	if a.SteamID != "" {
		if names, err = s.store.SteamNames(r.Context(), a.SteamID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	resp := map[string]any{"signedIn": true, "steamId": a.SteamID, "names": names}
	if s.cfg.Login == config.LoginOIDC {
		resp["name"], resp["picture"] = a.Name, a.Picture
	}
	writeJSON(w, http.StatusOK, resp)
}

// playerLogout ends the session; after the OpenID Connect sign-in the answer names the
// provider's logout page for the browser to go to.
func (s *Server) playerLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get(csrfHeader) == "" {
		writeError(w, http.StatusForbidden, "missing "+csrfHeader+" header")
		return
	}
	resp := map[string]string{"status": "signed out"}
	if token := cookieValue(r, playerCookie); token != "" {
		a, ok, err := s.playerSession(r)
		if err == nil {
			err = s.store.DeletePlayerSession(r.Context(), auth.HashToken(token))
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if ok && a.Issuer != "" {
			u, err := s.oidc.LogoutURL(r.Context(), a.IDToken, s.siteURL(r)+"/")
			if err != nil {
				s.log.Warn("oidc logout: provider unavailable", "err", err)
			} else if u != "" {
				resp["redirect"] = u
			}
		}
	}
	s.setCookie(w, r, playerCookie, "", 0, http.SameSiteLaxMode)
	writeJSON(w, http.StatusOK, resp)
}

// --- OpenID Connect ---------------------------------------------------------------------

// oidcRedirectURI is the callback registered at the provider; it has no query, as providers
// compare it exactly.
func (s *Server) oidcRedirectURI(r *http.Request) string {
	return s.siteURL(r) + "/api/auth/oidc/callback"
}

// oidcLogin sends the browser to the provider's sign-in page.
func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Login != config.LoginOIDC {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	l, err := auth.NewOIDCLogin()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.oidc.AuthURL(r.Context(), l, s.oidcRedirectURI(r))
	if err != nil {
		s.log.Warn("oidc sign-in: provider unavailable", "err", err)
		http.Redirect(w, r, s.siteURL(r)+"/?login=failed", http.StatusSeeOther)
		return
	}
	ret := base64.RawURLEncoding.EncodeToString([]byte(returnQuery(r.URL.Query().Get("return"))))
	// State, nonce and PKCE verifier are all URL-safe base64. Lax: the cookie must come back
	// with the provider's cross-site redirect to the callback.
	s.setCookie(w, r, oidcStateCookie, strings.Join([]string{l.State, l.Nonce, l.Verifier, ret}, "."), stateTTL, http.SameSiteLaxMode)
	http.Redirect(w, r, u, http.StatusFound)
}

// oidcCallback receives the authorization code, redeems it and signs the player in. The
// code, the tokens and the profile are never logged.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Login != config.LoginOIDC {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	site := s.siteURL(r)
	failed := site + "/?login=failed"
	parts := strings.Split(cookieValue(r, oidcStateCookie), ".")
	s.setCookie(w, r, oidcStateCookie, "", 0, http.SameSiteLaxMode) // one sign-in per state
	if len(parts) != 4 {
		parts = []string{"", "", "", ""}
	}
	l := auth.OIDCLogin{State: parts[0], Nonce: parts[1], Verifier: parts[2]}
	ret, _ := base64.RawURLEncoding.DecodeString(parts[3])
	back := site + "/" + returnQuery(string(ret))

	q := r.URL.Query()
	if l.State == "" || subtle.ConstantTimeCompare([]byte(l.State), []byte(q.Get("state"))) != 1 {
		s.log.Warn("oidc sign-in: state does not match", "ip", s.clientIP(r))
		http.Redirect(w, r, failed, http.StatusSeeOther)
		return
	}
	if e := q.Get("error"); e != "" || q.Get("code") == "" {
		if e == "access_denied" { // the user cancelled at the provider
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
		s.log.Warn("oidc sign-in: provider returned an error", "error", truncate(e, 100), "description", truncate(q.Get("error_description"), 300))
		http.Redirect(w, r, failed, http.StatusSeeOther)
		return
	}
	prof, err := s.oidc.Exchange(r.Context(), l, s.oidcRedirectURI(r), q.Get("code"))
	requested := strings.Join(s.cfg.OIDC.Scopes, " ")
	if ce, ok := errors.AsType[*auth.ClaimsError](err); ok {
		s.log.Warn("oidc sign-in: the provider shared no profile; check the scopes the client may use",
			"missing", ce.Missing, "requested", requested, "granted", grantedScope(ce.Granted), "dropped", ce.Dropped)
		http.Redirect(w, r, site+"/?login=claims", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.log.Warn("oidc sign-in rejected", "ip", s.clientIP(r), "err", err)
		http.Redirect(w, r, failed, http.StatusSeeOther)
		return
	}
	if len(prof.Dropped) > 0 {
		s.log.Warn("oidc sign-in: the provider granted fewer scopes than requested",
			"requested", requested, "granted", grantedScope(prof.Granted), "dropped", prof.Dropped)
	}
	token, hash, err := auth.NewToken()
	if err == nil {
		err = s.store.SaveOIDCAccount(r.Context(), hash, store.PlayerSession{
			Issuer: prof.Issuer, Subject: prof.Subject, SteamID: prof.SteamID,
			Name: prof.Name, Picture: prof.Picture, IDToken: prof.IDToken,
		}, time.Now().Add(s.cfg.Steam.SessionTTL.D()))
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setCookie(w, r, playerCookie, token, s.cfg.Steam.SessionTTL.D(), http.SameSiteLaxMode)
	if prof.SteamID == "" {
		s.log.Info("player signed in through OpenID Connect without a linked Steam account",
			"claim", s.cfg.OIDC.SteamIDClaim, "granted", grantedScope(prof.Granted))
		if strings.Contains(back, "?") {
			back += "&login=nosteam"
		} else {
			back += "?login=nosteam"
		}
	} else {
		s.log.Info("player signed in through OpenID Connect")
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// grantedScope is the scope the token endpoint reported, for logs.
func grantedScope(granted string) string {
	if granted == "" {
		return "(not reported: as requested)"
	}
	return granted
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
