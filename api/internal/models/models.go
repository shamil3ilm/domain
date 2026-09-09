// Package models contains shared data types.
package models

import "time"

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Role        Role      `json:"role"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   time.Time `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type APIKey struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	Scopes      []string   `json:"scopes"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	// Token is only populated on creation, never returned again.
	Token string `json:"token,omitempty"`
}

type Zone struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"` // Native | Master | Slave
	IsPrivate   bool      `json:"is_private"`
	Description string    `json:"description,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Serial      uint32    `json:"serial,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

type Record struct {
	ID       string `json:"id,omitempty"` // synthetic: name|type
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      int    `json:"ttl"`
	Content  string `json:"content"`
	Disabled bool   `json:"disabled,omitempty"`
}

// RecordSet groups records with the same (name, type) — PowerDNS's native
// unit of storage for authoritative records.
type RecordSet struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl"`
	Records []Record `json:"records"`
}

type AuditEvent struct {
	ID         int64                  `json:"id"`
	Timestamp  time.Time              `json:"ts"`
	ActorUser  *string                `json:"actor_user,omitempty"`
	ActorKey   *string                `json:"actor_key,omitempty"`
	ActorIP    string                 `json:"actor_ip,omitempty"`
	Action     string                 `json:"action"`
	TargetType string                 `json:"target_type,omitempty"`
	TargetID   string                 `json:"target_id,omitempty"`
	Outcome    string                 `json:"outcome"`
	Detail     map[string]interface{} `json:"detail,omitempty"`
}
