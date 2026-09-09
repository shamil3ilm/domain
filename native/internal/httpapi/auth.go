package httpapi

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

	"github.com/privatedns/native/internal/store"
)

// ---- Passwords ------------------------------------------------------------

const bcryptCost = 12

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func verifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// ---- JWT ------------------------------------------------------------------

type Claims struct {
	Sub   string     `json:"sub"`
	Email string     `json:"email"`
	Role  store.Role `json:"role"`
	jwt.RegisteredClaims
}

const tokenTTL = 12 * time.Hour

type jwtIssuer struct {
	secret []byte
}

func newJWT(secret string) *jwtIssuer { return &jwtIssuer{secret: []byte(secret)} }

func (j *jwtIssuer) sign(u *store.User) (string, error) {
	now := time.Now()
	c := Claims{
		Sub:   u.ID,
		Email: u.Email,
		Role:  u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "privatedns",
			Subject:   u.ID,
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return t.SignedString(j.secret)
}

func (j *jwtIssuer) parse(tok string) (*Claims, error) {
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

const apiKeyPrefix = "pd_"

type newKey struct {
	ID          string
	Token       string
	TokenPrefix string
	TokenHash   string
}

func generateAPIKey() (*newKey, error) {
	buf := make([]byte, 30)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	body := base64.RawURLEncoding.EncodeToString(buf)
	token := apiKeyPrefix + body
	sum := sha256.Sum256([]byte(token))
	return &newKey{
		ID:          uuid.NewString(),
		Token:       token,
		TokenPrefix: token[:11],
		TokenHash:   hex.EncodeToString(sum[:]),
	}, nil
}

func hashAPIKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---- Principal + lookup ---------------------------------------------------

type Principal struct {
	UserID string
	KeyID  string
	Email  string
	Role   store.Role
	Scopes []string
}

var errUnauthorized = errors.New("unauthorized")

func (a *apiServer) authenticate(ctx context.Context, header string) (*Principal, error) {
	tok := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if tok == "" {
		return nil, errUnauthorized
	}
	if strings.HasPrefix(tok, apiKeyPrefix) {
		k, u, err := a.store.LookupAPIKey(ctx, hashAPIKey(tok))
		if err != nil {
			return nil, errUnauthorized
		}
		if k.Disabled || u.Disabled {
			return nil, errUnauthorized
		}
		if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
			return nil, errUnauthorized
		}
		go a.store.TouchAPIKey(context.Background(), k.ID)
		return &Principal{UserID: u.ID, KeyID: k.ID, Email: u.Email, Role: u.Role, Scopes: k.Scopes}, nil
	}
	c, err := a.jwt.parse(tok)
	if err != nil {
		return nil, errUnauthorized
	}
	if c.ID != "" && a.store.IsRevoked(ctx, c.ID) {
		return nil, errUnauthorized
	}
	return &Principal{UserID: c.Sub, Email: c.Email, Role: c.Role}, nil
}
