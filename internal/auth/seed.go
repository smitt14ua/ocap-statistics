package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/smitt14ua/ocap-statistics/internal/config"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// EnsureAdmin creates the default admin user from the config/environment if it does not
// exist yet. An existing user's password is only overwritten when ResetPassword is set, so
// a password changed in the admin panel survives restarts.
func EnsureAdmin(ctx context.Context, st *store.Store, cfg config.Admin, log *slog.Logger) error {
	if cfg.Password == "" {
		users, err := st.Users(ctx)
		if err != nil {
			return err
		}
		if len(users) == 0 {
			log.Warn("admin panel has no users: set OCAPSTATS_ADMIN_USERNAME and OCAPSTATS_ADMIN_PASSWORD")
		}
		return nil
	}
	if err := ValidateUsername(cfg.Username); err != nil {
		return err
	}
	if err := ValidatePassword(cfg.Password); err != nil {
		return err
	}
	_, _, err := st.PasswordHash(ctx, cfg.Username)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if exists && !cfg.ResetPassword {
		return nil // the password is managed in the admin panel
	}
	hash, err := HashPassword(cfg.Password)
	if err != nil {
		return err
	}
	if !exists {
		if err := st.CreateUser(ctx, cfg.Username, hash); err != nil {
			return err
		}
		log.Info("admin user created", "user", cfg.Username)
		return nil
	}
	if err := st.SetPassword(ctx, cfg.Username, hash); err != nil {
		return err
	}
	log.Info("admin password reset from environment", "user", cfg.Username)
	return nil
}
