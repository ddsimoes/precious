package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/store"
	"precious/internal/web/session"
)

// Password length limits in bytes (design D12).
const (
	MinPasswordBytes = 15
	MaxPasswordBytes = 1024
)

// AdminUsername is the name of the single administrator row (users.id = 1).
const AdminUsername = "admin"

// Session kinds stored in sessions.kind.
const (
	KindPreLogin      = "pre_login"
	KindAuthenticated = "authenticated"
)

const (
	tokenBytes = 32
	// touchInterval bounds last_seen_at writes: a session is refreshed at most
	// once per interval, so idle expiry is exact to within this interval.
	touchInterval = time.Minute
	// pruneInterval bounds the deletion of expired and revoked session rows.
	pruneInterval = time.Minute
)

// ErrLoginFailed is the single generic login failure: wrong password,
// throttled attempt, or no administrator configured. Callers must show the
// same message for all of them.
var ErrLoginFailed = errors.New("login failed")

// ErrPasswordLength is returned for a password outside 15–1024 bytes.
var ErrPasswordLength = fmt.Errorf("the password must be %d to %d bytes long", MinPasswordBytes, MaxPasswordBytes)

// Options tunes New. The zero value is the production configuration.
type Options struct {
	// Hasher replaces the Argon2id hasher with ParamsV1 (tests only).
	Hasher PasswordHasher
}

// Service owns the administrator credential, sessions, and login flow.
type Service struct {
	st       *store.Store
	clk      clock.Clock
	idle     time.Duration
	absolute time.Duration
	hasher   PasswordHasher
	throttle *Throttle

	pruneMu   sync.Mutex
	nextPrune time.Time
}

// New returns a Service using the session lifetimes and throttle cap of cfg.
func New(st *store.Store, clk clock.Clock, cfg config.Auth, opts Options) *Service {
	h := opts.Hasher
	if h == nil {
		h = NewHasher(ParamsV1)
	}
	return &Service{
		st:       st,
		clk:      clk,
		idle:     cfg.SessionIdle.Duration,
		absolute: cfg.SessionAbsolute.Duration,
		hasher:   h,
		throttle: NewThrottle(clk, cfg.LoginMaxBackoff.Duration),
	}
}

// Issued is a newly created session: Token goes into the cookie and is never
// stored or logged.
type Issued struct {
	Token string
	Info  session.Info
}

// ValidatePassword enforces the length limits.
func ValidatePassword(password []byte) error {
	if len(password) < MinPasswordBytes || len(password) > MaxPasswordBytes {
		return ErrPasswordLength
	}
	return nil
}

