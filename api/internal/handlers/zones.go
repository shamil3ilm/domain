package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/privatedns/api/internal/audit"
	"github.com/privatedns/api/internal/auth"
	"github.com/privatedns/api/internal/config"
	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/middleware"
	"github.com/privatedns/api/internal/models"
	"github.com/privatedns/api/internal/pdns"
)

type ZoneHandler struct {
	Pool *db.Pool
	PDNS *pdns.Client
	Cfg  *config.Config
}

// ---- List ----------------------------------------------------------------

func (h *ZoneHandler) List(w http.ResponseWriter, r *http.Request) {
	zs, err := h.PDNS.ListZones(r.Context())
	if err != nil {
		mapPDNSErr(w, err)
		return
	}
	// Enrich with metadata from mgmt.zone_meta.
	out := make([]models.Zone, 0, len(zs))
	for _, z := range zs {
		name := trimDot(z.Name)
		zone := models.Zone{
			Name:      name,
			Kind:      z.Kind,
			Serial:    z.Serial,
			IsPrivate: h.isPrivate(name),
		}
		var desc *string
		var tags []string
		_ = h.Pool.QueryRow(r.Context(), `
			SELECT description, tags FROM mgmt.zone_meta WHERE zone_name = $1
		`, name).Scan(&desc, &tags)
		if desc != nil {
			zone.Description = *desc
		}
		zone.Tags = tags
		out = append(out, zone)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Create --------------------------------------------------------------

type createZoneRequest struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"` // Native | Master (default: Native)
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Nameservers []string `json:"nameservers,omitempty"`
}

func (h *ZoneHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createZoneRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	if err := validateZoneName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	kind := req.Kind
	if kind == "" {
		kind = "Native"
	}
	nameservers := req.Nameservers
	if len(nameservers) == 0 {
		nameservers = []string{h.Cfg.PDNSDefaultNS}
	}

	z, err := h.PDNS.CreateZone(r.Context(), pdns.Zone{
		Name:        req.Name,
		Kind:        kind,
		Nameservers: nameservers,
	})
	if err != nil {
		p, _ := middleware.PrincipalFrom(r.Context())
		writeAudit(r, h.Pool, p, "zone.create", "zone", req.Name, "failure", map[string]any{"err": err.Error()})
		mapPDNSErr(w, err)
		return
	}

	// Store metadata.
	p, _ := middleware.PrincipalFrom(r.Context())
	private := h.isPrivate(req.Name)
	var createdBy any
	if p != nil {
		createdBy = p.UserID
	}
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO mgmt.zone_meta (zone_name, is_private, description, tags, created_by)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (zone_name) DO UPDATE SET description=EXCLUDED.description, tags=EXCLUDED.tags
	`, trimDot(req.Name), private, req.Description, req.Tags, createdBy)

	writeAudit(r, h.Pool, p, "zone.create", "zone", req.Name, "success", nil)
	writeJSON(w, http.StatusCreated, models.Zone{
		Name:        trimDot(req.Name),
		Kind:        z.Kind,
		Serial:      z.Serial,
		IsPrivate:   private,
		Description: req.Description,
		Tags:        req.Tags,
	})
}

// ---- Get -----------------------------------------------------------------

func (h *ZoneHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "zone")
	z, err := h.PDNS.GetZone(r.Context(), name)
	if err != nil {
		mapPDNSErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, z)
}

// ---- Delete --------------------------------------------------------------

func (h *ZoneHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "zone")
	if err := h.PDNS.DeleteZone(r.Context(), name); err != nil {
		mapPDNSErr(w, err)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `DELETE FROM mgmt.zone_meta WHERE zone_name = $1`, trimDot(name))
	p, _ := middleware.PrincipalFrom(r.Context())
	writeAudit(r, h.Pool, p, "zone.delete", "zone", name, "success", nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Helpers -------------------------------------------------------------

// isPrivate returns true if the zone is a subzone of the configured private TLD.
func (h *ZoneHandler) isPrivate(name string) bool {
	name = trimDot(name)
	tld := strings.TrimSuffix(h.Cfg.PrivateTLD, ".")
	return name == tld || strings.HasSuffix(name, "."+tld)
}

func writeAudit(r *http.Request, pool *db.Pool, p *auth.Principal, action, ttype, tid, outcome string, detail map[string]any) {
	// This adaptor exists purely to keep the audit call sites terse.
	_ = pool // avoid unused import when audit is stubbed
	writeAuditReal(r, pool, p, action, ttype, tid, outcome, detail)
}

// The real implementation lives here so tests can override cheaply.
func writeAuditReal(r *http.Request, pool *db.Pool, p *auth.Principal, action, ttype, tid, outcome string, detail map[string]any) {
	ev := audit.Event{
		ActorIP:    middleware.ClientIPFrom(r.Context()),
		Action:     action,
		TargetType: ttype,
		TargetID:   tid,
		Outcome:    outcome,
		Detail:     detail,
	}
	if p != nil {
		ev.ActorUser = &p.UserID
		if p.KeyID != "" {
			k := p.KeyID
			ev.ActorKey = &k
		}
	}
	_ = audit.Write(r.Context(), pool, ev)
}

func validateZoneName(name string) error {
	name = trimDot(name)
	if name == "" {
		return errors.New("empty name")
	}
	if len(name) > 253 {
		return fmt.Errorf("name too long")
	}
	// Each label between 1..63, valid chars.
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return fmt.Errorf("invalid label %q", l)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '-' {
				return fmt.Errorf("invalid character in label %q", l)
			}
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("label may not start or end with hyphen: %q", l)
		}
	}
	return nil
}

