// Package store keeps recordings, their digests and computed statistics in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/smitt14ua/ocap-statistics/internal/stats"
)

// Mission statuses.
const (
	StatusPending = "pending"
	StatusDone    = "done"
	StatusError   = "error"
	// StatusMissing marks recordings whose data file OCAP no longer has. They are not
	// retried automatically.
	StatusMissing = "missing"
)

const schema = `
CREATE TABLE IF NOT EXISTS missions (
	id              INTEGER PRIMARY KEY, -- OCAP operation id
	filename        TEXT    NOT NULL,
	mission_name    TEXT    NOT NULL DEFAULT '',
	world_name      TEXT    NOT NULL DEFAULT '',
	date            TEXT    NOT NULL DEFAULT '',
	tag             TEXT    NOT NULL DEFAULT '',
	ocap_duration   REAL    NOT NULL DEFAULT 0,
	status          TEXT    NOT NULL DEFAULT 'pending',
	attempts        INTEGER NOT NULL DEFAULT 0,
	last_error      TEXT    NOT NULL DEFAULT '',
	next_attempt_at INTEGER NOT NULL DEFAULT 0,
	digest          BLOB,
	digest_version  INTEGER NOT NULL DEFAULT 0,
	compute_key     TEXT    NOT NULL DEFAULT '', -- stats.Rules.Key() used to compute the statistics
	author          TEXT    NOT NULL DEFAULT '',
	duration        REAL    NOT NULL DEFAULT 0,
	end_side        TEXT    NOT NULL DEFAULT '',
	end_message     TEXT    NOT NULL DEFAULT '',
	player_count    INTEGER NOT NULL DEFAULT 0,
	fetched_at      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_missions_date ON missions(date);
CREATE INDEX IF NOT EXISTS idx_missions_status ON missions(status);

CREATE TABLE IF NOT EXISTS mission_players (
	mission_id    INTEGER NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	name          TEXT    NOT NULL,
	raw_name      TEXT    NOT NULL,
	squad_raw     TEXT    NOT NULL,
	squad         TEXT    NOT NULL,
	side          TEXT    NOT NULL,
	kills         INTEGER NOT NULL,
	deaths        INTEGER NOT NULL,
	team_kills    INTEGER NOT NULL,
	vehicle_kills INTEGER NOT NULL,
	shots         INTEGER NOT NULL,
	hits          INTEGER NOT NULL,
	PRIMARY KEY (mission_id, name)
);
CREATE INDEX IF NOT EXISTS idx_mp_name ON mission_players(name);

CREATE TABLE IF NOT EXISTS kills (
	mission_id       INTEGER NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	frame            INTEGER NOT NULL,
	killer           TEXT    NOT NULL,
	killer_squad     TEXT    NOT NULL,
	killer_side      TEXT    NOT NULL,
	killer_is_player INTEGER NOT NULL,
	victim           TEXT    NOT NULL,
	victim_squad     TEXT    NOT NULL,
	victim_side      TEXT    NOT NULL,
	victim_kind      TEXT    NOT NULL,
	vehicle_class    TEXT    NOT NULL,
	weapon           TEXT    NOT NULL,
	distance         REAL    NOT NULL,
	team_kill        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_kills_mission ON kills(mission_id);
CREATE INDEX IF NOT EXISTS idx_kills_killer ON kills(killer);
CREATE INDEX IF NOT EXISTS idx_kills_victim ON kills(victim);

CREATE TABLE IF NOT EXISTS users (
	username      TEXT PRIMARY KEY COLLATE NOCASE,
	password_hash TEXT    NOT NULL,
	created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	username   TEXT    NOT NULL REFERENCES users(username) ON DELETE CASCADE ON UPDATE CASCADE,
	expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

type Store struct {
	db *sql.DB
	// generation increases on every change of statistics; API caches are keyed by it.
	generation atomic.Int64
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	// _txlock=immediate takes the write lock when a transaction starts, so concurrent writers
	// wait on busy_timeout instead of failing with SQLITE_BUSY when upgrading a read lock.
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db}
	s.generation.Store(time.Now().UnixNano())
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Generation identifies the current state of the statistics.
func (s *Store) Generation() int64 { return s.generation.Load() }

// Bump marks the statistics as changed (invalidates API caches).
func (s *Store) Bump() { s.generation.Add(1) }

// OperationMeta is recording metadata as listed by OCAP.
type OperationMeta struct {
	ID           int64
	Filename     string
	MissionName  string
	WorldName    string
	Date         string
	Tag          string
	OCAPDuration float64
}

// UpsertOperations inserts new recordings as pending and refreshes metadata of known ones.
// It returns the number of newly inserted recordings.
func (s *Store) UpsertOperations(ctx context.Context, ops []OperationMeta) (int, error) {
	known, err := s.operationMetas(ctx)
	if err != nil {
		return 0, err
	}
	var inserts, refetches, updates []OperationMeta
	for _, op := range ops {
		old, ok := known[op.ID]
		switch {
		case !ok:
			inserts = append(inserts, op)
		case old.Filename != op.Filename:
			refetches = append(refetches, op) // a different file under the same id
		case old.MissionName != op.MissionName || old.WorldName != op.WorldName || old.Date != op.Date || old.Tag != op.Tag:
			updates = append(updates, op)
		}
	}
	if len(inserts)+len(refetches)+len(updates) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []struct {
		sql string
		ops []OperationMeta
	}{
		{`INSERT INTO missions (mission_name, world_name, date, tag, ocap_duration, filename, id) VALUES (?, ?, ?, ?, ?, ?, ?)`, inserts},
		{`UPDATE missions SET mission_name = ?, world_name = ?, date = ?, tag = ?, ocap_duration = ?, filename = ?,
			status = 'pending', attempts = 0, next_attempt_at = 0, digest = NULL WHERE id = ?`, refetches},
		{`UPDATE missions SET mission_name = ?, world_name = ?, date = ?, tag = ?, ocap_duration = ?, filename = ? WHERE id = ?`, updates},
	} {
		if len(q.ops) == 0 {
			continue
		}
		stmt, err := tx.PrepareContext(ctx, q.sql)
		if err != nil {
			return 0, err
		}
		for _, op := range q.ops {
			if _, err := stmt.ExecContext(ctx, op.MissionName, op.WorldName, op.Date, op.Tag, op.OCAPDuration, op.Filename, op.ID); err != nil {
				stmt.Close()
				return 0, err
			}
		}
		stmt.Close()
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if len(refetches)+len(updates) > 0 {
		s.Bump()
	}
	return len(inserts), nil
}

func (s *Store) operationMetas(ctx context.Context) (map[int64]OperationMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, filename, mission_name, world_name, date, tag FROM missions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]OperationMeta{}
	for rows.Next() {
		var m OperationMeta
		if err := rows.Scan(&m.ID, &m.Filename, &m.MissionName, &m.WorldName, &m.Date, &m.Tag); err != nil {
			return nil, err
		}
		out[m.ID] = m
	}
	return out, rows.Err()
}

// DeleteMissing removes recordings that are no longer listed (deleted or re-tagged in OCAP,
// or excluded by the tags/since settings). keep holds the ids that are listed.
func (s *Store) DeleteMissing(ctx context.Context, keep map[int64]bool) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM missions`)
	if err != nil {
		return 0, err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(gone) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM missions WHERE id = ?`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.Bump()
	return len(gone), nil
}

// MissionRef identifies a recording to download.
type MissionRef struct {
	ID       int64
	Filename string
	Attempts int
}

// NextToFetch returns recordings that need downloading, newest first.
func (s *Store) NextToFetch(ctx context.Context, limit, maxAttempts int, digestVersion int) ([]MissionRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, filename, attempts FROM missions
		WHERE status = 'pending'
		   OR (status = 'error' AND attempts < ? AND next_attempt_at <= ?)
		   OR (status = 'done' AND digest_version <> ?)
		ORDER BY date DESC, id DESC LIMIT ?`, maxAttempts, time.Now().Unix(), digestVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MissionRef
	for rows.Next() {
		var r MissionRef
		if err := rows.Scan(&r.ID, &r.Filename, &r.Attempts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveResult stores a recording's digest and computed statistics, replacing previous ones.
func (s *Store) SaveResult(ctx context.Context, id int64, digestBlob []byte, digestVersion int, author, computeKey string, res stats.MissionResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if digestBlob != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE missions SET digest = ?, digest_version = ?, author = ?, fetched_at = ? WHERE id = ?`,
			digestBlob, digestVersion, author, time.Now().Unix(), id); err != nil {
			return err
		}
	}
	if err := writeResult(ctx, tx, id, computeKey, res); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Bump()
	return nil
}

func writeResult(ctx context.Context, tx *sql.Tx, id int64, computeKey string, res stats.MissionResult) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM mission_players WHERE mission_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM kills WHERE mission_id = ?`, id); err != nil {
		return err
	}
	pst, err := tx.PrepareContext(ctx, `INSERT INTO mission_players
		(mission_id, name, raw_name, squad_raw, squad, side, kills, deaths, team_kills, vehicle_kills, shots, hits)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer pst.Close()
	for _, p := range res.Players {
		if _, err := pst.ExecContext(ctx, id, p.Name, p.RawName, p.SquadRaw, p.Squad, p.Side,
			p.Kills, p.Deaths, p.TeamKills, p.VehicleKills, p.Shots, p.Hits); err != nil {
			return err
		}
	}
	kst, err := tx.PrepareContext(ctx, `INSERT INTO kills
		(mission_id, frame, killer, killer_squad, killer_side, killer_is_player, victim, victim_squad, victim_side,
		 victim_kind, vehicle_class, weapon, distance, team_kill)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer kst.Close()
	for _, k := range res.Kills {
		if _, err := kst.ExecContext(ctx, id, k.Frame, k.Killer, k.KillerSquad, k.KillerSide, k.KillerIsPlayer,
			k.Victim, k.VictimSquad, k.VictimSide, k.VictimKind, k.VehicleClass, k.Weapon, k.Distance, k.TeamKill); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE missions SET status = 'done', attempts = 0, last_error = '', next_attempt_at = 0,
		compute_key = ?, duration = ?, end_side = ?, end_message = ?, player_count = ? WHERE id = ?`,
		computeKey, res.Duration, res.EndSide, res.EndMessage, len(res.Players), id)
	return err
}

// MarkError records a failed download/parse and schedules a retry.
func (s *Store) MarkError(ctx context.Context, id int64, cause error, retryAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE missions SET status = 'error', attempts = attempts + 1, last_error = ?, next_attempt_at = ? WHERE id = ?`,
		truncate(cause.Error(), 500), retryAt.Unix(), id)
	return err
}

// MarkMissing records that OCAP has no data file for a recording.
func (s *Store) MarkMissing(ctx context.Context, id int64, cause error) error {
	_, err := s.db.ExecContext(ctx, `UPDATE missions SET status = 'missing', attempts = attempts + 1, last_error = ?, next_attempt_at = 0 WHERE id = ?`,
		truncate(cause.Error(), 500), id)
	return err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// StaleComputed returns ids of recordings whose statistics were computed with other rules
// (computeKey) but whose digest is still usable.
func (s *Store) StaleComputed(ctx context.Context, digestVersion int, computeKey string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM missions
		WHERE status = 'done' AND digest IS NOT NULL AND digest_version = ? AND compute_key <> ?`,
		digestVersion, computeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Digest returns the stored digest blob of a recording.
func (s *Store) Digest(ctx context.Context, id int64) ([]byte, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT digest FROM missions WHERE id = ?`, id).Scan(&b)
	return b, err
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// Counts summarises the ingestion state.
type Counts struct {
	Total   int `json:"total"`
	Done    int `json:"done"`
	Pending int `json:"pending"`
	Error   int `json:"error"`
	Missing int `json:"missing"`
}

func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM missions GROUP BY status`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return c, err
		}
		c.Total += n
		switch st {
		case StatusDone:
			c.Done = n
		case StatusPending:
			c.Pending = n
		case StatusError:
			c.Error = n
		case StatusMissing:
			c.Missing = n
		}
	}
	return c, rows.Err()
}

// --- queries ---------------------------------------------------------------

// Filter selects processed missions.
type Filter struct {
	From        string // YYYY-MM-DD, inclusive
	To          string // YYYY-MM-DD, inclusive
	Tag         string // exact (case-insensitive); empty = any
	MinDuration float64
}

func (f Filter) where(alias string) (string, []any) {
	p := alias + "."
	conds := []string{p + "status = 'done'", p + "date >= ?", p + "date <= ?", p + "duration >= ?"}
	args := []any{f.From, f.To, f.MinDuration}
	if f.Tag != "" {
		conds = append(conds, p+"tag = ? COLLATE NOCASE")
		args = append(args, f.Tag)
	}
	return strings.Join(conds, " AND "), args
}

type Mission struct {
	ID          int64   `json:"id"`
	Filename    string  `json:"filename"`
	Name        string  `json:"name"`
	World       string  `json:"world"`
	Date        string  `json:"date"`
	Tag         string  `json:"tag"`
	Author      string  `json:"author"`
	Duration    float64 `json:"duration"`
	EndSide     string  `json:"endSide"`
	EndMessage  string  `json:"endMessage"`
	PlayerCount int     `json:"playerCount"`
}

const missionCols = `m.id, m.filename, m.mission_name, m.world_name, m.date, m.tag, m.author, m.duration, m.end_side, m.end_message, m.player_count`

func scanMission(sc interface{ Scan(...any) error }) (Mission, error) {
	var m Mission
	err := sc.Scan(&m.ID, &m.Filename, &m.Name, &m.World, &m.Date, &m.Tag, &m.Author, &m.Duration, &m.EndSide, &m.EndMessage, &m.PlayerCount)
	return m, err
}

// Missions returns processed missions matching the filter, oldest first.
func (s *Store) Missions(ctx context.Context, f Filter) ([]Mission, error) {
	where, args := f.where("m")
	rows, err := s.db.QueryContext(ctx, `SELECT `+missionCols+` FROM missions m WHERE `+where+` ORDER BY m.date, m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mission{}
	for rows.Next() {
		m, err := scanMission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Mission returns one processed mission by OCAP id.
func (s *Store) Mission(ctx context.Context, id int64) (Mission, error) {
	return scanMission(s.db.QueryRowContext(ctx, `SELECT `+missionCols+` FROM missions m WHERE m.id = ? AND m.status = 'done'`, id))
}

// Row is a player's result in a mission, with the mission date.
type Row struct {
	MissionID int64
	Date      string
	stats.PlayerRow
}

// Rows returns player rows of missions matching the filter, ordered by date. When name is
// non-empty only that player's rows are returned.
func (s *Store) Rows(ctx context.Context, f Filter, name string) ([]Row, error) {
	where, args := f.where("m")
	if name != "" {
		where += " AND p.name = ?"
		args = append(args, name)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.mission_id, m.date, p.name, p.raw_name, p.squad_raw, p.squad, p.side,
		p.kills, p.deaths, p.team_kills, p.vehicle_kills, p.shots, p.hits
		FROM mission_players p JOIN missions m ON m.id = p.mission_id
		WHERE `+where+` ORDER BY m.date, m.id`, args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

func scanRows(rows *sql.Rows) ([]Row, error) {
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.MissionID, &r.Date, &r.Name, &r.RawName, &r.SquadRaw, &r.Squad, &r.Side,
			&r.Kills, &r.Deaths, &r.TeamKills, &r.VehicleKills, &r.Shots, &r.Hits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MissionRows returns the player rows of one mission.
func (s *Store) MissionRows(ctx context.Context, id int64) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.mission_id, m.date, p.name, p.raw_name, p.squad_raw, p.squad, p.side,
		p.kills, p.deaths, p.team_kills, p.vehicle_kills, p.shots, p.hits
		FROM mission_players p JOIN missions m ON m.id = p.mission_id
		WHERE p.mission_id = ? ORDER BY p.name`, id)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// KillQuery narrows Kills.
type KillQuery struct {
	MissionID int64  // 0 = any mission matching the filter
	Player    string // killer or victim
}

type Kill struct {
	MissionID int64  `json:"missionId"`
	Date      string `json:"date"`
	stats.KillRow
}

const killCols = `k.mission_id, m.date, k.frame, k.killer, k.killer_squad, k.killer_side,
		k.killer_is_player, k.victim, k.victim_squad, k.victim_side, k.victim_kind, k.vehicle_class, k.weapon, k.distance, k.team_kill`

func scanKill(sc interface{ Scan(...any) error }) (Kill, error) {
	var k Kill
	err := sc.Scan(&k.MissionID, &k.Date, &k.Frame, &k.Killer, &k.KillerSquad, &k.KillerSide, &k.KillerIsPlayer,
		&k.Victim, &k.VictimSquad, &k.VictimSide, &k.VictimKind, &k.VehicleClass, &k.Weapon, &k.Distance, &k.TeamKill)
	return k, err
}

// CountsForLongestKill reports whether a kill qualifies for the longest-kill award: a
// non-team kill of a soldier (player or AI) by a player with a personal weapon. Vehicle
// weapons are written by OCAP as "Vehicle: weapon" and do not count, otherwise air defence
// would win every time. LongestKill applies the same rule in SQL.
func CountsForLongestKill(k stats.KillRow) bool {
	return k.KillerIsPlayer && !k.TeamKill && k.VictimKind != stats.VictimVehicle &&
		k.Weapon != "" && !strings.Contains(k.Weapon, ": ")
}

// LongestKill returns the longest kill that CountsForLongestKill accepts, or nil.
func (s *Store) LongestKill(ctx context.Context, f Filter) (*Kill, error) {
	where, args := f.where("m")
	k, err := scanKill(s.db.QueryRowContext(ctx, `SELECT `+killCols+`
		FROM kills k JOIN missions m ON m.id = k.mission_id
		WHERE `+where+` AND k.killer_is_player = 1 AND k.team_kill = 0 AND k.victim_kind <> 'vehicle'
		  AND k.weapon <> '' AND k.weapon NOT LIKE '%: %'
		ORDER BY k.distance DESC, m.date, k.frame LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func (s *Store) Kills(ctx context.Context, f Filter, q KillQuery) ([]Kill, error) {
	where, args := f.where("m")
	if q.MissionID != 0 {
		where = "m.id = ? AND m.status = 'done'"
		args = []any{q.MissionID}
	}
	if q.Player != "" {
		where += " AND (k.killer = ? OR k.victim = ?)"
		args = append(args, q.Player, q.Player)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+killCols+`
		FROM kills k JOIN missions m ON m.id = k.mission_id
		WHERE `+where+` ORDER BY m.date, m.id, k.frame`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Kill
	for rows.Next() {
		k, err := scanKill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Recompute replaces a mission's statistics, keeping its digest.
func (s *Store) Recompute(ctx context.Context, id int64, computeKey string, res stats.MissionResult) error {
	return s.SaveResult(ctx, id, nil, 0, "", computeKey, res)
}

// DeleteAll removes every recording with its digest and statistics (full resync).
func (s *Store) DeleteAll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM missions`)
	s.Bump()
	return err
}

// RetryErrors makes failed and missing recordings eligible for download again.
func (s *Store) RetryErrors(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE missions SET status = 'error', attempts = 0, next_attempt_at = 0 WHERE status IN ('error', 'missing')`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// FailedMission is a recording that could not be processed.
type FailedMission struct {
	ID          int64     `json:"id"`
	Status      string    `json:"status"` // error or missing
	Filename    string    `json:"filename"`
	Date        string    `json:"date"`
	Attempts    int       `json:"attempts"`
	LastError   string    `json:"lastError"`
	NextAttempt time.Time `json:"nextAttempt"`
}

func (s *Store) FailedMissions(ctx context.Context, limit int) ([]FailedMission, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, status, filename, date, attempts, last_error, next_attempt_at
		FROM missions WHERE status IN ('error', 'missing') ORDER BY status = 'missing', date DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FailedMission{}
	for rows.Next() {
		var f FailedMission
		var next int64
		if err := rows.Scan(&f.ID, &f.Status, &f.Filename, &f.Date, &f.Attempts, &f.LastError, &next); err != nil {
			return nil, err
		}
		f.NextAttempt = time.Unix(next, 0).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// RecentRawNames returns distinct nicknames as recorded, most recent first (for previewing
// nickname formats in the admin panel).
func (s *Store) RecentRawNames(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.raw_name FROM mission_players p JOIN missions m ON m.id = p.mission_id
		GROUP BY p.raw_name ORDER BY MAX(m.date) DESC, p.raw_name LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMeta(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, key)
	return err
}
