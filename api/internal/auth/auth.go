// Package auth handles password hashing, JWT issuance, and API-key auth.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/models"
)

// ---- Passwords ------------------------------------------------------------

const bcryptCost = 12

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func VerifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// ---- JWT ------------------------------------------------------------------

type Claims struct {
	Sub   string      `json:"sub"`   // user id
	Email string      `json:"email"`
	Role  models.Role `json:"role"`
	jwt.RegisteredClaims
}

// TokenTTL is how long an issued JWT stays valid.
const TokenTTL = 12 * time.Hour

type JWTIssuer struct {
	secret []byte
}

func NewJWT(secret string) *JWTIssuer { return &JWTIssuer{secret: []byte(secret)} }

func (j *JWTIssuer) Sign(u *models.User) (string, error) {
	now := time.Now()
	c := Claims{
		Sub:   u.ID,
		Email: u.Email,
		Role:  u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "privatedns",
			Subject:   u.ID,
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return t.SignedString(j.secret)
}

func (j *JWTIssuer) Parse(tok string) (*Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(tok, &c, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Method)
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ---- API keys -------------------------------------------------------------
//
// API-key format: "pd_" + 40 char base64url. We store SHA-256(token) and the
// first 8 chars as a prefix so the UI can display it.
//
// Rationale for SHA-256 over bcrypt for keys: keys are 256 bits of random
// entropy — they're already unbrute-forceable — and API auth is on the hot
// path so we don't want a bcrypt verify per request.

const apiKeyPrefix = "pd_"

type NewKey struct {
	ID          string
	Token       string
	TokenPrefix string
	TokenHash   string
}

func GenerateAPIKey() (*NewKey, error) {
	buf := make([]byte, 30)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	body := base64.RawURLEncoding.EncodeToString(buf) // 40 chars
	token := apiKeyPrefix + body
	sum := sha256.Sum256([]byte(token))
	return &NewKey{
		ID:          uuid.NewString(),
		Token:       token,
		TokenPrefix: token[:11], // "pd_" + 8 chars
		TokenHash:   hex.EncodeToString(sum[:]),
	}, nil
}

func HashAPIKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// LookupAPIKey validates a presented token against the database.
type Principal struct {
	UserID string
	KeyID  string
	Email  string
	Role   models.Role
	Scopes []string
}

var ErrInvalidCredential = errors.New("invalid credentials")

func LookupAPIKey(ctx context.Context, pool *db.Pool, token string) (*Principal, error) {
	if !strings.HasPrefix(token, apiKeyPrefix) {
		return nil, ErrInvalidCredential
	}
	hash := HashAPIKey(token)

	var (
		keyID    string
		userID   string
		email    string
		role     string
		scopes   []string
		disabled bool
		userDis  bool
		expires  *time.Time
	)
	err := pool.QueryRow(ctx, `
		SELECT k.id, u.id, u.email, u.role, k.scopes, k.disabled, u.disabled, k.expires_at
		FROM mgmt.api_keys k JOIN mgmt.users u ON u.id = k.user_id
		WHERE k.token_hash = $1
	`, hash).Scan(&keyID, &userID, &email, &role, &scopes, &disabled, &userDis, &expires)
	if err != nil {
		return nil, ErrInvalidCredential
	}
	if disabled || userDis {
		return nil, ErrInvalidCredential
	}
	if expires != nil && time.Now().After(*expires) {
		return nil, ErrInvalidCredential
	}

	// Touch last_used_at (best-effort).
	_, _ = pool.Exec(ctx, `UPDATE mgmt.api_keys SET last_used_at = now() WHERE id = $1`, keyID)

	return &Principal{
		UserID: userID,
		KeyID:  keyID,
		Email:  email,
		Role:   models.Role(role),
		Scopes: scopes,
	}, nil
}
