// Package httpapi hosts the management HTTP API and serves the dashboard.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/privatedns/native/internal/config"
	"github.com/privatedns/native/internal/dashboard"
	"github.com/privatedns/native/internal/store"
)

type apiServer struct {
	store *store.Store
	cfg   *config.Config
	jwt   *jwtIssuer
}

// New wires the router and returns an http.Handler.
func New(st *store.Store, cfg *config.Config) http.Handler {
	a := &apiServer{
		store: st,
		cfg:   cfg,
		jwt:   newJWT(cfg.JWTSecret),
	}

	r := chi.NewRouter()
	r.Use(requestContext)
	r.Use(accessLog)
	r.Use(securityHeaders)
	limiter := newRateLimiter(120)
	r.Use(limiter.middleware)

	// Health (unauthenticated).
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", a.ready)

	// API routes.
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", a.login)

		r.Group(func(r chi.Router) {
			r.Use(a.authMiddleware)

			r.Get("/auth/me", a.me)
			r.Post("/auth/logout", a.logout)

			r.Get("/zones", a.listZones)
			r.Get("/zones/{zone}", a.getZone)
			r.Get("/zones/{zone}/records", a.listRecords)

			r.Get("/keys", a.listKeys)
			r.Post("/keys", a.createKey)
			r.Delete("/keys/{id}", a.deleteKey)

			r.Get("/audit", a.listAudit)

			r.Group(func(r chi.Router) {
				r.Use(requireRole(store.RoleAdmin, store.RoleOperator))
				r.Post("/zones", a.createZone)
				r.Delete("/zones/{zone}", a.deleteZone)
				r.Put("/zones/{zone}/records", a.upsertRecord)
				r.Put("/zones/{zone}/records:batch", a.batchUpsertRecords)
				r.Delete("/zones/{zone}/records", a.deleteRecord)
			})

			r.Group(func(r chi.Router) {
				r.Use(requireRole(store.RoleAdmin))
				r.Get("/users", a.listUsers)
				r.Post("/users", a.createUser)
				r.Delete("/users/{id}", a.deleteUser)
			})
		})
	})

	// Dashboard static files.
	sub, err := fs.Sub(dashboard.FS, "static")
	if err == nil {
		r.Handle("/*", http.FileServer(http.FS(sub)))
	}

	return r
}

func (a *apiServer) ready(w http.ResponseWriter, r *http.Request) {
	// Try a DB ping via a trivial query.
	_, err := a.store.ListZones(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "degraded", "err": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func trimDot(s string) string { return strings.TrimSuffix(s, ".") }

// qualify combines a relative or absolute name with a zone, always returning
// a fully qualified name without a trailing dot.
func qualify(name, zone string) string {
	name = trimDot(strings.TrimSpace(strings.ToLower(name)))
	zone = trimDot(strings.TrimSpace(strings.ToLower(zone)))
	if name == "" || name == "@" {
		return zone
	}
	if strings.HasSuffix(name, "."+zone) || name == zone {
		return name
	}
	return name + "." + zone
}

func validateZoneName(name string) error {
	name = trimDot(strings.ToLower(name))
	if name == "" {
		return errors.New("empty name")
	}
	if len(name) > 253 {
		return errors.New("name too long")
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return fmt.Errorf("invalid label %q", l)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
				return fmt.Errorf("invalid character in label %q", l)
			}
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("label %q may not start or end with hyphen", l)
		}
	}
	return nil
}

var supportedTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true,
	"NS": true, "SRV": true, "CAA": true, "PTR": true,
}

func validateRRType(t string) error {
	if !supportedTypes[strings.ToUpper(t)] {
		return fmt.Errorf("unsupported record type: %s", t)
	}
	return nil
}

// itoa is a tiny wrapper so callers stay tidy at the call site.
func itoa(n int) string { return strconv.Itoa(n) }

func parseIntOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
