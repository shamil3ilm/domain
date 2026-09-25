package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/privatedns/native/internal/store"
)

// ==== Auth handlers ========================================================

type loginRequest struct {
	Email, Password string
}

type loginResponse struct {
	Token     string      `json:"token"`
	ExpiresAt time.Time   `json:"expires_at"`
	User      *store.User `json:"user"`
}

func (a *apiServer) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required")
		return
	}

	ip := clientIPFrom(r.Context())

	// Lockout check runs BEFORE the bcrypt verify below. Otherwise a locked
	// -out attacker still consumes bcrypt work on every guess and can time
	// its cost.
	if allowed, retry := a.loginLim.allow(req.Email, ip); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		a.store.WriteAudit(r.Context(), store.AuditEvent{
			ActorIP: ip,
			Action:  "user.login", Outcome: "denied",
			Detail: "locked_out email=" + req.Email,
		})
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}

	u, err := a.store.GetUserByEmail(r.Context(), req.Email)
	if err != nil || u.Disabled || !verifyPassword(u.PasswordHash, req.Password) {
		a.loginLim.recordFailure(req.Email, ip)
		a.store.WriteAudit(r.Context(), store.AuditEvent{
			ActorIP: ip,
			Action:  "user.login", Outcome: "failure",
			Detail: "email=" + req.Email,
		})
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Clear on success so a typo followed by the correct password doesn't
	// leave the account cool-down half-full.
	a.loginLim.clear(req.Email, ip)

	tok, err := a.jwt.sign(u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sign token")
		return
	}
	_ = a.store.TouchLogin(r.Context(), u.ID)
	a.store.WriteAudit(r.Context(), store.AuditEvent{
		ActorUser: u.ID, ActorIP: ip,
		Action: "user.login", Outcome: "success",
	})
	u.PasswordHash = ""
	writeJSON(w, http.StatusOK, loginResponse{Token: tok, ExpiresAt: time.Now().Add(tokenTTL), User: u})
}

func (a *apiServer) me(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": p.UserID, "email": p.Email, "role": p.Role,
	})
}

