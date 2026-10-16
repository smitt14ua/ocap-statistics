// Package settings holds the statistics settings that can be changed at runtime from the
// admin panel. The config file and environment provide the defaults; once an admin saves
// settings, the saved copy (kept in the database) is used until it is reset.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/config"
	"github.com/smitt14ua/ocap-statistics/internal/stats"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

const metaKey = "settings"

type Settings struct {
	// Tags are the OCAP tags whose recordings are ingested (exact match). Empty = all.
	Tags []string `json:"tags"`
	// DefaultTag is used by the API when a request has no "tag" parameter. Empty = all.
	DefaultTag string `json:"defaultTag"`
	// Since limits ingestion to recordings dated on or after this day. Empty = all.
	Since string `json:"since"`
	// MinDuration (seconds): shorter missions are not counted.
	MinDuration float64         `json:"minDuration"`
	Nickname    stats.Nickname  `json:"nickname"`
	Rotation    config.Rotation `json:"rotation"`
}

// FromConfig builds settings from the config file and environment.
func FromConfig(cfg config.Config) Settings {
	return Settings{
		Tags:        slices.Clone(cfg.Sync.Tags),
		DefaultTag:  cfg.Stats.DefaultTag,
		Since:       cfg.Sync.Since,
		MinDuration: cfg.Stats.MinDuration,
		Nickname: stats.Nickname{
			SquadPattern: cfg.Stats.SquadPattern,
			Aliases:      maps.Clone(cfg.Stats.SquadAliases),
			IgnoreNames:  slices.Clone(cfg.Stats.IgnoreNames),
		},
		Rotation: cfg.Rotation,
	}
}

// Normalize trims values, validates them and compiles the nickname rules.
func (s *Settings) Normalize() (*stats.Rules, error) {
	s.Tags = cleanList(s.Tags)
	s.DefaultTag = strings.TrimSpace(s.DefaultTag)
	s.Since = strings.TrimSpace(s.Since)
	if err := checkDate("since", s.Since); err != nil {
		return nil, err
	}
	if s.MinDuration < 0 {
		return nil, errors.New("minDuration must not be negative")
	}
	s.Nickname.IgnoreNames = cleanList(s.Nickname.IgnoreNames)
	if s.Nickname.Aliases == nil {
		s.Nickname.Aliases = map[string]string{}
	}
	rules, err := stats.NewRules(s.Nickname)
	if err != nil {
		return nil, err
	}
	s.Rotation.Since = strings.TrimSpace(s.Rotation.Since)
	if err := checkDate("rotation.since", s.Rotation.Since); err != nil {
		return nil, err
	}
	for i := range s.Rotation.Sides {
		side := &s.Rotation.Sides[i]
		side.Name = strings.TrimSpace(side.Name)
		side.Color = strings.TrimSpace(side.Color)
		if side.Color != "" && !colorRe.MatchString(side.Color) {
			return nil, fmt.Errorf("rotation side %q: color must be a hex color like #3b82f6", side.Name)
		}
		side.Squads = cleanList(side.Squads)
		if side.Name == "" {
			return nil, fmt.Errorf("rotation side %d: name is required", i+1)
		}
	}
	return rules, nil
}

// colorRe accepts CSS hex colors; colors end up in style attributes of the web page.
var colorRe = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

func cleanList(in []string) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func checkDate(field, v string) error {
	if v == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return fmt.Errorf("%s: expected YYYY-MM-DD", field)
	}
	return nil
}

// Change tells what a settings update affects.
type Change struct {
	// Rules: nickname rules changed, statistics must be recomputed.
	Rules bool `json:"rules"`
	// Sync: the set of ingested recordings changed, a sync is needed.
	Sync bool `json:"sync"`
}

type Manager struct {
	store    *store.Store
	defaults Settings

	mu     sync.RWMutex
	cur    Settings
	rules  *stats.Rules
	custom bool
}

// NewManager loads saved settings, falling back to defaults when none are saved or the saved
// ones are no longer valid.
func NewManager(ctx context.Context, st *store.Store, defaults Settings, log *slog.Logger) (*Manager, error) {
	rules, err := defaults.Normalize()
	if err != nil {
		return nil, fmt.Errorf("settings from config: %w", err)
	}
	m := &Manager{store: st, defaults: defaults, cur: defaults, rules: rules}

	raw, err := st.Meta(ctx, metaKey)
	if err != nil || raw == "" {
		return m, err
	}
	// Start from a deep copy of the defaults so fields missing in the saved JSON keep them.
	var saved Settings
	if b, err := json.Marshal(defaults); err == nil {
		_ = json.Unmarshal(b, &saved)
	}
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		log.Warn("saved settings are unreadable, using config", "err", err)
		return m, nil
	}
	savedRules, err := saved.Normalize()
	if err != nil {
		log.Warn("saved settings are invalid, using config", "err", err)
		return m, nil
	}
	m.cur, m.rules, m.custom = saved, savedRules, true
	return m, nil
}

// Get returns the current settings. Treat the result as read-only.
func (m *Manager) Get() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// Rules returns the compiled nickname rules of the current settings.
func (m *Manager) Rules() *stats.Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules
}

// Defaults returns the settings from the config file and environment.
func (m *Manager) Defaults() Settings { return m.defaults }

// Custom reports whether saved settings override the config.
func (m *Manager) Custom() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.custom
}

// Save validates, stores and applies new settings.
func (m *Manager) Save(ctx context.Context, s Settings) (Change, error) {
	rules, err := s.Normalize()
	if err != nil {
		return Change{}, err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return Change{}, err
	}
	if err := m.store.SetMeta(ctx, metaKey, string(b)); err != nil {
		return Change{}, err
	}
	return m.apply(s, rules, true), nil
}

// Reset drops saved settings and goes back to the config file values.
func (m *Manager) Reset(ctx context.Context) (Change, error) {
	if err := m.store.DeleteMeta(ctx, metaKey); err != nil {
		return Change{}, err
	}
	s := m.defaults
	rules, err := s.Normalize()
	if err != nil {
		return Change{}, err
	}
	return m.apply(s, rules, false), nil
}

func (m *Manager) apply(s Settings, rules *stats.Rules, custom bool) Change {
	m.mu.Lock()
	old := m.cur
	oldKey := m.rules.Key()
	m.cur, m.rules, m.custom = s, rules, custom
	m.mu.Unlock()
	m.store.Bump() // cached API responses depend on the settings
	return Change{
		Rules: rules.Key() != oldKey,
		Sync:  !slices.Equal(old.Tags, s.Tags) || old.Since != s.Since,
	}
}
