package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/privatedns/api/internal/auth"
	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/middleware"
	"github.com/privatedns/api/internal/models"
)

type UserHandler struct{ Pool *db.Pool }

type createUserRequest struct {
	Email    string      `json:"email"`
	Password string      `json:"password"`
	Role     models.Role `json:"role"`
}

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, email, role, disabled, created_at, last_login_at
		FROM mgmt.users ORDER BY created_at`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []models.User{}
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.Disabled, &u.CreatedAt, &u.LastLoginAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required")
		return
	}
	if req.Role == "" {
		req.Role = models.RoleViewer
	}
	switch req.Role {
	case models.RoleAdmin, models.RoleOperator, models.RoleViewer:
	default:
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	if len(req.Password) < 12 {
		writeError(w, http.StatusBadRequest, "password must be at least 12 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash password")
		return
	}
	var u models.User
	err = h.Pool.QueryRow(r.Context(), `
		INSERT INTO mgmt.users (email, password_hash, role)
		VALUES ($1,$2,$3)
		RETURNING id, email, role, disabled, created_at, last_login_at
	`, req.Email, hash, req.Role).Scan(&u.ID, &u.Email, &u.Role, &u.Disabled, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Prevent deleting the last admin.
	var adminCount int
	_ = h.Pool.QueryRow(r.Context(), `SELECT count(*) FROM mgmt.users WHERE role = 'admin' AND NOT disabled`).Scan(&adminCount)
	var target string
	_ = h.Pool.QueryRow(r.Context(), `SELECT role FROM mgmt.users WHERE id = $1`, id).Scan(&target)
	if target == "admin" && adminCount <= 1 {
		writeError(w, http.StatusConflict, "cannot delete the last admin")
		return
	}
	if _, err := h.Pool.Exec(r.Context(), `DELETE FROM mgmt.users WHERE id = $1`, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- API keys ------------------------------------------------------------

type APIKeyHandler struct{ Pool *db.Pool }

type createKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

func (h *APIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	p, _ := middleware.PrincipalFrom(r.Context())
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, user_id, name, token_prefix, scopes, disabled, created_at, last_used_at, expires_at
		FROM mgmt.api_keys WHERE user_id = $1 ORDER BY created_at DESC`, p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []models.APIKey{}
	for rows.Next() {
		var k models.APIKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.TokenPrefix, &k.Scopes, &k.Disabled,
			&k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, k)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *APIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	if req.Scopes == nil {
		req.Scopes = []string{}
	}
	p, _ := middleware.PrincipalFrom(r.Context())

	nk, err := auth.GenerateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate key")
		return
	}
	var k models.APIKey
	err = h.Pool.QueryRow(r.Context(), `
		INSERT INTO mgmt.api_keys (id, user_id, name, token_hash, token_prefix, scopes)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, user_id, name, token_prefix, scopes, disabled, created_at, last_used_at, expires_at
	`, nk.ID, p.UserID, req.Name, nk.TokenHash, nk.TokenPrefix, req.Scopes).Scan(
		&k.ID, &k.UserID, &k.Name, &k.TokenPrefix, &k.Scopes, &k.Disabled,
		&k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	k.Token = nk.Token // returned once
	writeJSON(w, http.StatusCreated, k)
}

func (h *APIKeyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, _ := middleware.PrincipalFrom(r.Context())
	tag, err := h.Pool.Exec(r.Context(),
		`DELETE FROM mgmt.api_keys WHERE id = $1 AND user_id = $2`, id, p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
