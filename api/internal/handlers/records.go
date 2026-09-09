package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/privatedns/api/internal/config"
	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/middleware"
	"github.com/privatedns/api/internal/models"
	"github.com/privatedns/api/internal/pdns"
)

type RecordHandler struct {
	Pool *db.Pool
	PDNS *pdns.Client
	Cfg  *config.Config
}

// ---- List records in a zone ----------------------------------------------

func (h *RecordHandler) List(w http.ResponseWriter, r *http.Request) {
	zone := chi.URLParam(r, "zone")
	z, err := h.PDNS.GetZone(r.Context(), zone)
	if err != nil {
		mapPDNSErr(w, err)
		return
	}

	out := make([]models.RecordSet, 0, len(z.RRsets))
	for _, rr := range z.RRsets {
		rs := models.RecordSet{
			Name: trimDot(rr.Name),
			Type: rr.Type,
			TTL:  rr.TTL,
		}
		for _, rec := range rr.Records {
			rs.Records = append(rs.Records, models.Record{
				ID:       fmt.Sprintf("%s|%s", trimDot(rr.Name), rr.Type),
				Name:     trimDot(rr.Name),
				Type:     rr.Type,
				TTL:      rr.TTL,
				Content:  rec.Content,
				Disabled: rec.Disabled,
			})
		}
		out = append(out, rs)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	writeJSON(w, http.StatusOK, out)
}

// ---- Create / Replace an RRset -------------------------------------------

type upsertRRsetRequest struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl"`
	Content []string `json:"content"` // multiple contents = multi-value RRset
	// For convenience with the dashboard, allow a single "value" string.
	Value string `json:"value,omitempty"`
}

func (h *RecordHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	zone := chi.URLParam(r, "zone")
	var req upsertRRsetRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "name and type required")
		return
	}
	if req.TTL <= 0 {
		req.TTL = 3600
	}
	if req.Value != "" && len(req.Content) == 0 {
		req.Content = []string{req.Value}
	}
	if len(req.Content) == 0 {
		writeError(w, http.StatusBadRequest, "content required")
		return
	}
	if err := validateRRType(req.Type); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Qualify the record name with the zone if the caller passed a relative label.
	fqdn := qualify(req.Name, zone)

	// Validate/normalize the content for common record types.
	for i, c := range req.Content {
		norm, err := normalizeContent(req.Type, c)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Content[i] = norm
	}

	recs := make([]pdns.Record, 0, len(req.Content))
	for _, c := range req.Content {
		recs = append(recs, pdns.Record{Content: c})
	}

	err := h.PDNS.PatchRRsets(r.Context(), zone, []pdns.RRset{{
		Name:       fqdn,
		Type:       req.Type,
		TTL:        req.TTL,
		ChangeType: "REPLACE",
		Records:    recs,
	}})
	p, _ := middleware.PrincipalFrom(r.Context())
	if err != nil {
		writeAuditReal(r, h.Pool, p, "record.upsert", "record", fqdn+"|"+req.Type, "failure",
			map[string]any{"err": err.Error()})
		mapPDNSErr(w, err)
		return
	}
	writeAuditReal(r, h.Pool, p, "record.upsert", "record", fqdn+"|"+req.Type, "success",
		map[string]any{"ttl": req.TTL, "content": req.Content})

	writeJSON(w, http.StatusOK, models.RecordSet{
		Name:    trimDot(fqdn),
		Type:    req.Type,
		TTL:     req.TTL,
		Records: toModelRecords(fqdn, req.Type, req.TTL, req.Content),
	})
}

// ---- Delete an RRset ------------------------------------------------------

func (h *RecordHandler) Delete(w http.ResponseWriter, r *http.Request) {
	zone := chi.URLParam(r, "zone")
	// Records are identified as "name|type" from list responses. Accept either
	// {name,type} query params or the composed id in the path.
	id := chi.URLParam(r, "id")
	var name, typ string
	if id != "" {
		parts := strings.SplitN(id, "|", 2)
		if len(parts) != 2 {
			writeError(w, http.StatusBadRequest, "invalid record id")
			return
		}
		name, typ = parts[0], parts[1]
	} else {
		name = r.URL.Query().Get("name")
		typ = r.URL.Query().Get("type")
	}
	if name == "" || typ == "" {
		writeError(w, http.StatusBadRequest, "name and type required")
		return
	}
	fqdn := qualify(name, zone)

	err := h.PDNS.PatchRRsets(r.Context(), zone, []pdns.RRset{{
		Name:       fqdn,
		Type:       typ,
		ChangeType: "DELETE",
	}})
	p, _ := middleware.PrincipalFrom(r.Context())
	if err != nil {
		writeAuditReal(r, h.Pool, p, "record.delete", "record", fqdn+"|"+typ, "failure",
			map[string]any{"err": err.Error()})
		mapPDNSErr(w, err)
		return
	}
	writeAuditReal(r, h.Pool, p, "record.delete", "record", fqdn+"|"+typ, "success", nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Helpers -------------------------------------------------------------

func qualify(name, zone string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	zone = strings.TrimSuffix(strings.TrimSpace(zone), ".")
	if name == "" || name == "@" {
		return zone + "."
	}
	if strings.HasSuffix(name, "."+zone) || name == zone {
		return name + "."
	}
	return name + "." + zone + "."
}

func toModelRecords(fqdn, typ string, ttl int, content []string) []models.Record {
	out := make([]models.Record, 0, len(content))
	for _, c := range content {
		out = append(out, models.Record{
			ID:      fmt.Sprintf("%s|%s", trimDot(fqdn), typ),
			Name:    trimDot(fqdn),
			Type:    typ,
			TTL:     ttl,
			Content: c,
		})
	}
	return out
}

var supportedTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true,
	"NS": true, "SOA": true, "SRV": true, "CAA": true, "PTR": true,
	"ALIAS": true, "SPF": true, "SSHFP": true, "TLSA": true,
}

func validateRRType(t string) error {
	if !supportedTypes[strings.ToUpper(t)] {
		return fmt.Errorf("unsupported record type: %s", t)
	}
	return nil
}

// normalizeContent enforces PowerDNS's wire format for a few common types.
// PDNS is strict — MX/SRV need priorities in-content, TXT needs quotes.
func normalizeContent(typ, c string) (string, error) {
	c = strings.TrimSpace(c)
	if c == "" {
		return "", fmt.Errorf("empty content")
	}
	switch strings.ToUpper(typ) {
	case "TXT":
		// PDNS requires TXT content to be wrapped in double quotes.
		if !strings.HasPrefix(c, "\"") {
			c = "\"" + strings.ReplaceAll(c, "\"", "\\\"") + "\""
		}
		return c, nil
	case "CNAME", "NS", "PTR", "ALIAS":
		if !strings.HasSuffix(c, ".") {
			c += "."
		}
		return c, nil
	}
	return c, nil
}
