// Package middleware wires request ID, logging, auth, RBAC, and rate limiting.
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/privatedns/api/internal/auth"
	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/models"
)

type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyRequestID
	ctxKeyClientIP
)

func PrincipalFrom(ctx context.Context) (*auth.Principal, bool) {
	p, ok := ctx.Value(ctxKeyPrincipal).(*auth.Principal)
	return p, ok
}

func RequestIDFrom(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return id
	}
	return ""
}

func ClientIPFrom(ctx context.Context) string {
	if ip, ok := ctx.Value(ctxKeyClientIP).(string); ok {
		return ip
	}
	return ""
}

// ---- Request ID + client IP -----------------------------------------------

func RequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)

		ip := clientIP(r)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		ctx = context.WithValue(ctx, ctxKeyClientIP, ip)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		parts := strings.Split(xf, ",")
		return strings.TrimSpace(parts[0])
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- Access log -----------------------------------------------------------

type respRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *respRecorder) WriteHeader(s int) { r.status = s; r.ResponseWriter.WriteHeader(s) }
func (r *respRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rr := &respRecorder{ResponseWriter: w}
			next.ServeHTTP(rr, r)
			log.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rr.status,
				"bytes", rr.bytes,
				"dur_ms", time.Since(start).Milliseconds(),
				"ip", clientIP(r),
				"req_id", RequestIDFrom(r.Context()),
			)
		})
	}
}

// ---- Security headers -----------------------------------------------------

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		// CSP for the dashboard; the JSON API endpoints are fine with any CSP.
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		}
		next.ServeHTTP(w, r)
	})
}

// ---- Rate limit -----------------------------------------------------------

type Limiter struct {
	mu   sync.Mutex
	m    map[string]*rate.Limiter
	rpm  int
	burst int
}

func NewLimiter(rpm int) *Limiter {
	if rpm <= 0 {
		rpm = 120
	}
	return &Limiter{m: map[string]*rate.Limiter{}, rpm: rpm, burst: rpm}
}

func (l *Limiter) get(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.m[key]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(float64(l.rpm)/60.0), l.burst)
		l.m[key] = lim
	}
	return lim
}

func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientIP(r)
		if p, ok := PrincipalFrom(r.Context()); ok {
			key = "u:" + p.UserID
		}
		if !l.get(key).Allow() {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- Auth -----------------------------------------------------------------
//
// Supports two schemes:
//   Authorization: Bearer <jwt>
//   Authorization: Bearer pd_<api-key>
//
// The prefix "pd_" flags an API key; otherwise it's parsed as a JWT.

type Authenticator struct {
	jwt  *auth.JWTIssuer
	pool *db.Pool
}

func NewAuthenticator(j *auth.JWTIssuer, p *db.Pool) *Authenticator {
	return &Authenticator{jwt: j, pool: p}
}

var ErrUnauthorized = errors.New("unauthorized")

func (a *Authenticator) authenticate(ctx context.Context, header string) (*auth.Principal, error) {
	tok := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if tok == "" {
		return nil, ErrUnauthorized
	}
	if strings.HasPrefix(tok, "pd_") {
		return auth.LookupAPIKey(ctx, a.pool, tok)
	}
	c, err := a.jwt.Parse(tok)
	if err != nil {
		return nil, ErrUnauthorized
	}
	// Check revocation.
	var revoked bool
	_ = a.pool.QueryRow(ctx, `SELECT true FROM mgmt.revoked_tokens WHERE jti = $1`, c.ID).Scan(&revoked)
	if revoked {
		return nil, ErrUnauthorized
	}
	return &auth.Principal{
		UserID: c.Sub,
		Email:  c.Email,
		Role:   c.Role,
	}, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.authenticate(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyPrincipal, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole rejects requests whose principal doesn't have one of the roles.
func RequireRole(roles ...models.Role) func(http.Handler) http.Handler {
	set := make(map[models.Role]struct{}, len(roles))
	for _, r := range roles {
		set[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if _, allowed := set[p.Role]; !allowed {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
