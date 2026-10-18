// Package config loads service settings from a JSON file and OCAPSTATS_* environment variables.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/stats"
)

// Duration is a time.Duration that is written in JSON as a string ("10m", "500ms").
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n float64
		if err2 := json.Unmarshal(b, &n); err2 != nil {
			return err
		}
		*d = Duration(time.Duration(n * float64(time.Second)))
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// D returns the value as time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

type Config struct {
	// Listen is the HTTP listen address.
	Listen string `json:"listen"`
	// DB is the path to the SQLite database file.
	DB string `json:"db"`
	// TrustProxy makes the service trust X-Forwarded-For / X-Forwarded-Proto / X-Real-IP.
	// Enable it only when the service is reachable exclusively through a reverse proxy.
	TrustProxy bool `json:"trustProxy"`
	Log        Log  `json:"log"`
	// AdminToken, when set, authorises POST /api/v1/sync and the admin API with
	// "Authorization: Bearer <token>" (for scripts and webhooks).
	AdminToken string   `json:"adminToken"`
	Admin      Admin    `json:"admin"`
	OCAP       OCAP     `json:"ocap"`
	Sync       Sync     `json:"sync"`
	Stats      Stats    `json:"stats"`
	Rotation   Rotation `json:"rotation"`
	UI         UI       `json:"ui"`
}

// Log configures logging.
type Log struct {
	// Level is debug, info, warn or error.
	Level string `json:"level"`
	// Format is text or json.
	Format string `json:"format"`
}

// Admin configures the admin panel. Username/Password create the first admin user; it is
// created once and its password is then managed in the panel (unless ResetPassword is set).
type Admin struct {
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	ResetPassword bool     `json:"resetPassword"`
	SessionTTL    Duration `json:"sessionTTL"`
}

type OCAP struct {
	// URL is the base URL of the OCAP web server, e.g. https://ocap.example.com
	URL string `json:"url"`
	// DataDir, when set, makes the service read <DataDir>/<filename>.json.gz from disk
	// instead of downloading recordings over HTTP (useful when running next to OCAP).
	DataDir string `json:"dataDir"`
	// Token is an optional Bearer token for OCAP instances that are not in public mode.
	Token   string   `json:"token"`
	Timeout Duration `json:"timeout"`
}

type Sync struct {
	Enabled bool `json:"enabled"`
	// Interval between polls of the OCAP operations list.
	Interval Duration `json:"interval"`
	// Tags limits which recordings are ingested (exact, case-insensitive). Empty = all tags.
	Tags []string `json:"tags"`
	// Since limits ingestion to recordings dated on or after this day (YYYY-MM-DD). Empty = all.
	Since string `json:"since"`
	// Concurrency is the number of recordings downloaded/parsed in parallel.
	Concurrency int `json:"concurrency"`
	// RequestDelay is a pause each worker makes after every download to spare the OCAP server.
	RequestDelay Duration `json:"requestDelay"`
	// MaxAttempts is how many times a failing recording is retried before giving up.
	MaxAttempts int      `json:"maxAttempts"`
	RetryDelay  Duration `json:"retryDelay"`
}

type Stats struct {
	// DefaultTag is used by the API when the request has no "tag" parameter. Empty = all tags.
	DefaultTag string `json:"defaultTag"`
	// MinDuration (seconds) – shorter missions are not counted.
	MinDuration float64 `json:"minDuration"`
	// MinMissions hides players and squads with fewer missions in the selected period from
	// the tables and achievements. 0 shows everyone.
	MinMissions int `json:"minMissions"`
	// SquadPattern is a regexp whose first group is the squad in a nickname; the whole match
	// is removed to get the name.
	SquadPattern string `json:"squadPattern"`
	// SquadAliases maps squad spellings (case-insensitive) to one name.
	SquadAliases map[string]string `json:"squadAliases"`
	// IgnoreNames are extra player names (case-insensitive, without squad prefix) to skip.
	IgnoreNames []string `json:"ignoreNames"`
	// Assists configures kill assists for hitting an enemy shortly before its death.
	Assists stats.Assists `json:"assists"`
}

type Rotation struct {
	// Since is the first day (YYYY-MM-DD) counted in the rotation report.
	Since string         `json:"since"`
	Sides []RotationSide `json:"sides"`
}

type RotationSide struct {
	Name   string   `json:"name"`
	Color  string   `json:"color"`
	Squads []string `json:"squads"`
}

