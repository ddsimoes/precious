package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"precious/internal/clock"
)

// Audit event kinds written by this package.
const (
	AuditPasswordSet     = "password_set"
	AuditSessionsRevoked = "sessions_revoked"
	AuditLoginSucceeded  = "login_succeeded"
	AuditLoginFailed     = "login_failed"
	AuditLoginThrottled  = "login_throttled"
	AuditLogout          = "logout"
)

// Audit actors.
const (
	// ActorAdmin is the authenticated administrator.
	ActorAdmin = "admin"
	// ActorAnonymous is an unauthenticated client (failed or throttled login).
	ActorAnonymous = "anonymous"
	// ActorCLI is the local command line.
	ActorCLI = "cli"
)

// AuditEvent is one audit_events row. Detail is serialized as a JSON object;
// it must never carry passwords, session tokens, CSRF tokens, or file content.
type AuditEvent struct {
	At         time.Time
	Kind       string
	Actor      string
	ClientAddr netip.Addr // zero when there is no network client
	Detail     map[string]any
}

// WriteAudit inserts ev inside tx.
func WriteAudit(ctx context.Context, tx *sql.Tx, ev AuditEvent) error {
	detail := []byte("{}")
	if len(ev.Detail) > 0 {
		b, err := json.Marshal(ev.Detail)
		if err != nil {
			return fmt.Errorf("auth: encode audit detail: %w", err)
		}
		detail = b
	}
	var addr sql.NullString
	if ev.ClientAddr.IsValid() {
		addr = sql.NullString{String: ev.ClientAddr.String(), Valid: true}
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO audit_events (occurred_at, kind, actor, client_addr, detail) VALUES (?, ?, ?, ?, ?)`,
		clock.Millis(ev.At), ev.Kind, ev.Actor, addr, string(detail))
	if err != nil {
		return fmt.Errorf("auth: write audit event %s: %w", ev.Kind, err)
	}
	return nil
}
