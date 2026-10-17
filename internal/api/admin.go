package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/auth"
	"github.com/smitt14ua/ocap-statistics/internal/formula"
	"github.com/smitt14ua/ocap-statistics/internal/report"
	"github.com/smitt14ua/ocap-statistics/internal/settings"
	"github.com/smitt14ua/ocap-statistics/internal/stats"
	"github.com/smitt14ua/ocap-statistics/internal/store"
	"github.com/smitt14ua/ocap-statistics/internal/syncer"
)

const (
	// sessionCookie is the session cookie name. Over HTTPS the "__Host-" prefix makes the
	// browser reject the cookie unless it is Secure, host-only and scoped to "/".
	sessionCookie       = "ocapstats_session"
	secureSessionCookie = "__Host-" + sessionCookie
	// csrfHeader must accompany state-changing admin requests authenticated by cookie. A
	// cross-site page cannot set it without a CORS preflight, which the admin API never allows.
	csrfHeader = "X-Requested-With"
	// maxJSONBody limits admin request bodies.
	maxJSONBody = 1 << 20
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/admin/login", s.login)
	mux.HandleFunc("POST /api/admin/logout", s.logout)
	mux.HandleFunc("GET /api/admin/me", s.requireAdmin(s.me))
	mux.HandleFunc("GET /api/admin/state", s.requireAdmin(s.adminState))
	mux.HandleFunc("PUT /api/admin/settings", s.requireAdmin(s.saveSettings))
	mux.HandleFunc("DELETE /api/admin/settings", s.requireAdmin(s.resetSettings))
	mux.HandleFunc("POST /api/admin/preview-names", s.requireAdmin(s.previewNames))
	mux.HandleFunc("POST /api/admin/preview-column", s.requireAdmin(s.previewColumn))
	mux.HandleFunc("POST /api/admin/tasks/{task}", s.requireAdmin(s.startTask))
	mux.HandleFunc("GET /api/admin/users", s.requireAdmin(s.listUsers))
	mux.HandleFunc("POST /api/admin/users", s.requireAdmin(s.createUser))
	mux.HandleFunc("DELETE /api/admin/users/{name}", s.requireAdmin(s.deleteUser))
	mux.HandleFunc("PUT /api/admin/users/{name}/password", s.requireAdmin(s.setPassword))
}

type userKey struct{}

func adminUser(r *http.Request) string {
	u, _ := r.Context().Value(userKey{}).(string)
	return u
}

func (s *Server) validAdminToken(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && s.cfg.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AdminToken)) == 1
}

// sessionToken returns the session token sent by the browser, if any.
func sessionToken(r *http.Request) string {
	for _, name := range []string{secureSessionCookie, sessionCookie} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// setSessionCookie sets (or, with an empty token, clears) the session cookie.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	secure := s.isHTTPS(r)
	name := sessionCookie
	if secure {
		name = secureSessionCookie
	}
	maxAge := int(ttl.Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

// requireAdmin accepts a session cookie or the admin token.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.validAdminToken(r) {
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, "token")))
			return
		}
		token := sessionToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		user, err := s.store.SessionUser(r.Context(), auth.HashToken(token))
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				s.log.Error("session lookup", "err", err)
			}
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get(csrfHeader) == "" {
			writeError(w, http.StatusForbidden, "missing "+csrfHeader+" header")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	}
}

// decodeJSON decodes a JSON request body of at most maxJSONBody bytes into dst.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return apiError{status: http.StatusUnsupportedMediaType, msg: "expected application/json"}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return err
		}
		return badRequest("invalid JSON: %v", err)
	}
	return nil
}

// --- login --------------------------------------------------------------------------

// loginLimiter slows down password guessing. Failures are counted per client address and per
// user name; the per-name limit is higher, so one attacker cannot easily lock an admin out.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string]*loginFails
}

type loginFails struct {
	count int
	until time.Time
}

const (
	maxLoginFailsPerIP   = 10
	maxLoginFailsPerUser = 50
	loginLockout         = 15 * time.Minute
)

func newLoginLimiter() *loginLimiter { return &loginLimiter{fails: map[string]*loginFails{}} }

func limiterKeys(ip, user string) (ipKey, userKey string) {
	return "ip:" + ip, "user:" + strings.ToLower(user)
}

func (l *loginLimiter) locked(ip, user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	ipKey, userKey := limiterKeys(ip, user)
	over := func(key string, limit int) bool {
		f := l.fails[key]
		return f != nil && f.count >= limit && now.Before(f.until)
	}
	return over(ipKey, maxLoginFailsPerIP) || over(userKey, maxLoginFailsPerUser)
}

