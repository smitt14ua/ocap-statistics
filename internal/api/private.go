package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/smitt14ua/ocap-statistics/internal/auth"
)

// The private API answers trusted services (a community bot, a forum) with what the public
// API never shows: statistics looked up by Steam id. It needs config.APIToken (or the admin
// token) as a Bearer token, is same-origin only (no CORS headers) and not in openapi.json.

func (s *Server) privateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/private/v1/players/{steamId}", s.requireAPIToken(s.steamPlayer))
}

// requireAPIToken accepts the API token or the admin token. Without either configured the
// private API does not exist (404).
func (s *Server) requireAPIToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.cfg.APIToken == "" && s.cfg.AdminToken == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		valid := ok && s.cfg.APIToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.APIToken)) == 1
		if !valid && !s.validAdminToken(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ocap-stats"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// steamPlayer returns the profile of the player a Steam account is merged into, exactly as
// /api/v1/players/{name} does for that player: the missions played with this Steam id, under
// any name, and the older missions without Steam ids under a name only this account used
// (report.Identities). It adds the Steam id and the names the account played under.
func (s *Server) steamPlayer(w http.ResponseWriter, r *http.Request) {
	steamID := r.PathValue("steamId")
	if !auth.ValidSteamID(steamID) {
		s.fail(w, r, badRequest("steamId must be a SteamID64 (17 digits)"))
		return
	}
	f, err := s.filter(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()
	b, err := s.report(ctx, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	name, ok := b.ids.Player(steamID)
	if !ok {
		s.fail(w, r, withCode(http.StatusNotFound, "unknown_steam_id", "no processed mission was played with this Steam id"))
		return
	}
	profile, ok, err := s.playerProfile(ctx, f, b, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		s.fail(w, r, withCode(http.StatusNotFound, "no_missions", "the player has no missions in this period"))
		return
	}
	names, err := s.store.SteamNames(ctx, steamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	profile["steamId"], profile["names"] = steamID, names
	writeJSON(w, http.StatusOK, profile)
}