type UI struct {
	Title   string `json:"title"`
	LogoURL string `json:"logoUrl"`
	// OCAPURL is the public OCAP address used for "open replay" links. Defaults to OCAP.URL.
	OCAPURL string `json:"ocapUrl"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Listen: "127.0.0.1:5080",
		DB:     "data/stats.db",
		Admin:  Admin{Username: "admin", SessionTTL: Duration(7 * 24 * time.Hour)},
		Log:    Log{Level: "info", Format: "text"},
		OCAP: OCAP{
			Timeout: Duration(5 * time.Minute),
		},
		Sync: Sync{
			Enabled:      true,
			Interval:     Duration(10 * time.Minute),
			Concurrency:  2,
			RequestDelay: Duration(500 * time.Millisecond),
			MaxAttempts:  5,
			RetryDelay:   Duration(30 * time.Minute),
		},
		Stats: Stats{
			MinDuration:  1200,
			SquadPattern: stats.DefaultSquadPattern,
			Assists:      stats.DefaultAssists(),
		},
		UI: UI{
			Title: "OCAP Statistics",
		},
	}
}

// Load reads the config file (if path is non-empty and exists) on top of the defaults,
// then applies environment overrides.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return cfg, err
		default:
			if err := json.Unmarshal(b, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		}
	}
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func applyEnv(cfg *Config) error {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	str("OCAPSTATS_LISTEN", &cfg.Listen)
	str("OCAPSTATS_DB", &cfg.DB)
	str("OCAPSTATS_ADMIN_TOKEN", &cfg.AdminToken)
	str("OCAPSTATS_LOG_LEVEL", &cfg.Log.Level)
	str("OCAPSTATS_LOG_FORMAT", &cfg.Log.Format)
	str("OCAPSTATS_ADMIN_USERNAME", &cfg.Admin.Username)
	str("OCAPSTATS_ADMIN_PASSWORD", &cfg.Admin.Password)
	str("OCAPSTATS_SQUAD_PATTERN", &cfg.Stats.SquadPattern)
	str("OCAPSTATS_OCAP_URL", &cfg.OCAP.URL)
	str("OCAPSTATS_OCAP_DATA_DIR", &cfg.OCAP.DataDir)
	str("OCAPSTATS_OCAP_TOKEN", &cfg.OCAP.Token)
	str("OCAPSTATS_SYNC_SINCE", &cfg.Sync.Since)
	str("OCAPSTATS_DEFAULT_TAG", &cfg.Stats.DefaultTag)
	str("OCAPSTATS_ROTATION_SINCE", &cfg.Rotation.Since)
	str("OCAPSTATS_UI_TITLE", &cfg.UI.Title)
	str("OCAPSTATS_UI_LOGO_URL", &cfg.UI.LogoURL)
	str("OCAPSTATS_UI_OCAP_URL", &cfg.UI.OCAPURL)

	if v, ok := os.LookupEnv("OCAPSTATS_SYNC_TAGS"); ok {
		cfg.Sync.Tags = splitList(v)
	}
	for key, dst := range map[string]*bool{
		"OCAPSTATS_SYNC_ENABLED":         &cfg.Sync.Enabled,
		"OCAPSTATS_ADMIN_PASSWORD_RESET": &cfg.Admin.ResetPassword,
		"OCAPSTATS_TRUST_PROXY":          &cfg.TrustProxy,
		"OCAPSTATS_HIT_ASSISTS":          &cfg.Stats.Assists.Hits,
	} {
		if v, ok := os.LookupEnv(key); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			*dst = b
		}
	}
	if v, ok := os.LookupEnv("OCAPSTATS_SYNC_CONCURRENCY"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("OCAPSTATS_SYNC_CONCURRENCY: %w", err)
		}
		cfg.Sync.Concurrency = n
	}
	if v, ok := os.LookupEnv("OCAPSTATS_MIN_DURATION"); ok {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("OCAPSTATS_MIN_DURATION: %w", err)
		}
		cfg.Stats.MinDuration = n
	}
	if v, ok := os.LookupEnv("OCAPSTATS_MIN_MISSIONS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("OCAPSTATS_MIN_MISSIONS: %w", err)
		}
		cfg.Stats.MinMissions = n
	}
	if v, ok := os.LookupEnv("OCAPSTATS_ASSIST_WINDOW"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("OCAPSTATS_ASSIST_WINDOW: %w", err)
		}
		cfg.Stats.Assists.Window = n
	}
	for key, dst := range map[string]*Duration{
		"OCAPSTATS_SYNC_INTERVAL":      &cfg.Sync.Interval,
		"OCAPSTATS_SYNC_REQUEST_DELAY": &cfg.Sync.RequestDelay,
		"OCAPSTATS_ADMIN_SESSION_TTL":  &cfg.Admin.SessionTTL,
	} {
		if v, ok := os.LookupEnv(key); ok {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			*dst = Duration(d)
		}
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *Config) validate() error {
	c.OCAP.URL = strings.TrimRight(c.OCAP.URL, "/")
	if c.UI.OCAPURL == "" {
		c.UI.OCAPURL = c.OCAP.URL
	}
	if c.OCAP.URL == "" {
		return errors.New("ocap.url is required (recordings are listed through the OCAP API)")
	}
	if c.Sync.Concurrency < 1 {
		c.Sync.Concurrency = 1
	}
	if c.Sync.Interval.D() < time.Minute {
		c.Sync.Interval = Duration(time.Minute)
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: expected debug, info, warn or error, got %q", c.Log.Level)
	}
	switch strings.ToLower(c.Log.Format) {
	case "text", "json":
	default:
		return fmt.Errorf("log.format: expected text or json, got %q", c.Log.Format)
	}
	if c.Admin.SessionTTL.D() < time.Minute {
		c.Admin.SessionTTL = Duration(7 * 24 * time.Hour)
	}
	if c.Sync.MaxAttempts < 1 {
		c.Sync.MaxAttempts = 1
	}
	for _, d := range []struct{ name, v string }{{"sync.since", c.Sync.Since}, {"rotation.since", c.Rotation.Since}} {
		if d.v == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", d.v); err != nil {
			return fmt.Errorf("%s: expected YYYY-MM-DD, got %q", d.name, d.v)
		}
	}
	return nil
}
