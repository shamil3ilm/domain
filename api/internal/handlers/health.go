package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/pdns"
)

type HealthHandler struct {
	Pool *db.Pool
	PDNS *pdns.Client
}

type healthReport struct {
	Status   string            `json:"status"`
	Services map[string]string `json:"services"`
	Time     time.Time         `json:"time"`
}

func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	rep := healthReport{
		Services: map[string]string{},
		Time:     time.Now().UTC(),
	}
	rep.Services["database"] = pingDB(r.Context(), h.Pool)
	rep.Services["pdns"] = pingPDNS(r.Context(), h.PDNS)

	rep.Status = "ok"
	code := http.StatusOK
	for _, v := range rep.Services {
		if v != "ok" {
			rep.Status = "degraded"
			code = http.StatusServiceUnavailable
			break
		}
	}
	writeJSON(w, code, rep)
}

func pingDB(ctx context.Context, pool *db.Pool) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		return "unreachable: " + err.Error()
	}
	return "ok"
}

func pingPDNS(ctx context.Context, p *pdns.Client) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := p.Ping(ctx); err != nil {
		return "unreachable: " + err.Error()
	}
	return "ok"
}
