package auth

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/store"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var clientA = netip.MustParseAddr("203.0.113.7")

// cheapParams keep tests fast where the work factor is not under test.
var cheapParams = Params{Memory: 64, Time: 1, Threads: 1}

func newTestService(t *testing.T, p Params) (*Service, *store.Store, *fakeClock) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	return New(st, clk, config.Defaults().Auth, Options{Hasher: NewHasher(p)}), st, clk
}

func storedHash(t *testing.T, st *store.Store) string {
	t.Helper()
	var h string
	if err := st.Reader().QueryRow(`SELECT password_hash FROM users WHERE id = 1`).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

// login runs a pre-login visit followed by a successful login.
func login(t *testing.T, svc *Service) (pre, authed Issued) {
	t.Helper()
	ctx := context.Background()
	pre, err := svc.StartPreLogin(ctx, clientA)
	if err != nil {
		t.Fatal(err)
	}
	authed, err = svc.Login(ctx, LoginRequest{Password: []byte(testPassword), ClientAddr: clientA, PreviousID: pre.Info.ID})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return pre, authed
}

func lookupOK(t *testing.T, svc *Service, token string) bool {
	t.Helper()
	_, ok, err := svc.Lookup(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// Task 8.1: a successful login with an outdated hash stores one under the
// current parameters ("Parameter upgrade on login").
func TestParameterUpgradeOnLogin(t *testing.T) {
	old, st, _ := newTestService(t, Params{Memory: 8192, Time: 1, Threads: 1})
	if _, err := old.SetPassword(context.Background(), []byte(testPassword)); err != nil {
		t.Fatal(err)
	}
	before := storedHash(t, st)
	if !strings.HasPrefix(before, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("setup hash = %q", before)
	}
	current := New(st, old.clk, config.Defaults().Auth, Options{})
	login(t, current)
	after := storedHash(t, st)
	if !phcV1.MatchString(after) {
		t.Fatalf("hash after login = %q, want parameter set v1", after)
	}
	if match, outdated, err := NewHasher(ParamsV1).Verify(context.Background(), after, []byte(testPassword)); !match || outdated || err != nil {
		t.Fatalf("upgraded hash verify = %v, %v, %v", match, outdated, err)
	}
	// An up-to-date hash is left alone on the next login.
	login(t, current)
	if again := storedHash(t, st); again != after {
		t.Fatal("a current hash was rewritten on login")
	}
}

// Task 8.3: "Session rotation on login".
func TestSessionRotationOnLogin(t *testing.T) {
	svc, st, _ := newTestService(t, cheapParams)
	if _, err := svc.SetPassword(context.Background(), []byte(testPassword)); err != nil {
		t.Fatal(err)
	}
	pre, authed := login(t, svc)
	if pre.Info.Authenticated || !authed.Info.Authenticated {
		t.Fatalf("kinds: pre %+v, authed %+v", pre.Info, authed.Info)
	}
	if authed.Token == pre.Token || authed.Info.CSRFToken == pre.Info.CSRFToken {
		t.Fatal("login reused the pre-login token or CSRF token")
	}
	if lookupOK(t, svc, pre.Token) {
		t.Fatal("the pre-login token still resolves after login")
	}
	info, ok, err := svc.Lookup(context.Background(), authed.Token)
	if err != nil || !ok || !info.Authenticated || info.ID != authed.Info.ID {
		t.Fatalf("Lookup(new) = %+v, %v, %v", info, ok, err)
	}
	// Only the SHA-256 of the token is stored.
	var n int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = ? OR csrf_token = ?`,
		[]byte(authed.Token), authed.Token).Scan(&n); err != nil || n != 0 {
		t.Fatalf("raw token stored: %d rows, %v", n, err)
	}
}

// Task 8.3: "Idle expiry" under a fake clock, plus the bounded last_seen_at
// refresh and the absolute lifetime.
func TestSessionIdleExpiry(t *testing.T) {
	svc, st, clk := newTestService(t, cheapParams)
	if _, err := svc.SetPassword(context.Background(), []byte(testPassword)); err != nil {
		t.Fatal(err)
	}
	_, authed := login(t, svc)
	lastSeen := func() int64 {
		var v int64
		if err := st.Reader().QueryRow(`SELECT last_seen_at FROM sessions WHERE id = ?`, authed.Info.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	created := lastSeen()

	clk.Advance(30 * time.Second)
	if !lookupOK(t, svc, authed.Token) {
		t.Fatal("session expired after 30s")
	}
	if lastSeen() != created {
		t.Fatal("last_seen_at written twice within a minute")
	}
	clk.Advance(30 * time.Second)
	if !lookupOK(t, svc, authed.Token) {
		t.Fatal("session expired after 1m")
	}
	if got := lastSeen(); got != clock.Millis(clk.Now()) {
		t.Fatalf("last_seen_at = %d, want refreshed to %d", got, clock.Millis(clk.Now()))
	}

	// Unused for longer than the idle lifetime (1h default).
	clk.Advance(time.Hour + time.Millisecond)
	if lookupOK(t, svc, authed.Token) {
		t.Fatal("session still valid after the idle lifetime")
	}

	// An active session still ends at the absolute lifetime (12h default).
	_, active := login(t, svc)
	start := clk.Now()
	for clk.Now().Sub(start) < 12*time.Hour-30*time.Minute {
		clk.Advance(30 * time.Minute)
		if !lookupOK(t, svc, active.Token) {
			t.Fatalf("active session expired after %v", clk.Now().Sub(start))
		}
	}
	clk.Advance(30 * time.Minute)
	if lookupOK(t, svc, active.Token) {
		t.Fatal("session valid past the absolute lifetime")
	}
}

// Task 8.3: "Logout revokes".
func TestLogoutRevokes(t *testing.T) {
	svc, st, _ := newTestService(t, cheapParams)
	if _, err := svc.SetPassword(context.Background(), []byte(testPassword)); err != nil {
		t.Fatal(err)
	}
	_, authed := login(t, svc)
	if err := svc.Logout(context.Background(), authed.Info.ID, clientA); err != nil {
		t.Fatal(err)
	}
	if lookupOK(t, svc, authed.Token) {
		t.Fatal("replayed token resolves after logout")
	}
	var addr string
	if err := st.Reader().QueryRow(`SELECT client_addr FROM audit_events WHERE kind = 'logout'`).Scan(&addr); err != nil || addr != clientA.String() {
		t.Fatalf("logout audit client_addr = %q, %v", addr, err)
	}
}

// "Fresh install has no usable login".
func TestLoginWithoutAdministratorFails(t *testing.T) {
	svc, st, _ := newTestService(t, cheapParams)
	pre, err := svc.StartPreLogin(context.Background(), clientA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Login(context.Background(), LoginRequest{Password: []byte(testPassword), ClientAddr: clientA, PreviousID: pre.Info.ID})
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("Login = %v, want ErrLoginFailed", err)
	}
	var detail string
	if err := st.Reader().QueryRow(`SELECT detail FROM audit_events WHERE kind = 'login_failed'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if detail != `{"reason":"no_administrator"}` {
		t.Fatalf("audit detail = %s", detail)
	}
}

func TestLookupRejectsMalformedTokens(t *testing.T) {
	svc, _, _ := newTestService(t, cheapParams)
	for _, tok := range []string{"", "x", strings.Repeat("A", 43), strings.Repeat("!", 43), strings.Repeat("A", 44)} {
		if lookupOK(t, svc, tok) {
			t.Fatalf("token %q resolved", tok)
		}
	}
}
