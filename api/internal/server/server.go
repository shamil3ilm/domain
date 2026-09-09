// Package server wires middleware, routes, and handlers into an http.Handler.
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/privatedns/api/internal/auth"
	"github.com/privatedns/api/internal/config"
	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/handlers"
	"github.com/privatedns/api/internal/middleware"
	"github.com/privatedns/api/internal/models"
	"github.com/privatedns/api/internal/pdns"
)

func New(cfg *config.Config, pool *db.Pool, pd *pdns.Client, log *slog.Logger) http.Handler {
	r := chi.NewRouter()

	jwt := auth.NewJWT(cfg.JWTSecret)
	authn := middleware.NewAuthenticator(jwt, pool)
	limiter := middleware.NewLimiter(cfg.RateLimit)

	// ---- Global middleware ----
	r.Use(middleware.RequestContext)
	r.Use(middleware.AccessLog(log))
	r.Use(middleware.SecurityHeaders)
	r.Use(limiter.Middleware)

	// ---- Handlers ----
	authH := &handlers.AuthHandler{Pool: pool, JWT: jwt}
	zoneH := &handlers.ZoneHandler{Pool: pool, PDNS: pd, Cfg: cfg}
	recH := &handlers.RecordHandler{Pool: pool, PDNS: pd, Cfg: cfg}
	userH := &handlers.UserHandler{Pool: pool}
	keyH := &handlers.APIKeyHandler{Pool: pool}
	auditH := &handlers.AuditHandler{Pool: pool}
	healthH := &handlers.HealthHandler{Pool: pool, PDNS: pd}

	// ---- Unauthenticated ----
	r.Get("/healthz", healthH.Liveness)
	r.Get("/readyz", healthH.Readiness)
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/api/v1", func(r chi.Router) {
		// Public: login only.
		r.Post("/auth/login", authH.Login)

		// Authenticated.
		r.Group(func(r chi.Router) {
			r.Use(authn.Middleware)

			r.Get("/auth/me", authH.Me)
			r.Post("/auth/logout", authH.Logout)

			// Read-only endpoints: any authenticated role.
			r.Get("/zones", zoneH.List)
			r.Get("/zones/{zone}", zoneH.Get)
			r.Get("/zones/{zone}/records", recH.List)
			r.Get("/audit", auditH.List)

			// Own API keys — any role can manage their own.
			r.Get("/keys", keyH.List)
			r.Post("/keys", keyH.Create)
			r.Delete("/keys/{id}", keyH.Delete)

			// Operator/Admin: mutation.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequireRole(models.RoleAdmin, models.RoleOperator))
				r.Post("/zones", zoneH.Create)
				r.Delete("/zones/{zone}", zoneH.Delete)
				r.Put("/zones/{zone}/records", recH.Upsert)
				r.Delete("/zones/{zone}/records", recH.Delete)
				r.Delete("/zones/{zone}/records/{id}", recH.Delete)
			})

			// Admin only: user management.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequireRole(models.RoleAdmin))
				r.Get("/users", userH.List)
				r.Post("/users", userH.Create)
				r.Delete("/users/{id}", userH.Delete)
			})
		})
	})

	return r
}
