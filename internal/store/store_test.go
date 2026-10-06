package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"precious/migrations"
)

func openTemp(t *testing.T, opts Options) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestOpenAppliesPragmas(t *testing.T) {
	s, _ := openTemp(t, Options{})
	ctx := context.Background()
	check := func(label, pragma string, want any) {
		t.Helper()
		var got any
		if err := s.Writer().QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if g, ok := got.([]byte); ok {
			got = string(g)
		}
		if got != want {
			t.Fatalf("%s = %v (%T), want %v", label, got, got, want)
		}
	}
	check("journal_mode", "journal_mode", "wal")
	check("foreign_keys", "foreign_keys", int64(1))
	check("synchronous", "synchronous", int64(2)) // FULL
	check("busy_timeout", "busy_timeout", int64(5000))

	var qo int64
	if err := s.Reader().QueryRowContext(ctx, "PRAGMA query_only").Scan(&qo); err != nil || qo != 1 {
		t.Fatalf("reader query_only = %d, %v; want 1", qo, err)
	}
	if _, err := s.Reader().ExecContext(ctx, `INSERT INTO audit_events (occurred_at, kind, actor) VALUES (1, 'x', 'test')`); err == nil {
		t.Fatal("reader accepted a write")
	}
}

func TestDatabaseFileIsOwnerOnly(t *testing.T) {
	_, dir := openTemp(t, Options{})
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("database mode = %o, want 600", perm)
	}
}

func TestFreshDatabaseMigratedToLatest(t *testing.T) {
	s, _ := openTemp(t, Options{})
	ms, err := LoadMigrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	v, err := SchemaVersion(context.Background(), s.Writer())
	if err != nil {
		t.Fatal(err)
	}
	if v != ms[len(ms)-1].Version {
		t.Fatalf("schema version = %d, want %d", v, ms[len(ms)-1].Version)
	}
}

func TestNewerSchemaRefusedWithoutModification(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	newer := fstest.MapFS{
		"0001_one.sql": {Data: []byte(`CREATE TABLE a (x INTEGER);`)},
		"0002_two.sql": {Data: []byte(`CREATE TABLE b (x INTEGER);`)},
	}
	s, err := Open(ctx, dir, Options{Migrations: newer})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileDigest(t, filepath.Join(dir, FileName))

	older := fstest.MapFS{"0001_one.sql": newer["0001_one.sql"]}
	_, err = Open(ctx, dir, Options{Migrations: older})
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Open with older binary: err = %v, want ErrSchemaTooNew", err)
	}
	if after := fileDigest(t, filepath.Join(dir, FileName)); after != before {
		t.Fatal("database file changed after refusal")
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	good := fstest.MapFS{"0001_one.sql": {Data: []byte(`CREATE TABLE a (x INTEGER);`)}}
	s, err := Open(ctx, dir, Options{Migrations: good})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	bad := fstest.MapFS{
		"0001_one.sql": good["0001_one.sql"],
		"0002_bad.sql": {Data: []byte(`CREATE TABLE b (x INTEGER); INSERT INTO missing_table VALUES (1);`)},
	}
	if _, err := Open(ctx, dir, Options{Migrations: bad}); err == nil {
		t.Fatal("Open succeeded with a failing migration")
	}

	s, err = Open(ctx, dir, Options{Migrations: good})
	if err != nil {
		t.Fatalf("reopen after failed migration: %v", err)
	}
	defer s.Close()
	v, err := SchemaVersion(ctx, s.Writer())
	if err != nil || v != 1 {
		t.Fatalf("schema version = %d, %v; want 1", v, err)
	}
	var n int
	if err := s.Writer().QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'b'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("table b exists after rollback (count=%d, err=%v)", n, err)
	}
}

func TestMigrationsMustBeContiguous(t *testing.T) {
	_, err := LoadMigrations(fstest.MapFS{
		"0001_one.sql":   {Data: []byte(`SELECT 1;`)},
		"0003_three.sql": {Data: []byte(`SELECT 1;`)},
	})
	if err == nil {
		t.Fatal("gap in migration versions accepted")
	}
}

// A v0.2 curator.db in the state directory is named in exactly one log line
// and left byte-identical, with no SQLite side files: it is never opened.
func TestLegacyDatabaseLoggedAndUntouched(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "curator.db")
	content := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0xA5}, 4096)...)
	if err := os.WriteFile(legacy, content, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(legacy, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	before := fileDigest(t, legacy)

	var logs bytes.Buffer
	s, err := Open(context.Background(), dir, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(logs.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], legacy) {
		t.Fatalf("log = %q, want one line naming %s", logs.String(), legacy)
	}
	if after := fileDigest(t, legacy); after != before {
		t.Fatal("curator.db changed")
	}
	fi, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mtime) {
		t.Fatalf("curator.db mtime = %v, want %v", fi.ModTime(), mtime)
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(legacy + side); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("curator.db%s exists (err %v)", side, err)
		}
	}
}

// Without a v0.2 database, Open logs nothing.
func TestNoLegacyDatabaseNoLog(t *testing.T) {
	var logs bytes.Buffer
	s, err := Open(context.Background(), t.TempDir(), Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if logs.Len() != 0 {
		t.Fatalf("log = %q, want nothing", logs.String())
	}
}

func fileDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
