// Package handlers contains HTTP handlers for the management API.
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/privatedns/api/internal/pdns"
)

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

// mapPDNSErr translates a PDNS API error into an appropriate HTTP status.
func mapPDNSErr(w http.ResponseWriter, err error) {
	var pe *pdns.APIError
	if errors.As(err, &pe) {
		switch pe.Status {
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, pe.Message)
		case http.StatusConflict:
			writeError(w, http.StatusConflict, pe.Message)
		case http.StatusUnprocessableEntity, http.StatusBadRequest:
			writeError(w, http.StatusBadRequest, pe.Message)
		default:
			writeError(w, http.StatusBadGateway, "pdns: "+pe.Message)
		}
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func trimDot(s string) string { return strings.TrimSuffix(s, ".") }