func (l *loginLimiter) fail(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	ipKey, userKey := limiterKeys(ip, user)
	for _, key := range []string{ipKey, userKey} {
		f := l.fails[key]
		if f == nil || now.After(f.until) {
			f = &loginFails{}
			l.fails[key] = f
		}
		f.count++
		f.until = now.Add(loginLockout)
	}
	// Forget expired entries so the map cannot grow without bound.
	if len(l.fails) > 10_000 {
		for k, f := range l.fails {
			if now.After(f.until) {
				delete(l.fails, k)
			}
		}
	}
}

func (l *loginLimiter) reset(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ipKey, userKey := limiterKeys(ip, user)
	delete(l.fails, ipKey)
	delete(l.fails, userKey)
}

var dummyHash = sync.OnceValue(func() string {
	h, _ := auth.HashPassword("timing-equaliser")
	return h
})

// checkPassword verifies a password while limiting how many hashes run at once.
func (s *Server) checkPassword(ctx context.Context, hash, password string) (bool, error) {
	select {
	case s.hashSlots <- struct{}{}:
		defer func() { <-s.hashSlots }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return auth.CheckPassword(hash, password), nil
}

// hashPassword hashes a password while limiting how many hashes run at once.
func (s *Server) hashPassword(ctx context.Context, password string) (string, error) {
	select {
	case s.hashSlots <- struct{}{}:
		defer func() { <-s.hashSlots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return auth.HashPassword(password)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	ip := s.clientIP(r)
	if s.limiter.locked(ip, req.Username) {
		writeErrorCode(w, http.StatusTooManyRequests, "too_many_attempts", "too many failed attempts, try again later")
		return
	}
	name, hash, lookupErr := s.store.PasswordHash(r.Context(), req.Username)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		s.fail(w, r, lookupErr)
		return
	}
	if lookupErr != nil {
		hash = dummyHash() // spend the same time for unknown users
	}
	ok, err := s.checkPassword(r.Context(), hash, req.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok || lookupErr != nil {
		s.limiter.fail(ip, req.Username)
		s.log.Warn("admin login failed", "user", req.Username, "ip", ip)
		writeErrorCode(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	s.limiter.reset(ip, req.Username)

	token, tokenHash, err := auth.NewToken()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ttl := s.cfg.Admin.SessionTTL.D()
	if err := s.store.CreateSession(r.Context(), tokenHash, name, time.Now().Add(ttl)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.setSessionCookie(w, r, token, ttl)
	s.log.Info("admin login", "user", name, "ip", ip)
	writeJSON(w, http.StatusOK, map[string]string{"username": name})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := sessionToken(r); token != "" {
		_ = s.store.DeleteSession(r.Context(), auth.HashToken(token))
	}
	s.setSessionCookie(w, r, "", 0)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": adminUser(r)})
}

// --- state and settings ---------------------------------------------------------------

func (s *Server) adminState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts, err := s.store.Counts(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	failed, err := s.store.FailedMissions(ctx, 50)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":     adminUser(r),
		"missions": counts,
		"sync":     s.syncer.Status(),
		"failed":   failed,
		"settings": s.settings.Get(),
		"defaults": s.settings.Defaults(),
		"custom":   s.settings.Custom(),
		"service": map[string]any{
			"ocapUrl":      s.cfg.OCAP.URL,
			"dataDir":      s.cfg.OCAP.DataDir,
			"syncEnabled":  s.cfg.Sync.Enabled,
			"syncInterval": s.cfg.Sync.Interval.D().String(),
			"concurrency":  s.cfg.Sync.Concurrency,
		},
	})
}

// applyChange queues the work a settings change needs.
func (s *Server) applyChange(c settings.Change) {
	if c.Rules {
		s.syncer.Request(syncer.TaskRecompute)
	}
	if c.Sync {
		s.syncer.Request(syncer.TaskSync)
	}
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var set settings.Settings
	if err := decodeJSON(w, r, &set); err != nil {
		s.fail(w, r, err)
		return
	}
	change, err := s.settings.Save(r.Context(), set)
	if err != nil {
		var ae apiError
		if !errors.As(err, &ae) {
			err = withCode(http.StatusBadRequest, "invalid_settings", err.Error()) // validation errors
		}
		s.fail(w, r, err)
		return
	}
	s.applyChange(change)
	s.log.Info("settings saved", "user", adminUser(r), "recompute", change.Rules, "sync", change.Sync)
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.settings.Get(), "change": change})
}

func (s *Server) resetSettings(w http.ResponseWriter, r *http.Request) {
	change, err := s.settings.Reset(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.applyChange(change)
	s.log.Info("settings reset", "user", adminUser(r))
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.settings.Get(), "change": change})
}

// previewNames shows how nicknames would be parsed with a nickname format. Without names
// it uses recent nicknames from the database.
func (s *Server) previewNames(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nickname stats.Nickname `json:"nickname"`
		Names    []string       `json:"names"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	rules, err := stats.NewRules(req.Nickname)
	if err != nil {
		s.fail(w, r, withCode(http.StatusBadRequest, "invalid_settings", err.Error()))
		return
	}
	names := req.Names
	if len(names) == 0 {
		if names, err = s.store.RecentRawNames(r.Context(), 300); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	type parsed struct {
		Raw      string `json:"raw"`
		Name     string `json:"name"`
		SquadRaw string `json:"squadRaw"`
		Squad    string `json:"squad"`
		Counted  bool   `json:"counted"`
	}
	out := make([]parsed, 0, len(names))
	for _, raw := range names {
		name, squadRaw, squad := rules.ParseName(raw)
		out = append(out, parsed{raw, name, squadRaw, squad, rules.ValidPlayer(name)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": out})
}

// previewColumn evaluates a formula over all-time statistics of the default tag, for the
// top players (or squads) by kills.
func (s *Server) previewColumn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Formula  string `json:"formula"`
		Squads   bool   `json:"squads"`
		Decimals int    `json:"decimals"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	expr, err := report.CompileFormula(req.Formula)
	if err != nil {
		var fe *formula.Error
		pos := -1
		if errors.As(err, &fe) {
			pos = fe.Pos
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "invalid_formula", "position": pos})
		return
	}
	set := s.settings.Get()
	b, err := s.report(r.Context(), store.Filter{From: "0000-01-01", To: "9999-12-31", Tag: set.DefaultTag, MinDuration: set.MinDuration})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": b.res.Preview(expr, req.Squads, min(max(req.Decimals, 0), 6), 10)})
}

