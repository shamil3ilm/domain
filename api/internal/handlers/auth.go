package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/privatedns/api/internal/audit"
	"github.com/privatedns/api/internal/auth"
	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/middleware"
	"github.com/privatedns/api/internal/models"
)

type AuthHandler struct {
	Pool *db.Pool
	JWT  *auth.JWTIssuer
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string      `json:"token"`
	ExpiresAt time.Time   `json:"expires_at"`
	User      models.User `json:"user"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required")
		return
	}

	u, hash, err := lookupUserByEmail(r.Context(), h.Pool, req.Email)
	if err != nil {
		_ = audit.Write(r.Context(), h.Pool, audit.Event{
			ActorIP: middleware.ClientIPFrom(r.Context()),
			Action:  "user.login", Outcome: "failure",
			Detail: map[string]any{"email": req.Email, "reason": "not_found"},
		})
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if u.Disabled || !auth.VerifyPassword(hash, req.Password) {
		_ = audit.Write(r.Context(), h.Pool, audit.Event{
			ActorUser: &u.ID,
			ActorIP:   middleware.ClientIPFrom(r.Context()),
			Action:    "user.login", Outcome: "failure",
			Detail: map[string]any{"reason": "bad_password_or_disabled"},
		})
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	tok, err := h.JWT.Sign(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sign token")
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `UPDATE mgmt.users SET last_login_at = now() WHERE id = $1`, u.ID)

	_ = audit.Write(r.Context(), h.Pool, audit.Event{
		ActorUser: &u.ID,
		ActorIP:   middleware.ClientIPFrom(r.Context()),
		Action:    "user.login", Outcome: "success",
	})

	writeJSON(w, http.StatusOK, loginResponse{
		Token:     tok,
		ExpiresAt: time.Now().Add(auth.TokenTTL),
		User:      *u,
	})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": p.UserID,
		"email":   p.Email,
		"role":    p.Role,
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	// Best-effort JWT revocation. If the user authenticated via API key we
	// don't need to do anything — API key revocation is a separate flow.
	tok := r.Header.Get("Authorization")
	if len(tok) > 7 && tok[:7] == "Bearer " {
		if c, err := h.JWT.Parse(tok[7:]); err == nil && c.ID != "" {
			_, _ = h.Pool.Exec(r.Context(),
				`INSERT INTO mgmt.revoked_tokens (jti, expires_at) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				c.ID, c.ExpiresAt.Time)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func lookupUserByEmail(ctx context.Context, pool *db.Pool, email string) (*models.User, string, error) {
	var u models.User
	var hash string
	err := pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role, disabled, created_at, last_login_at
		FROM mgmt.users WHERE email = $1
	`, email).Scan(&u.ID, &u.Email, &hash, &u.Role, &u.Disabled, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, "", err
	}
	return &u, hash, nil
}
