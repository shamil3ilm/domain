// Package bootstrap seeds the initial admin user and private-root zone.
package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/privatedns/native/internal/config"
	"github.com/privatedns/native/internal/store"
)

// Run creates the bootstrap admin if there is none, and ensures the private
// TLD exists as a zone.
func Run(ctx context.Context, st *store.Store, cfg *config.Config) error {
	users, err := st.ListUsers(ctx)
	if err != nil {
		return err
	}
	haveAdmin := false
	for _, u := range users {
		if u.Role == store.RoleAdmin && !u.Disabled {
			haveAdmin = true
			break
		}
	}
	if !haveAdmin {
		hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), 12)
		if err != nil {
			return fmt.Errorf("hash admin password: %w", err)
		}
		u := &store.User{
			ID: uuid.NewString(), Email: cfg.AdminEmail,
			PasswordHash: string(hash), Role: store.RoleAdmin,
		}
		if err := st.CreateUser(ctx, u); err != nil {
			return fmt.Errorf("create admin: %w", err)
		}
		slog.Info("bootstrap.admin_created", "email", cfg.AdminEmail)
		fmt.Println("========================================")
		fmt.Println(" privatedns bootstrap admin credentials")
		fmt.Println("   email:   ", cfg.AdminEmail)
		fmt.Println("   password:", cfg.AdminPassword)
		fmt.Println(" (save this — it is not shown again)")
		fmt.Println("========================================")
	}

	tld := strings.ToLower(cfg.PrivateTLD)
	if _, err := st.GetZone(ctx, tld); errors.Is(err, sql.ErrNoRows) {
		if err := st.CreateZone(ctx, &store.Zone{
			Name: tld, Description: "private TLD (bootstrap)", IsPrivate: true,
		}); err != nil {
			return fmt.Errorf("create root zone: %w", err)
		}
		slog.Info("bootstrap.root_zone_created", "zone", tld)
	}
	return nil
}
