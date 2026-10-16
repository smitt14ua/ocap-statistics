package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrUserExists is returned when creating a user whose name is taken.
var ErrUserExists = errors.New("user already exists")

type User struct {
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var created int64
		if err := rows.Scan(&u.Username, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

// PasswordHash returns the user name as stored (names are case-insensitive) and the password
// hash, or sql.ErrNoRows for an unknown user.
func (s *Store) PasswordHash(ctx context.Context, username string) (name, hash string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT username, password_hash FROM users WHERE username = ?`, username).Scan(&name, &hash)
	return name, hash, err
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)
		ON CONFLICT(username) DO NOTHING`, username, passwordHash, time.Now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserExists
	}
	return nil
}

// SetPassword changes a password and signs the user out everywhere.
func (s *Store) SetPassword(ctx context.Context, username, passwordHash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE username = ?`, passwordHash, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE username = ?`, username)
	return err
}

func (s *Store) DeleteUser(ctx context.Context, username string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE username = ?`, username)
	return err
}

// CreateSession stores a session by the hash of its token.
func (s *Store) CreateSession(ctx context.Context, tokenHash, username string, expires time.Time) error {
	// Expired sessions are cleaned up opportunistically.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, username, expires_at) VALUES (?, ?, ?)`,
		tokenHash, username, expires.Unix())
	return err
}

// SessionUser returns the user of a valid session, or sql.ErrNoRows.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (string, error) {
	var u string
	err := s.db.QueryRowContext(ctx, `SELECT s.username FROM sessions s JOIN users u ON u.username = s.username
		WHERE s.token_hash = ? AND s.expires_at >= ?`, tokenHash, time.Now().Unix()).Scan(&u)
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}
