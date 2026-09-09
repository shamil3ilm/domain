package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/privatedns/native/internal/store"
)

type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyRequestID
	ctxKeyClientIP
)

func principalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(ctxKeyPrincipal).(*Principal)
	return p, ok
}

func clientIPFrom(ctx context.Context) string {
	if s, ok := ctx.Value(ctxKeyClientIP).(string); ok {
		return s
	}
	return ""
}

func requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		ip := realIP(r)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		ctx = context.WithValue(ctx, ctxKeyClientIP, ip)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func realIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		return strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

type respRec struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *respRec) WriteHeader(s int) { r.status = s; r.ResponseWriter.WriteHeader(s) }
func (r *respRec) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &respRec{ResponseWriter: w}
		next.ServeHTTP(rr, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rr.status,
			"bytes", rr.bytes,
			"dur_ms", time.Since(start).Milliseconds(),
			"ip", realIP(r),
		)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		}
		next.ServeHTTP(w, r)
	})
}

// ---- Rate limiter ---------------------------------------------------------

type rateLimiter struct {
	mu    sync.Mutex
	m     map[string]*rate.Limiter
	rpm   int
	burst int
}

func newRateLimiter(rpm int) *rateLimiter {
	if rpm <= 0 {
		rpm = 120
	}
	return &rateLimiter{m: map[string]*rate.Limiter{}, rpm: rpm, burst: rpm}
}

func (l *rateLimiter) get(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.m[key]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(float64(l.rpm)/60.0), l.burst)
		l.m[key] = lim
	}
	return lim
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := realIP(r)
		if p, ok := principalFrom(r.Context()); ok {
			key = "u:" + p.UserID
		}
		if !l.get(key).Allow() {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- Auth + RBAC ----------------------------------------------------------

func (a *apiServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.authenticate(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyPrincipal, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requireRole(roles ...store.Role) func(http.Handler) http.Handler {
	set := make(map[store.Role]struct{}, len(roles))
	for _, r := range roles {
		set[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := principalFrom(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if _, allowed := set[p.Role]; !allowed {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
