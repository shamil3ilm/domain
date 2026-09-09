package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/models"
)

type AuditHandler struct{ Pool *db.Pool }

func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, ts, actor_user, actor_key, actor_ip::text, action, target_type, target_id, outcome, detail
		FROM mgmt.audit_log
		ORDER BY ts DESC
		LIMIT $1`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	out := []models.AuditEvent{}
	for rows.Next() {
		var e models.AuditEvent
		var ip *string
		var detail []byte
		var targetType, targetID *string
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.ActorUser, &e.ActorKey, &ip,
			&e.Action, &targetType, &targetID, &e.Outcome, &detail); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ip != nil {
			e.ActorIP = *ip
		}
		if targetType != nil {
			e.TargetType = *targetType
		}
		if targetID != nil {
			e.TargetID = *targetID
		}
		if len(detail) > 0 {
			var m map[string]interface{}
			_ = json.Unmarshal(detail, &m)
			e.Detail = m
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}
