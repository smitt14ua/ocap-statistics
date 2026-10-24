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
)

// Players sign in through Steam (OpenID 2.0). A player session only tells the site who is
// looking at it, so the page can highlight their statistics; it grants no other access.
const (
	playerCookie = "ocapstats_player"
	// stateCookie binds Steam's answer to the browser that started the sign-in, so nobody
	// can sign a visitor in to another account (login CSRF).
	stateCookie = "ocapstats_steam_state"
	stateTTL    = 10 * time.Minute
	// maxReturnQuery limits the page query a sign-in returns to.
	maxReturnQuery = 1024
)

func (s *Server) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/steam", s.steamLogin)
	mux.HandleFunc("GET /api/auth/steam/callback", s.steamCallback)
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
	if !s.cfg.Steam.Login {
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
	if !s.cfg.Steam.Login {
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

// playerSteamID returns the Steam id of the signed-in player, or "".
func (s *Server) playerSteamID(r *http.Request) (string, error) {
	token := cookieValue(r, playerCookie)
	if token == "" {
		return "", nil
	}
	id, err := s.store.PlayerSession(r.Context(), auth.HashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// account tells the page who is signed in: the player's own Steam id and the names it played
// under, the most recent first.
func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.cfg.Steam.Login {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	steamID, err := s.playerSteamID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if steamID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"signedIn": false})
		return
	}
	names, err := s.store.SteamNames(r.Context(), steamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signedIn": true, "steamId": steamID, "names": names})
}

func (s *Server) playerLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get(csrfHeader) == "" {
		writeError(w, http.StatusForbidden, "missing "+csrfHeader+" header")
		return
	}
	if token := cookieValue(r, playerCookie); token != "" {
		if err := s.store.DeletePlayerSession(r.Context(), auth.HashToken(token)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.setCookie(w, r, playerCookie, "", 0, http.SameSiteLaxMode)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}
