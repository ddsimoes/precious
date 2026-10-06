package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	"precious/migrations"
)

// latestVersion is the schema version a freshly migrated store reaches.
func latestVersion(t *testing.T) int {
	t.Helper()
	ms, err := LoadMigrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	return ms[len(ms)-1].Version
}

// openBackup opens a finished backup the way an operator would after restoring it.
func openBackup(t *testing.T, path string) *sql.DB {
	t.Helper()
	q := url.Values{}
	q.Set("mode", "ro")
	db, err := sql.Open("sqlite", fileURI(path, q))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func assertIntegrityOK(t *testing.T, db *sql.DB) {
	t.Helper()
	var res string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil || res != "ok" {
		t.Fatalf("integrity_check = %q, %v; want ok", res, err)
	}
}

// dirNames lists dir's entries, sorted.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(es))
	for _, e := range es {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// State-store scenario "Backup while serving": a backup taken while another
// connection keeps committing is a valid database at one consistent point in
// time and passes integrity_check.
func TestBackupWhileServing(t *testing.T) {
	s, _ := openTemp(t, Options{})
	ctx := context.Background()

	// Each transaction commits two rows sharing one sequence number, so a
	// consistent snapshot holds both rows of every committed batch 1..N and
	// nothing else. attempted is raised before a batch begins and committed
	// after it commits, bounding which batches the snapshot may contain.
	var attempted, committed atomic.Int64
	started := make(chan struct{})
	stop := make(chan struct{})
	writerErr := make(chan error, 1)
	go func() {
		defer close(writerErr)
		for i := int64(1); ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			attempted.Store(i)
			err := s.Write(ctx, func(tx *sql.Tx) error {
				for _, kind := range []string{"a", "b"} {
					if _, err := tx.Exec(
						`INSERT INTO audit_events (occurred_at, kind, actor) VALUES (?, ?, 'test')`, i, kind); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				writerErr <- err
				return
			}
			committed.Store(i)
			if i == 50 {
				close(started)
			}
		}
	}()

	select {
	case <-started:
	case err := <-writerErr:
		t.Fatalf("writer stopped before the backup began: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "backup.db")
	lo := committed.Load()
	backupErr := Backup(ctx, s.Path(), dest)
	hi := attempted.Load()
	close(stop)
	if err := <-writerErr; err != nil {
		t.Fatalf("writer: %v", err)
	}
	if backupErr != nil {
		t.Fatalf("Backup: %v", backupErr)
	}

	db := openBackup(t, dest)
	assertIntegrityOK(t, db)
	var rows, batches, minSeq, maxSeq int64
	if err := db.QueryRow(
		`SELECT count(*), count(DISTINCT occurred_at), min(occurred_at), max(occurred_at) FROM audit_events`).
		Scan(&rows, &batches, &minSeq, &maxSeq); err != nil {
		t.Fatal(err)
	}
	if rows != 2*batches || minSeq != 1 || maxSeq != batches {
		t.Fatalf("backup is not a committed prefix: %d rows, %d batches, sequence %d..%d", rows, batches, minSeq, maxSeq)
	}
	var unpaired int64
	if err := db.QueryRow(
		`SELECT count(*) FROM (SELECT occurred_at FROM audit_events GROUP BY occurred_at HAVING count(*) != 2)`).
		Scan(&unpaired); err != nil || unpaired != 0 {
		t.Fatalf("%d batches only partly present (err %v)", unpaired, err)
	}
	if batches < lo || batches > hi {
		t.Fatalf("backup holds %d batches; commits during the backup ran from %d to %d", batches, lo, hi)
	}
	t.Logf("backup holds %d batches; %d committed before it began, %d begun before it returned", batches, lo, hi)
	v, err := SchemaVersion(ctx, db)
	if want := latestVersion(t); err != nil || v != want {
		t.Fatalf("backup schema version = %d, %v; want %d", v, err, want)
	}
	if got := dirNames(t, filepath.Dir(dest)); len(got) != 1 || got[0] != "backup.db" {
		t.Fatalf("destination directory holds %q; want only backup.db", got)
	}
}

// The backup is readable only by the account that made it.
func TestBackupFileIsOwnerOnly(t *testing.T) {
	s, _ := openTemp(t, Options{})
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(context.Background(), s.Path(), dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("backup mode = %o, want 600", perm)
	}
}

// The documented restore (install the backup as precious.db in a state
// directory with no -wal/-shm files) yields a database the server opens with
// its data, schema version, and WAL mode.
func TestBackupRestoresAsStateDatabase(t *testing.T) {
	s, _ := openTemp(t, Options{})
	ctx := context.Background()
	mustWrite(t, s, `INSERT INTO audit_events (occurred_at, kind, actor) VALUES (7, 'restore-marker', 'test')`)
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(ctx, s.Path(), dest); err != nil {
		t.Fatal(err)
	}

	restored := t.TempDir()
	if err := os.Rename(dest, filepath.Join(restored, FileName)); err != nil {
		t.Fatal(err)
	}
	r, err := Open(ctx, restored, Options{})
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer r.Close()
	if n := countRows(t, r, `SELECT count(*) FROM audit_events WHERE kind = 'restore-marker'`); n != 1 {
		t.Fatalf("restored database has %d marker rows, want 1", n)
	}
	if v, err := SchemaVersion(ctx, r.Writer()); err != nil || v != latestVersion(t) {
		t.Fatalf("restored schema version = %d, %v; want %d", v, err, latestVersion(t))
	}
	var mode string
	if err := r.Writer().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("restored journal_mode = %q, %v; want wal", mode, err)
	}
}

// State-store scenario "Existing destination refused": the command fails and
// the existing file is byte-identical afterwards.
func TestBackupExistingDestinationRefused(t *testing.T) {
	s, _ := openTemp(t, Options{})
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.db")
	want := []byte("an older backup that must survive")
	if err := os.WriteFile(dest, want, 0o640); err != nil {
		t.Fatal(err)
	}

	err := Backup(context.Background(), s.Path(), dest)
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("Backup over existing file: err = %v, want ErrDestinationExists", err)
	}
	got, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("existing destination modified: %q", got)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o640 {
		t.Fatalf("existing destination mode changed to %o", fi.Mode().Perm())
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Fatalf("destination directory holds %q; want only the original file", names)
	}
}

// The final link refuses a destination that appeared after Backup's early
// check (a concurrent writer), rather than replacing it.
func TestBackupPublishNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp")
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(tmp, []byte("new copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("racing file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publish(tmp, dest); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("publish over existing file: err = %v, want ErrDestinationExists", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "racing file" {
		t.Fatalf("destination replaced: %q", got)
	}

	// A dangling symlink is an existing name too; link must not follow it.
	link := filepath.Join(dir, "link")
	target := filepath.Join(dir, "elsewhere")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := publish(tmp, link); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("publish over dangling symlink: err = %v, want ErrDestinationExists", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target created: %v", err)
	}
}

// A failed copy leaves neither the destination nor temporary files behind.
func TestBackupFailureLeavesNoTemporaryFiles(t *testing.T) {
	src := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(src, bytes.Repeat([]byte("not a database "), 512), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.db")
	if err := Backup(context.Background(), src, dest); err == nil {
		t.Fatal("Backup of a non-database succeeded")
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("destination directory holds %q after a failed backup; want it empty", names)
	}
}

// Backup never creates a missing database (it would back up an empty one).
func TestBackupMissingDatabaseNotCreated(t *testing.T) {
	stateDir := t.TempDir()
	src := filepath.Join(stateDir, FileName)
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(context.Background(), src, dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Backup of a missing database: err = %v, want ErrNotExist", err)
	}
	if names := dirNames(t, stateDir); len(names) != 0 {
		t.Fatalf("state directory holds %q; want it untouched", names)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination created: %v", err)
	}
}

// Paths with characters that are special in SQLite URIs back up correctly.
func TestBackupURISpecialCharactersInPaths(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), plain, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(filepath.Dir(plain), "state ?#%")
	if err := os.Rename(plain, stateDir); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(t.TempDir(), "dest ?#%")
	if err := os.Mkdir(destDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(destDir, "b?ck#up%20.db")
	if err := Backup(context.Background(), filepath.Join(stateDir, FileName), dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	db := openBackup(t, dest)
	assertIntegrityOK(t, db)
	if v, err := SchemaVersion(context.Background(), db); err != nil || v != latestVersion(t) {
		t.Fatalf("backup schema version = %d, %v; want %d (copied the wrong file?)", v, err, latestVersion(t))
	}
	if names := dirNames(t, destDir); len(names) != 1 || names[0] != "b?ck#up%20.db" {
		t.Fatalf("destination directory holds %q", names)
	}
}
