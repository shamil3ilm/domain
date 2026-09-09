// Package audit writes append-only audit events to the management schema.
package audit

import (
	"context"
	"encoding/json"

	"github.com/privatedns/api/internal/db"
)

type Event struct {
	ActorUser  *string
	ActorKey   *string
	ActorIP    string
	Action     string
	TargetType string
	TargetID   string
	Outcome    string // success | failure | denied
	Detail     map[string]any
}

func Write(ctx context.Context, pool *db.Pool, e Event) error {
	var detail []byte
	if e.Detail != nil {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = b
	}
	var actorIP any
	if e.ActorIP != "" {
		actorIP = e.ActorIP
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO mgmt.audit_log
		(actor_user, actor_key, actor_ip, action, target_type, target_id, outcome, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, e.ActorUser, e.ActorKey, actorIP, e.Action, e.TargetType, e.TargetID, e.Outcome, detail)
	return err
}
