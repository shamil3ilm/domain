package httpapi

import (
	"net/http"
	"strings"
)

// Scope names. Keep the taxonomy small so operators don't have to memorize
// twenty variants. Wildcard "*" grants everything — reserved for keys minted
// by an admin who wants a full-power token (rare; prefer scoped keys).
const (
	ScopeAll          = "*"
	ScopeZonesRead    = "zones:read"
	ScopeZonesWrite   = "zones:write"
	ScopeRecordsRead  = "records:read"
	ScopeRecordsWrite = "records:write"
	ScopeUsersRead    = "users:read"
	ScopeUsersWrite   = "users:write"
	ScopeAuditRead    = "audit:read"
	ScopeKeysManage   = "keys:manage"
)

var knownScopes = map[string]struct{}{
	ScopeAll:          {},
	ScopeZonesRead:    {},
	ScopeZonesWrite:   {},
	ScopeRecordsRead:  {},
	ScopeRecordsWrite: {},
	ScopeUsersRead:    {},
	ScopeUsersWrite:   {},
	ScopeAuditRead:    {},
	ScopeKeysManage:   {},
}

// isKnownScope validates a scope name against the taxonomy. Enforced at
// key-creation time so operators can't silently ship keys with typos that
// grant nothing at runtime.
func isKnownScope(s string) bool {
	_, ok := knownScopes[strings.TrimSpace(s)]
	return ok
}

// requireScopes returns middleware that permits the request only if the
// principal's scopes satisfy one of the required scopes.
//
// Semantics:
//   - JWT sessions have no scopes (role-based only). They pass this
//     middleware unconditionally — RBAC has already been checked upstream.
//   - API keys with an EMPTY scope list are grandfathered in (backward
//     compat): they behave like JWTs, subject to role checks only.
//   - API keys with a NON-EMPTY scope list must contain one of the
//     required scopes (or "*").
//
// This lets operators start issuing scoped keys today without breaking any
// currently-issued key.
func requireScopes(required ...string) func(http.Handler) http.Handler {
	set := make(map[string]struct{}, len(required))
	for _, s := range required {
		set[s] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := principalFrom(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			// JWT session (no key) OR key with no declared scopes: pass.
			// Role checks upstream still apply.
			if p.KeyID == "" || len(p.Scopes) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			for _, s := range p.Scopes {
				s = strings.TrimSpace(s)
				if s == ScopeAll {
					next.ServeHTTP(w, r)
					return
				}
				if _, hit := set[s]; hit {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeError(w, http.StatusForbidden, "insufficient scope")
		})
	}
}
