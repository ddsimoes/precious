package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"precious/internal/auth"
	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/store"
)

// fakeTerminal answers password prompts from a script.
type fakeTerminal struct {
	tty     bool
	answers []string
	prompts []string
}

func (f *fakeTerminal) IsTerminal() bool { return f.tty }

func (f *fakeTerminal) ReadPassword(_ context.Context, prompt string) ([]byte, error) {
	f.prompts = append(f.prompts, prompt)
	if len(f.answers) == 0 {
		return nil, errors.New("no more input")
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return []byte(a), nil
}

func writeAdminConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "precious.toml")
	body := fmt.Sprintf(`state_dir = %q

[server]
external_origin = "https://precious.example.net"
`, filepath.Join(dir, "state"))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setPassword(t *testing.T, cfgPath string, tty *fakeTerminal) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = adminCommand(context.Background(), env{stdout: &out, stderr: &errb},
		[]string{"set-password", "--config", cfgPath}, tty)
	return code, out.String(), errb.String()
}

// openAdminState opens the configured database for inspection.
func openAdminState(t *testing.T, cfgPath string) (*store.Store, config.Config) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.EnsureStateDir(cfg.StateDir); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), cfg.StateDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, cfg
}

func credential(t *testing.T, st *store.Store) string {
	t.Helper()
	var h string
	err := st.Reader().QueryRow(`SELECT password_hash FROM users WHERE id = 1`).Scan(&h)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return h
}

const (
	pw15  = "fifteen-bytes!!" // the minimum length
	pwNew = "a different administrator passphrase"
)

// Task 8.2 "Non-interactive input refused": stdin from a file or a pipe exits
// non-zero and leaves the stored credential unchanged.
func TestAdminSetPasswordRefusesNonTerminal(t *testing.T) {
	cfgPath := writeAdminConfig(t)
	if code, _, stderr := setPassword(t, cfgPath, &fakeTerminal{tty: true, answers: []string{pw15, pw15}}); code != 0 {
		t.Fatalf("initial set-password = %d: %s", code, stderr)
	}
	st, _ := openAdminState(t, cfgPath)
	before := credential(t, st)

	file := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(file, []byte(pwNew+"\n"+pwNew+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	if _, err := pw.WriteString(pwNew + "\n" + pwNew + "\n"); err != nil {
		t.Fatal(err)
	}
	pw.Close()

	for name, stdin := range map[string]*os.File{"file": f, "pipe": pr} {
		var out, errb bytes.Buffer
		code := run(context.Background(), env{stdin: stdin, stdout: &out, stderr: &errb},
			[]string{"admin", "set-password", "--config", cfgPath})
		if code == 0 {
			t.Fatalf("%s: set-password succeeded with non-terminal input", name)
		}
		if !strings.Contains(errb.String(), "not a terminal") {
			t.Errorf("%s: stderr = %q", name, errb.String())
		}
	}
	if after := credential(t, st); after != before {
		t.Fatal("the stored credential changed")
	}
}

// Task 8.2 "Password reset revokes sessions", with the fake terminal.
func TestAdminSetPasswordRevokesSessions(t *testing.T) {
	cfgPath := writeAdminConfig(t)
	tty := &fakeTerminal{tty: true, answers: []string{pw15, pw15}}
	if code, stdout, stderr := setPassword(t, cfgPath, tty); code != 0 || !strings.Contains(stdout, "0 session(s) revoked") {
		t.Fatalf("set-password = %d: %s %s", code, stdout, stderr)
	}
	if len(tty.prompts) != 2 {
		t.Fatalf("prompts = %q, want the password twice", tty.prompts)
	}
	st, cfg := openAdminState(t, cfgPath)
	if h := credential(t, st); !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") || strings.Contains(h, pw15) {
		t.Fatalf("stored credential = %q", h)
	}

	ctx := context.Background()
	svc := auth.New(st, clock.Real{}, cfg.Auth, auth.Options{})
	addr := netip.MustParseAddr("203.0.113.7")
	pre, err := svc.StartPreLogin(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	session, err := svc.Login(ctx, auth.LoginRequest{Password: []byte(pw15), ClientAddr: addr, PreviousID: pre.Info.ID})
	if err != nil {
		t.Fatalf("login with the set password: %v", err)
	}

	if code, stdout, stderr := setPassword(t, cfgPath, &fakeTerminal{tty: true, answers: []string{pwNew, pwNew}}); code != 0 ||
		!strings.Contains(stdout, "1 session(s) revoked") {
		t.Fatalf("reset = %d: %s %s", code, stdout, stderr)
	}
	if _, ok, err := svc.Lookup(ctx, session.Token); err != nil || ok {
		t.Fatalf("session after reset: ok=%v err=%v", ok, err)
	}

	var sets, revocations int
	if err := st.Reader().QueryRow(`SELECT
		(SELECT count(*) FROM audit_events WHERE kind = 'password_set' AND actor = 'cli'),
		(SELECT count(*) FROM audit_events WHERE kind = 'sessions_revoked' AND detail = '{"count":1,"reason":"password_set"}')`).
		Scan(&sets, &revocations); err != nil {
		t.Fatal(err)
	}
	if sets != 2 || revocations != 1 {
		t.Fatalf("audit: %d password_set, %d revocation rows", sets, revocations)
	}
}

func TestAdminSetPasswordRejectsBadInput(t *testing.T) {
	cfgPath := writeAdminConfig(t)
	for name, answers := range map[string][]string{
		"mismatch":  {pwNew, pwNew + "x"},
		"too short": {"fourteen-bytes"},
		"too long":  {strings.Repeat("x", 1025)},
		"no input":  {},
	} {
		if code, _, _ := setPassword(t, cfgPath, &fakeTerminal{tty: true, answers: answers}); code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, args := range [][]string{{}, {"reset"}, {"set-password", "--config", cfgPath, "extra"}} {
		var out, errb bytes.Buffer
		if code := adminCommand(context.Background(), env{stdout: &out, stderr: &errb}, args, &fakeTerminal{tty: true}); code != 2 {
			t.Errorf("args %q: exit %d, want usage error", args, code)
		}
	}
	st, _ := openAdminState(t, cfgPath)
	if h := credential(t, st); h != "" {
		t.Fatalf("a credential was stored: %q", h)
	}
}