// AdminExists reports whether an administrator password has been set.
func (s *Service) AdminExists(ctx context.Context) (bool, error) {
	_, ok, err := s.adminHash(ctx, s.st.Reader())
	return ok, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Service) adminHash(ctx context.Context, q queryer) (string, bool, error) {
	var h string
	err := q.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = 1`).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("auth: read administrator: %w", err)
	}
	return h, true, nil
}

// SetPassword creates or replaces the administrator credential, revokes every
// session, and audits both, in one transaction. It returns the number of
// sessions revoked. Only the local CLI calls it.
func (s *Service) SetPassword(ctx context.Context, password []byte) (int64, error) {
	if err := ValidatePassword(password); err != nil {
		return 0, err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return 0, err
	}
	now := s.clk.Now()
	ms := clock.Millis(now)
	var revoked int64
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		_, existed, err := s.adminHash(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO users (id, username, password_hash, password_changed_at, created_at)
			VALUES (1, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET
				password_hash = excluded.password_hash,
				password_changed_at = excluded.password_changed_at`,
			AdminUsername, hash, ms, ms); err != nil {
			return fmt.Errorf("auth: store credential: %w", err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE revoked_at IS NULL`, ms)
		if err != nil {
			return fmt.Errorf("auth: revoke sessions: %w", err)
		}
		if revoked, err = res.RowsAffected(); err != nil {
			return err
		}
		if err := WriteAudit(ctx, tx, AuditEvent{
			At: now, Kind: AuditPasswordSet, Actor: ActorCLI,
			Detail: map[string]any{"created": !existed},
		}); err != nil {
			return err
		}
		return WriteAudit(ctx, tx, AuditEvent{
			At: now, Kind: AuditSessionsRevoked, Actor: ActorCLI,
			Detail: map[string]any{"reason": AuditPasswordSet, "count": revoked},
		})
	})
	if err != nil {
		return 0, err
	}
	return revoked, nil
}

// newToken returns a random 32-byte token in base64url form and the SHA-256
// of its raw bytes.
func (s *Service) newToken() (string, []byte, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: read random token: %w", err)
	}
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(b), sum[:], nil
}

// tokenHash decodes a cookie token; ok is false for anything that is not a
// base64url encoding of exactly 32 bytes.
func tokenHash(token string) ([]byte, bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != tokenBytes {
		return nil, false
	}
	sum := sha256.Sum256(b)
	return sum[:], true
}

// createSession inserts a session row inside tx.
func (s *Service) createSession(ctx context.Context, tx *sql.Tx, kind string, now time.Time, addr netip.Addr) (Issued, error) {
	token, hash, err := s.newToken()
	if err != nil {
		return Issued{}, err
	}
	csrf, _, err := s.newToken()
	if err != nil {
		return Issued{}, err
	}
	absolute := s.absolute
	var userID sql.NullInt64
	if kind == KindAuthenticated {
		userID = sql.NullInt64{Int64: 1, Valid: true}
	} else {
		// A pre-login session only needs to outlive one visit to the login page.
		absolute = min(s.idle, s.absolute)
	}
	var clientAddr sql.NullString
	if addr.IsValid() {
		clientAddr = sql.NullString{String: addr.String(), Valid: true}
	}
	ms := clock.Millis(now)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, kind, user_id, csrf_token, created_at, last_seen_at,
		                      idle_expires_at, absolute_expires_at, client_addr)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hash, kind, userID, csrf, ms, ms,
		clock.Millis(now.Add(s.idle)), clock.Millis(now.Add(absolute)), clientAddr)
	if err != nil {
		return Issued{}, fmt.Errorf("auth: create session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Issued{}, err
	}
	return Issued{Token: token, Info: session.Info{ID: id, Authenticated: kind == KindAuthenticated, CSRFToken: csrf}}, nil
}

// pruneSessions deletes expired and revoked rows, at most once per pruneInterval.
func (s *Service) pruneSessions(ctx context.Context, tx *sql.Tx, now time.Time) error {
	s.pruneMu.Lock()
	due := !now.Before(s.nextPrune)
	if due {
		s.nextPrune = now.Add(pruneInterval)
	}
	s.pruneMu.Unlock()
	if !due {
		return nil
	}
	ms := clock.Millis(now)
	_, err := tx.ExecContext(ctx, `
		DELETE FROM sessions
		WHERE revoked_at IS NOT NULL OR idle_expires_at <= ? OR absolute_expires_at <= ?`, ms, ms)
	if err != nil {
		return fmt.Errorf("auth: prune sessions: %w", err)
	}
	return nil
}

// StartPreLogin creates the pre-login session whose CSRF token authorizes the
// login request.
func (s *Service) StartPreLogin(ctx context.Context, addr netip.Addr) (Issued, error) {
	now := s.clk.Now()
	var iss Issued
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		if err := s.pruneSessions(ctx, tx, now); err != nil {
			return err
		}
		var err error
		iss, err = s.createSession(ctx, tx, KindPreLogin, now, addr)
		return err
	})
	return iss, err
}

// Lookup returns the live session identified by a cookie token. Revoked,
// idle-expired, absolutely expired, and unknown tokens report ok=false. A live
// session's last_seen_at and idle expiry are refreshed at most once a minute.
func (s *Service) Lookup(ctx context.Context, token string) (session.Info, bool, error) {
	hash, ok := tokenHash(token)
	if !ok {
		return session.Info{}, false, nil
	}
	var (
		info                      session.Info
		kind                      string
		lastSeen, idleExp, absExp int64
		revoked                   sql.NullInt64
	)
	err := s.st.Reader().QueryRowContext(ctx, `
		SELECT id, kind, csrf_token, last_seen_at, idle_expires_at, absolute_expires_at, revoked_at
		FROM sessions WHERE token_hash = ?`, hash).
		Scan(&info.ID, &kind, &info.CSRFToken, &lastSeen, &idleExp, &absExp, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Info{}, false, nil
	}
	if err != nil {
		return session.Info{}, false, fmt.Errorf("auth: read session: %w", err)
	}
	now := s.clk.Now()
	ms := clock.Millis(now)
	if revoked.Valid || ms >= idleExp || ms >= absExp {
		return session.Info{}, false, nil
	}
	if now.Sub(clock.FromMillis(lastSeen)) >= touchInterval {
		_, err := s.st.Writer().ExecContext(ctx,
			`UPDATE sessions SET last_seen_at = ?, idle_expires_at = ? WHERE id = ? AND revoked_at IS NULL`,
			ms, clock.Millis(now.Add(s.idle)), info.ID)
		if err != nil {
			return session.Info{}, false, fmt.Errorf("auth: refresh session: %w", err)
		}
	}
	info.Authenticated = kind == KindAuthenticated
	return info, true, nil
}

// LoginRequest is one submitted login.
type LoginRequest struct {
	Password []byte
	// ClientAddr is the derived client address (throttling and audit key).
	ClientAddr netip.Addr
	// PreviousID is the session the request arrived with (normally the
	// pre-login session); it is replaced by the new session. Zero when none.
	PreviousID int64
}

// Login checks the throttle, verifies the password, and on success issues a
// new authenticated session, removing the previous one. Every refusal returns
// ErrLoginFailed; other errors are internal failures. Every outcome is audited.
func (s *Service) Login(ctx context.Context, req LoginRequest) (Issued, error) {
	if !s.throttle.Admit(req.ClientAddr) {
		return Issued{}, s.loginRefused(ctx, req.ClientAddr, AuditLoginThrottled, "")
	}
	hash, exists, err := s.adminHash(ctx, s.st.Reader())
	if err != nil {
		return Issued{}, err
	}
	if !exists {
		return Issued{}, s.loginRefused(ctx, req.ClientAddr, AuditLoginFailed, "no_administrator")
	}
	if ValidatePassword(req.Password) != nil {
		// No stored password can have this length; skip the Argon2 work.
		return Issued{}, s.loginRefused(ctx, req.ClientAddr, AuditLoginFailed, "wrong_password")
	}
	match, outdated, err := s.hasher.Verify(ctx, hash, req.Password)
	if err != nil {
		return Issued{}, err
	}
	if !match {
		return Issued{}, s.loginRefused(ctx, req.ClientAddr, AuditLoginFailed, "wrong_password")
	}
	var upgraded string
	if outdated {
		if upgraded, err = s.hasher.Hash(ctx, req.Password); err != nil {
			return Issued{}, err
		}
	}

	now := s.clk.Now()
	ms := clock.Millis(now)
	var (
		iss     Issued
		changed bool
	)
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		// The credential may have been reset by the CLI while Argon2 ran;
		// never issue a session for a password that is no longer current.
		current, ok, err := s.adminHash(ctx, tx)
		if err != nil {
			return err
		}
		if !ok || current != hash {
			changed = true
			return WriteAudit(ctx, tx, AuditEvent{
				At: now, Kind: AuditLoginFailed, Actor: ActorAnonymous, ClientAddr: req.ClientAddr,
				Detail: map[string]any{"reason": "credential_changed"},
			})
		}
		if upgraded != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = 1`, upgraded); err != nil {
				return fmt.Errorf("auth: upgrade password hash: %w", err)
			}
		}
		if req.PreviousID != 0 {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM sessions WHERE id = ? AND kind = ?`, req.PreviousID, KindPreLogin); err != nil {
				return fmt.Errorf("auth: remove pre-login session: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, ms, req.PreviousID); err != nil {
				return fmt.Errorf("auth: revoke previous session: %w", err)
			}
		}
		if iss, err = s.createSession(ctx, tx, KindAuthenticated, now, req.ClientAddr); err != nil {
			return err
		}
		detail := map[string]any{"session_id": iss.Info.ID}
		if upgraded != "" {
			detail["password_hash_upgraded"] = true
		}
		return WriteAudit(ctx, tx, AuditEvent{
			At: now, Kind: AuditLoginSucceeded, Actor: ActorAdmin, ClientAddr: req.ClientAddr, Detail: detail,
		})
	})
	if err != nil {
		return Issued{}, err
	}
	if changed {
		return Issued{}, ErrLoginFailed
	}
	s.throttle.Succeeded(req.ClientAddr)
	return iss, nil
}

// loginRefused audits a refused attempt and returns ErrLoginFailed.
func (s *Service) loginRefused(ctx context.Context, addr netip.Addr, kind, reason string) error {
	ev := AuditEvent{At: s.clk.Now(), Kind: kind, Actor: ActorAnonymous, ClientAddr: addr}
	if reason != "" {
		ev.Detail = map[string]any{"reason": reason}
	}
	if err := s.st.Write(ctx, func(tx *sql.Tx) error { return WriteAudit(ctx, tx, ev) }); err != nil {
		return err
	}
	return ErrLoginFailed
}

// Logout revokes the session immediately and audits it.
func (s *Service) Logout(ctx context.Context, sessionID int64, addr netip.Addr) error {
	now := s.clk.Now()
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, clock.Millis(now), sessionID); err != nil {
			return fmt.Errorf("auth: revoke session: %w", err)
		}
		return WriteAudit(ctx, tx, AuditEvent{
			At: now, Kind: AuditLogout, Actor: ActorAdmin, ClientAddr: addr,
			Detail: map[string]any{"session_id": sessionID},
		})
	})
}
