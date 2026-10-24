package store

import (
	"context"
	"time"
)

// Players signed in through Steam. Their sessions are separate from admin sessions and grant
// nothing but recognising the player on the site.

// CreatePlayerSession stores a player session by the hash of its token.
func (s *Store) CreatePlayerSession(ctx context.Context, tokenHash, steamID string, expires time.Time) error {
	// Expired sessions are cleaned up opportunistically.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM player_sessions WHERE expires_at < ?`, time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO player_sessions (token_hash, steam_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash, steamID, expires.Unix())
	return err
}

// PlayerSession returns the Steam id of a valid player session, or sql.ErrNoRows.
func (s *Store) PlayerSession(ctx context.Context, tokenHash string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT steam_id FROM player_sessions WHERE token_hash = ? AND expires_at >= ?`,
		tokenHash, time.Now().Unix()).Scan(&id)
	return id, err
}

func (s *Store) DeletePlayerSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM player_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// SteamName is a name a Steam account played under.
type SteamName struct {
	Name     string `json:"name"`
	LastSeen string `json:"lastSeen"`
	Missions int    `json:"missions"`
}

// SteamNames returns the names a Steam account played under, the most recent first.
// Statistics are merged by name, so these names are the player on the site.
func (s *Store) SteamNames(ctx context.Context, steamID string) ([]SteamName, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.name, MAX(m.date), COUNT(*)
		FROM mission_players p JOIN missions m ON m.id = p.mission_id
		WHERE p.steam_id = ? AND m.status = 'done'
		GROUP BY p.name ORDER BY MAX(m.date) DESC, MAX(m.id) DESC`, steamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SteamName{}
	for rows.Next() {
		var n SteamName
		if err := rows.Scan(&n.Name, &n.LastSeen, &n.Missions); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