func (s *Server) startTask(w http.ResponseWriter, r *http.Request) {
	task := r.PathValue("task")
	switch task {
	case string(syncer.TaskSync), string(syncer.TaskRecompute), string(syncer.TaskResync):
		s.syncer.Request(syncer.Task(task))
	case "retry-errors":
		if _, err := s.store.RetryErrors(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		s.syncer.Request(syncer.TaskSync)
	default:
		writeError(w, http.StatusNotFound, "unknown task")
		return
	}
	s.log.Info("admin task queued", "task", task, "user", adminUser(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": task, "sync": s.syncer.Status()})
}

// --- users -----------------------------------------------------------------------------

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := auth.ValidateUsername(req.Username); err != nil {
		s.fail(w, r, withCode(http.StatusBadRequest, "invalid_username", err.Error()))
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		s.fail(w, r, withCode(http.StatusBadRequest, "password_too_short", err.Error()))
		return
	}
	hash, err := s.hashPassword(r.Context(), req.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.CreateUser(r.Context(), req.Username, hash); errors.Is(err, store.ErrUserExists) {
		s.fail(w, r, withCode(http.StatusConflict, "user_exists", "user already exists"))
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("admin user created", "user", req.Username, "by", adminUser(r))
	writeJSON(w, http.StatusCreated, map[string]string{"username": req.Username})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.EqualFold(name, adminUser(r)) {
		s.fail(w, r, withCode(http.StatusBadRequest, "cannot_delete_self", "you cannot delete yourself"))
		return
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(users) <= 1 {
		s.fail(w, r, withCode(http.StatusBadRequest, "cannot_delete_last_user", "the last user cannot be deleted"))
		return
	}
	if err := s.store.DeleteUser(r.Context(), name); err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("admin user deleted", "user", name, "by", adminUser(r))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		s.fail(w, r, withCode(http.StatusBadRequest, "password_too_short", err.Error()))
		return
	}
	hash, err := s.hashPassword(r.Context(), req.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	name := r.PathValue("name")
	if err := s.store.SetPassword(r.Context(), name, hash); err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("admin password changed", "user", name, "by", adminUser(r))
	w.WriteHeader(http.StatusNoContent)
}
