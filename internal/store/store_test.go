package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrateDropsIDToken opens a database whose player sessions still keep ID tokens: the
// column goes, the sessions stay.
func TestMigrateDropsIDToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).Unix()
	if _, err := db.Exec(`CREATE TABLE player_sessions (
			token_hash TEXT PRIMARY KEY, steam_id TEXT NOT NULL, expires_at INTEGER NOT NULL,
			issuer TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '',
			id_token TEXT NOT NULL DEFAULT '', sid TEXT NOT NULL DEFAULT '');
		INSERT INTO player_sessions VALUES ('h1', '', ?, 'https://idp.example.com', 'user-1', 'eyJ.token', 'sid-1')`, expires); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if has, err := hasColumn(s.db, "player_sessions", "id_token"); err != nil || has {
		t.Errorf("id_token column after migrate: %v %v", has, err)
	}
	a, err := s.PlayerSession(context.Background(), "h1")
	if err != nil || a.Issuer != "https://idp.example.com" || a.Subject != "user-1" {
		t.Errorf("session after migrate = %+v, %v", a, err)
	}
	if n, err := s.EndOIDCSessions(context.Background(), "https://idp.example.com", "", "sid-1"); err != nil || n != 1 {
		t.Errorf("back-channel logout by sid after migrate: %d %v", n, err)
	}
}
