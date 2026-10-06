package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"precious/internal/store"
)

// backupFixture writes a valid configuration whose state directory is
// stateDir (not created here) and returns the config path.
func backupFixture(t *testing.T, stateDir string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "precious.toml")
	body := fmt.Sprintf(`state_dir = %q

[server]
external_origin = "http://127.0.0.1:8080"
allow_insecure_http = true
`, stateDir)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func runBackupCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(context.Background(), env{stdout: &out, stderr: &errOut}, args)
	return code, out.String(), errOut.String()
}

// State-store "Consistent online backup" through the CLI, with the database
// held open as a running server would: the command copies the configured
// database, prints the destination, and exits zero.
func TestBackupCommandWritesCopy(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), stateDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := backupFixture(t, stateDir)
	dest := filepath.Join(t.TempDir(), "precious-backup.db")

	code, stdout, stderr := runBackupCLI("backup", "--config", cfg, dest)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if strings.TrimSpace(stdout) != dest {
		t.Fatalf("stdout = %q, want the destination %q", stdout, dest)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("backup mode = %o, want 600", perm)
	}
}

// State-store scenario "Existing destination refused": non-zero exit, the
// existing file untouched.
func TestBackupCommandRefusesExistingDestination(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), stateDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := backupFixture(t, stateDir)
	dest := filepath.Join(t.TempDir(), "precious-backup.db")
	want := []byte("yesterday's backup")
	if err := os.WriteFile(dest, want, 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runBackupCLI("backup", "--config", cfg, dest)
	if code == 0 {
		t.Fatalf("exit 0 over an existing destination (stdout %q)", stdout)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("stderr %q does not explain the refusal", stderr)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, want) {
		t.Fatalf("existing destination modified: %q", got)
	}
}

// A missing database is an error; the command creates neither the state
// directory nor an empty database to copy.
func TestBackupCommandMissingDatabase(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg := backupFixture(t, stateDir)
	dest := filepath.Join(t.TempDir(), "precious-backup.db")

	code, _, stderr := runBackupCLI("backup", "--config", cfg, dest)
	if code == 0 {
		t.Fatal("exit 0 without a database")
	}
	if !strings.Contains(stderr, "does not exist") {
		t.Fatalf("stderr %q does not name the missing database", stderr)
	}
	if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory created: %v", err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination created: %v", err)
	}
}

func TestBackupCommandUsage(t *testing.T) {
	cfg := backupFixture(t, filepath.Join(t.TempDir(), "state"))
	for _, args := range [][]string{
		{"backup", "--config", cfg},
		{"backup", "--config", cfg, "a.db", "b.db"},
	} {
		if code, _, stderr := runBackupCLI(args...); code != 2 || !strings.Contains(stderr, "destination") {
			t.Errorf("%q: exit %d, stderr %q; want exit 2 naming the destination", args, code, stderr)
		}
	}
	if code, _, stderr := runBackupCLI("backup", "/tmp/x.db"); code == 0 || !strings.Contains(stderr, "--config") {
		t.Errorf("missing --config: exit %d, stderr %q", code, stderr)
	}
}