func (a *apiServer) logout(w http.ResponseWriter, r *http.Request) {
	tok := r.Header.Get("Authorization")
	if strings.HasPrefix(tok, "Bearer ") {
		if c, err := a.jwt.parse(tok[7:]); err == nil && c.ID != "" {
			a.store.RevokeToken(r.Context(), c.ID, c.ExpiresAt.Time)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// ==== Zones ================================================================

type createZoneRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (a *apiServer) listZones(w http.ResponseWriter, r *http.Request) {
	zs, err := a.store.ListZones(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if zs == nil {
		zs = []store.Zone{}
	}
	writeJSON(w, http.StatusOK, zs)
}

func (a *apiServer) getZone(w http.ResponseWriter, r *http.Request) {
	z, err := a.store.GetZone(r.Context(), chi.URLParam(r, "zone"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, z)
}

func (a *apiServer) createZone(w http.ResponseWriter, r *http.Request) {
	var req createZoneRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := validateZoneName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.ToLower(trimDot(req.Name))
	tld := strings.ToLower(a.cfg.PrivateTLD)
	isPrivate := name == tld || strings.HasSuffix(name, "."+tld)

	z := &store.Zone{Name: name, Description: req.Description, IsPrivate: isPrivate}
	if err := a.store.CreateZone(r.Context(), z); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "zone exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, _ := principalFrom(r.Context())
	a.store.WriteAudit(r.Context(), store.AuditEvent{
		ActorUser: p.UserID, ActorIP: clientIPFrom(r.Context()),
		Action: "zone.create", TargetType: "zone", TargetID: name, Outcome: "success",
	})
	z, _ = a.store.GetZone(r.Context(), name)
	writeJSON(w, http.StatusCreated, z)
}

func (a *apiServer) deleteZone(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "zone")
	if err := a.store.DeleteZone(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, _ := principalFrom(r.Context())
	a.store.WriteAudit(r.Context(), store.AuditEvent{
		ActorUser: p.UserID, ActorIP: clientIPFrom(r.Context()),
		Action: "zone.delete", TargetType: "zone", TargetID: name, Outcome: "success",
	})
	w.WriteHeader(http.StatusNoContent)
}

// ==== Records ==============================================================

type upsertRecordRequest struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl,omitempty"`
	Content []string `json:"content,omitempty"`
	Value   string   `json:"value,omitempty"`
}

// RecordSet groups records with the same (name, type) for the API response.
type RecordSet struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	TTL     int            `json:"ttl"`
	Records []store.Record `json:"records"`
}

func (a *apiServer) listRecords(w http.ResponseWriter, r *http.Request) {
	zone := chi.URLParam(r, "zone")
	if _, err := a.store.GetZone(r.Context(), zone); err != nil {
		writeError(w, http.StatusNotFound, "zone not found")
		return
	}
	recs, err := a.store.ListRecordsInZone(r.Context(), zone)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Group into sets.
	sets := map[string]*RecordSet{}
	order := []string{}
	for _, rec := range recs {
		key := rec.Name + "|" + rec.Type
		if _, ok := sets[key]; !ok {
			sets[key] = &RecordSet{Name: rec.Name, Type: rec.Type, TTL: rec.TTL}
			order = append(order, key)
		}
		sets[key].Records = append(sets[key].Records, rec)
	}
	out := make([]*RecordSet, 0, len(order))
	for _, k := range order {
		out = append(out, sets[k])
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *apiServer) upsertRecord(w http.ResponseWriter, r *http.Request) {
	zone := chi.URLParam(r, "zone")
	if _, err := a.store.GetZone(r.Context(), zone); err != nil {
		writeError(w, http.StatusNotFound, "zone not found")
		return
	}

	var req upsertRecordRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "name and type required")
		return
	}
	if err := validateRRType(req.Type); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

	typ := strings.ToUpper(req.Type)
	fqdn := qualify(req.Name, zone)

	if err := a.store.UpsertRecordSet(r.Context(), zone, fqdn, typ, req.TTL, req.Content); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, _ := principalFrom(r.Context())
	a.store.WriteAudit(r.Context(), store.AuditEvent{
		ActorUser: p.UserID, ActorIP: clientIPFrom(r.Context()),
		Action: "record.upsert", TargetType: "record", TargetID: fqdn + "|" + typ,
		Outcome: "success", Detail: strings.Join(req.Content, ","),
	})
	writeJSON(w, http.StatusOK, RecordSet{
		Name: fqdn, Type: typ, TTL: req.TTL,
	})
}

// ---- Batch upsert -------------------------------------------------------

type batchUpsertRequest struct {
	Records []upsertRecordRequest `json:"records"`
}

// batchUpsertRecords replaces every (name,type) pair in one transaction.
// All-or-nothing: any invalid record aborts the entire batch. Uses the
// same auth/RBAC as the single-record endpoint (mounted in the same
// operator/admin group in server.go).
func (a *apiServer) batchUpsertRecords(w http.ResponseWriter, r *http.Request) {
	zone := chi.URLParam(r, "zone")
	if _, err := a.store.GetZone(r.Context(), zone); err != nil {
		writeError(w, http.StatusNotFound, "zone not found")
		return
	}

	var req batchUpsertRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(req.Records) == 0 {
		writeError(w, http.StatusBadRequest, "records required")
		return
	}
	// Cap the batch size to keep transaction time bounded.
	const maxBatch = 500
	if len(req.Records) > maxBatch {
		writeError(w, http.StatusBadRequest, "batch too large (max 500)")
		return
	}

	// Validate every record first. If any fails, nothing runs.
	sets := make([]store.RecordSetInput, 0, len(req.Records))
	seen := make(map[string]int, len(req.Records))
	for i, rec := range req.Records {
		if rec.Name == "" || rec.Type == "" {
			writeError(w, http.StatusBadRequest, "records[%d]: name and type required")
			return
		}
		if err := validateRRType(rec.Type); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if rec.Value != "" && len(rec.Content) == 0 {
			rec.Content = []string{rec.Value}
		}
		if len(rec.Content) == 0 {
			writeError(w, http.StatusBadRequest, "records: content required")
			return
		}
		if rec.TTL <= 0 {
			rec.TTL = 3600
		}
		typ := strings.ToUpper(rec.Type)
		fqdn := qualify(rec.Name, zone)
		key := fqdn + "|" + typ
		if prev, dup := seen[key]; dup {
			writeError(w, http.StatusBadRequest,
				"duplicate (name,type) in batch — combine content instead: index "+
					strings.TrimSpace(itoa(prev))+" and "+strings.TrimSpace(itoa(i)))
			return
		}
		seen[key] = i
		sets = append(sets, store.RecordSetInput{
			Name:     fqdn,
			Type:     typ,
			TTL:      rec.TTL,
			Contents: rec.Content,
		})
	}

	if err := a.store.UpsertRecordSetsBatch(r.Context(), zone, sets); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	p, _ := principalFrom(r.Context())
	a.store.WriteAudit(r.Context(), store.AuditEvent{
		ActorUser: p.UserID, ActorIP: clientIPFrom(r.Context()),
		Action:     "record.batch_upsert",
		TargetType: "zone", TargetID: zone,
		Outcome: "success",
		Detail:  itoa(len(sets)) + " record sets",
	})

	// Response mirrors the single-upsert style: return the record sets that
	// were persisted, without materializing every content row.
	out := make([]RecordSet, 0, len(sets))
	for _, rs := range sets {
		recs := make([]store.Record, 0, len(rs.Contents))
		for _, c := range rs.Contents {
			recs = append(recs, store.Record{
				Zone: zone, Name: rs.Name, Type: rs.Type, TTL: rs.TTL, Content: c,
			})
		}
		out = append(out, RecordSet{Name: rs.Name, Type: rs.Type, TTL: rs.TTL, Records: recs})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *apiServer) deleteRecord(w http.ResponseWriter, r *http.Request) {
	zone := chi.URLParam(r, "zone")
	name := r.URL.Query().Get("name")
	typ := strings.ToUpper(r.URL.Query().Get("type"))
	if name == "" || typ == "" {
		writeError(w, http.StatusBadRequest, "name and type required")
		return
	}
	fqdn := qualify(name, zone)
	ok, err := a.store.DeleteRecordSet(r.Context(), zone, fqdn, typ)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	p, _ := principalFrom(r.Context())
	a.store.WriteAudit(r.Context(), store.AuditEvent{
		ActorUser: p.UserID, ActorIP: clientIPFrom(r.Context()),
		Action: "record.delete", TargetType: "record", TargetID: fqdn + "|" + typ,
		Outcome: "success",
	})
	w.WriteHeader(http.StatusNoContent)
}

// ==== Users ================================================================

type createUserRequest struct {
	Email    string     `json:"email"`
	Password string     `json:"password"`
	Role     store.Role `json:"role"`
}

func (a *apiServer) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := a.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range us {
		us[i].PasswordHash = ""
	}
	if us == nil {
		us = []store.User{}
	}
	writeJSON(w, http.StatusOK, us)
}

func (a *apiServer) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || len(req.Password) < 12 {
		writeError(w, http.StatusBadRequest, "email required and password >= 12 chars")
		return
	}
	if req.Role == "" {
		req.Role = store.RoleViewer
	}
	switch req.Role {
	case store.RoleAdmin, store.RoleOperator, store.RoleViewer:
	default:
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash password")
		return
	}
	u := &store.User{
		ID:           uuid.NewString(),
		Email:        req.Email,
		PasswordHash: hash,
		Role:         req.Role,
	}
	if err := a.store.CreateUser(r.Context(), u); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	created, _ := a.store.GetUserByID(r.Context(), u.ID)
	if created != nil {
		created.PasswordHash = ""
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *apiServer) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := a.store.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if target.Role == store.RoleAdmin {
		n, _ := a.store.CountAdmins(r.Context())
		if n <= 1 {
			writeError(w, http.StatusConflict, "cannot delete the last admin")
			return
		}
	}
	if err := a.store.DeleteUser(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ==== API keys =============================================================

type createKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes,omitempty"`
}

func (a *apiServer) listKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	ks, err := a.store.ListAPIKeys(r.Context(), p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ks == nil {
		ks = []store.APIKey{}
	}
	writeJSON(w, http.StatusOK, ks)
}

func (a *apiServer) createKey(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	p, _ := principalFrom(r.Context())
	nk, err := generateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate key")
		return
	}
	k := &store.APIKey{
		ID: nk.ID, UserID: p.UserID, Name: req.Name,
		TokenHash: nk.TokenHash, TokenPrefix: nk.TokenPrefix, Scopes: req.Scopes,
	}
	if err := a.store.CreateAPIKey(r.Context(), k); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	k.Token = nk.Token
	writeJSON(w, http.StatusCreated, k)
}

func (a *apiServer) deleteKey(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	ok, err := a.store.DeleteAPIKey(r.Context(), p.UserID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ==== Audit ================================================================

func (a *apiServer) listAudit(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOr(r.URL.Query().Get("limit"), 100)
	es, err := a.store.ListAudit(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if es == nil {
		es = []store.AuditEvent{}
	}
	writeJSON(w, http.StatusOK, es)
}
