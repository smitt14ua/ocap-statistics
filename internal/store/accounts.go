package store

import (
	"context"
	"database/sql"
	"time"
)

// Players signed in through Steam or an OpenID Connect provider. Their sessions are separate
// from admin sessions and grant nothing but recognising the player on the site.

// PlayerSession is a signed-in player.
type PlayerSession struct {
	SteamID string
	// Issuer and Subject identify a user of the OpenID Connect sign-in (empty for Steam).
	Issuer, Subject string
	// Name and Picture are the profile at that provider at the last sign-in.
	Name, Picture string
	// IDToken is the provider's ID token, for the logout.
	IDToken string
	// SessionID is the provider's session ("sid"), for its back-channel logout.
	SessionID string
}

// CreatePlayerSession stores a session of the Steam sign-in by the hash of its token.
func (s *Store) CreatePlayerSession(ctx context.Context, tokenHash, steamID string, expires time.Time) error {
	return s.createPlayerSession(ctx, tokenHash, PlayerSession{SteamID: steamID}, expires)
}

// SaveOIDCAccount stores (or refreshes) the profile of an OpenID Connect user and creates a
// session for them. Users are told apart by issuer and subject only; the Steam id, name and
// avatar are profile data that change at the provider.
func (s *Store) SaveOIDCAccount(ctx context.Context, tokenHash string, a PlayerSession, expires time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO oidc_accounts (issuer, subject, steam_id, name, picture, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (issuer, subject) DO UPDATE SET steam_id = excluded.steam_id, name = excluded.name,
			picture = excluded.picture, updated_at = excluded.updated_at`,
		a.Issuer, a.Subject, a.SteamID, a.Name, a.Picture, time.Now().Unix()); err != nil {
		return err
	}
	return s.createPlayerSession(ctx, tokenHash, PlayerSession{Issuer: a.Issuer, Subject: a.Subject, IDToken: a.IDToken, SessionID: a.SessionID}, expires)
}

// EndOIDCSessions deletes, with their ID tokens, every session of an OpenID Connect user (by
// issuer and subject), or without a subject the sessions of the provider's session sid. It
// returns how many sessions ended.
func (s *Store) EndOIDCSessions(ctx context.Context, issuer, subject, sid string) (int64, error) {
	var res sql.Result
	var err error
	switch {
	case issuer == "":
		return 0, nil
	case subject != "":
		res, err = s.db.ExecContext(ctx, `DELETE FROM player_sessions WHERE issuer = ? AND subject = ?`, issuer, subject)
	case sid != "":
		res, err = s.db.ExecContext(ctx, `DELETE FROM player_sessions WHERE issuer = ? AND sid = ?`, issuer, sid)
	default:
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) createPlayerSession(ctx context.Context, tokenHash string, a PlayerSession, expires time.Time) error {
	// Expired sessions are cleaned up opportunistically.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM player_sessions WHERE expires_at < ?`, time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO player_sessions (token_hash, steam_id, expires_at, issuer, subject, id_token, sid)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, tokenHash, a.SteamID, expires.Unix(), a.Issuer, a.Subject, a.IDToken, a.SessionID)
	return err
}

// PlayerSession returns a valid player session, or sql.ErrNoRows. A session of the OpenID
// Connect sign-in carries the user's current profile.
func (s *Store) PlayerSession(ctx context.Context, tokenHash string) (PlayerSession, error) {
	var a PlayerSession
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN p.issuer = '' THEN p.steam_id ELSE COALESCE(a.steam_id, '') END,
			p.issuer, p.subject, COALESCE(a.name, ''), COALESCE(a.picture, ''), p.id_token
		FROM player_sessions p LEFT JOIN oidc_accounts a ON a.issuer = p.issuer AND a.subject = p.subject
		WHERE p.token_hash = ? AND p.expires_at >= ?`,
		tokenHash, time.Now().Unix()).Scan(&a.SteamID, &a.Issuer, &a.Subject, &a.Name, &a.Picture, &a.IDToken)
	return a, err
}

func (s *Store) DeletePlayerSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM player_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// SteamAlias is a name a Steam account played under, with its latest processed mission.
type SteamAlias struct {
	SteamID  string
	Name     string
	LastDate string
	LastID   int64
}

// SteamAliases returns every name each Steam account played under in processed missions.
func (s *Store) SteamAliases(ctx context.Context) ([]SteamAlias, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.steam_id, p.name, MAX(m.date), MAX(m.id)
		FROM mission_players p JOIN missions m ON m.id = p.mission_id
		WHERE p.steam_id != '' AND m.status = 'done'
		GROUP BY p.steam_id, p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SteamAlias
	for rows.Next() {
		var a SteamAlias
		if err := rows.Scan(&a.SteamID, &a.Name, &a.LastDate, &a.LastID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SteamName is a name a Steam account played under.
type SteamName struct {
	Name     string `json:"name"`
	LastSeen string `json:"lastSeen"`
	Missions int    `json:"missions"`
}

// SteamNames returns the names a Steam account played under, the most recent first. The
// first is the account's player on the site (report.Identities merges the others into it).
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
