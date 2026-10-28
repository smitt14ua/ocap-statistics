// Package api serves the public statistics API, the admin API and the web UI.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/smitt14ua/ocap-statistics/internal/auth"
	"github.com/smitt14ua/ocap-statistics/internal/config"
	"github.com/smitt14ua/ocap-statistics/internal/report"
	"github.com/smitt14ua/ocap-statistics/internal/settings"
	"github.com/smitt14ua/ocap-statistics/internal/store"
	"github.com/smitt14ua/ocap-statistics/internal/syncer"
)

// BuildInfo describes the running binary.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type Server struct {
	cfg      config.Config
	build    BuildInfo
	store    *store.Store
	settings *settings.Manager
	syncer   *syncer.Syncer
	log      *slog.Logger
	web      fs.FS

	responses *responseCache
	results   *resultCache
	limiter   *loginLimiter
	steam     *auth.Steam
	oidc      *auth.OIDC
	// hashSlots bounds concurrent password hashing (CPU-heavy by design).
	hashSlots chan struct{}

	idsMu  sync.Mutex
	ids    *report.Identities // of data generation idsGen
	idsGen int64
	ui     uiStrings // page translations for link previews
}

func New(cfg config.Config, build BuildInfo, st *store.Store, sm *settings.Manager, sy *syncer.Syncer, web fs.FS, log *slog.Logger) *Server {
	return &Server{
		cfg: cfg, build: build, store: st, settings: sm, syncer: sy, web: web, log: log,
		responses: newResponseCache(512, 64<<20),
		results:   newResultCache(32),
		limiter:   newLoginLimiter(),
		steam:     auth.NewSteam(),
		oidc:      auth.NewOIDC(cfg.OIDC),
		hashSlots: make(chan struct{}, 2),
	}
}

// Handler returns the HTTP handler of the whole service.
func (s *Server) Handler() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/config", s.publicConfig)
	mux.HandleFunc("POST /api/v1/sync", s.triggerSync)

	mux.Handle("GET /api/v1/stats", s.cached(s.overview))
	mux.Handle("GET /api/v1/missions", s.cached(s.missions))
	mux.Handle("GET /api/v1/missions/{id}", s.cached(s.mission))
	mux.Handle("GET /api/v1/players", s.cached(s.players))
	mux.Handle("GET /api/v1/players/{name}", s.cached(s.player))
	mux.Handle("GET /api/v1/squads", s.cached(s.squads))
	mux.Handle("GET /api/v1/squads/{name}", s.cached(s.squad))
	mux.Handle("GET /api/v1/squad-matrix", s.cached(s.squadMatrix))
	mux.Handle("GET /api/v1/achievements", s.cached(s.achievements))
	mux.Handle("GET /api/v1/rotation", s.cached(s.rotation))

	s.adminRoutes(mux)
	s.accountRoutes(mux)

	mux.HandleFunc("GET /api/openapi.json", s.openapi)
	mux.HandleFunc("GET /api/docs", s.docs)
	mux.HandleFunc("GET /api/docs.js", s.docsScript)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	if s.web != nil {
		static, err := newStaticHandler(s.web)
		if err != nil {
			return nil, err
		}
		mux.Handle("/", s.sharePage(static))
	}
	return s.withSecurityHeaders(withCORS(withRecover(s.log, mux))), nil
}

// --- errors -----------------------------------------------------------------------------

// apiError is an error with an HTTP status. Code is a stable machine-readable identifier
// (the UI translates it); it defaults to one derived from the status.
type apiError struct {
	status int
	msg    string
	code   string
}

func (e apiError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return apiError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

func notFound(what string) error {
	return apiError{status: http.StatusNotFound, msg: what + " not found"}
}

// withCode returns an apiError with an explicit code.
func withCode(status int, code, msg string) error {
	return apiError{status: status, code: code, msg: msg}
}

var defaultCodes = map[int]string{
	http.StatusBadRequest:            "bad_request",
	http.StatusUnauthorized:          "unauthorized",
	http.StatusForbidden:             "forbidden",
	http.StatusNotFound:              "not_found",
	http.StatusConflict:              "conflict",
	http.StatusRequestEntityTooLarge: "too_large",
	http.StatusUnsupportedMediaType:  "unsupported_media_type",
	http.StatusTooManyRequests:       "too_many_requests",
	http.StatusInternalServerError:   "internal_error",
	http.StatusServiceUnavailable:    "unavailable",
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeErrorCode(w, status, "", msg)
}

// writeErrorCode writes {"error": msg, "code": code}.
func writeErrorCode(w http.ResponseWriter, status int, code, msg string) {
	if code == "" {
		code = defaultCodes[status]
		if code == "" {
			code = "error"
		}
	}
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// fail writes err as an API error response.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae apiError
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &ae):
		writeErrorCode(w, ae.status, ae.code, ae.msg)
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, context.Canceled):
		// The client went away; nobody reads the answer.
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "the request took too long, try again")
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// --- cached statistics endpoints ----------------------------------------------------------

type handlerFunc func(r *http.Request) (response, error)

func jsonResponse(v any) (response, error) {
	b, err := json.Marshal(v)
	return response{contentType: "application/json; charset=utf-8", body: b}, err
}

// cached serves statistics responses from memory until the data changes. Responses carry an
// ETag (conditional requests get 304) and are gzipped for clients that accept it.
func (s *Server) cached(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gen := s.store.Generation()
		key := cacheKey(r.URL.Path, r.URL.Query())
		etag := fmt.Sprintf(`W/"%x-%x"`, uint64(gen), fnv(key))

		resp, ok := s.responses.get(gen, key)
		if !ok {
			var err error
			if resp, err = h(r); err != nil {
				s.fail(w, r, err)
				return
			}
			resp.compress()
			s.responses.put(gen, key, resp)
		}

		hdr := w.Header()
		hdr.Set("ETag", etag)
		hdr.Set("Cache-Control", "public, max-age=60")
		hdr.Add("Vary", "Accept-Encoding")
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		hdr.Set("Content-Type", resp.contentType)
		if resp.filename != "" {
			hdr.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, resp.filename))
		}
		writeBody(w, r, resp.body, resp.gzipped)
	})
}

// writeBody writes the gzipped variant when there is one and the client accepts it.
func writeBody(w http.ResponseWriter, r *http.Request, body, gzipped []byte) {
	if gzipped != nil && acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		body = gzipped
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// acceptsGzip reports whether Accept-Encoding allows gzip (and does not give it q=0).
func acceptsGzip(r *http.Request) bool {
	for part := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			continue
		}
		if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			v, err := strconv.ParseFloat(q, 64)
			return err == nil && v > 0
		}
		return true
	}
	return false
}

// etagMatches implements the weak comparison of If-None-Match.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for tag := range strings.SplitSeq(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == want {
			return true
		}
	}
	return false
}

func fnv(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// --- middleware ---------------------------------------------------------------------------

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The public API may be used from any site; the admin API is same-origin only.
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match")
			w.Header().Set("Access-Control-Expose-Headers", "ETag")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("panic", "method", r.Method, "path", r.URL.Path, "err", v)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders sets headers that apply to every response. Pages add their own
// Content-Security-Policy.
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// isHTTPS reports whether the client connection is HTTPS, trusting X-Forwarded-Proto only
// behind a configured reverse proxy.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clientIP returns the client address, from X-Forwarded-For (the address appended by the
// proxy, i.e. the last one) only behind a configured reverse proxy.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
