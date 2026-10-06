package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ErrDestinationExists is returned by Backup when the destination path already
// exists. The existing file is never modified.
var ErrDestinationExists = errors.New("store: backup destination already exists")

// maxIntegrityMessages bounds how many integrity_check findings an error quotes.
const maxIntegrityMessages = 5

// Backup writes a transactionally consistent copy of the database at dbPath to
// dest (state-store spec "Consistent online backup", design D10). It is safe to
// run while another process holds the database open and keeps writing.
//
// The copy is made with VACUUM INTO from a separate read-only connection into an
// empty owner-only (0600) temporary file in dest's directory, verified with
// PRAGMA integrity_check, synced, and published with link+unlink so an existing
// dest is never replaced (ErrDestinationExists). Every failure path removes the
// temporary file; dbPath is never created or modified.
func Backup(ctx context.Context, dbPath, dest string) error {
	src, err := filepath.Abs(dbPath)
	if err != nil {
		return fmt.Errorf("store: backup source: %w", err)
	}
	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("store: backup source: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("store: backup source %s is not a regular file", src)
	}

	dst, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("store: backup destination: %w", err)
	}
	// Fail before copying a large database when dest is already taken. The
	// link below enforces no-replace atomically; this check only saves work.
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%w: %s", ErrDestinationExists, dst)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: backup destination: %w", err)
	}

	dir := filepath.Dir(dst)
	f, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".*.tmp")
	if err != nil {
		return fmt.Errorf("store: create backup temporary file: %w", err)
	}
	tmp := f.Name()
	defer removeTemp(tmp)
	// CreateTemp's 0600 is subject to umask; set the exact mode.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("store: create backup temporary file: %w", err)
	}
	// SQLite opens the file itself; VACUUM INTO accepts an existing empty file.
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: create backup temporary file: %w", err)
	}

	if err := vacuumInto(ctx, src, tmp); err != nil {
		return err
	}
	if err := integrityCheck(ctx, tmp); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return fmt.Errorf("store: sync backup: %w", err)
	}
	if err := publish(tmp, dst); err != nil {
		return err
	}
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("store: backup written to %s, but removing temporary name %s failed: %w", dst, tmp, err)
	}
	if err := syncFile(dir); err != nil {
		return fmt.Errorf("store: backup written to %s, but syncing its directory failed: %w", dst, err)
	}
	return nil
}

// vacuumInto copies the database at src into the empty file tmp in one read
// transaction, giving a single consistent point in time.
func vacuumInto(ctx context.Context, src, tmp string) error {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", fileURI(src, q))
	if err != nil {
		return fmt.Errorf("store: open database for backup: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// tmp is absolute, so SQLite never parses it as a "file:" URI.
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return fmt.Errorf("store: copy database %s: %w", src, err)
	}
	return db.Close()
}

// integrityCheck runs PRAGMA integrity_check on the finished copy. The copy is
// opened immutable: nothing else knows its name, and no -wal/-shm files may be
// left beside it.
func integrityCheck(ctx context.Context, path string) error {
	q := url.Values{}
	q.Set("immutable", "1")
	db, err := sql.Open("sqlite", fileURI(path, q))
	if err != nil {
		return fmt.Errorf("store: open backup copy: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("store: backup integrity check: %w", err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return fmt.Errorf("store: backup integrity check: %w", err)
		}
		if msg != "ok" && len(problems) < maxIntegrityMessages {
			problems = append(problems, msg)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: backup integrity check: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("store: backup copy failed the integrity check: %s", strings.Join(problems, "; "))
	}
	return rows.Close()
}

// publish gives tmp the name dest without ever replacing an existing dest:
// link(2) fails with EEXIST instead of overwriting.
func publish(tmp, dest string) error {
	if err := os.Link(tmp, dest); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
		}
		return fmt.Errorf("store: publish backup: %w", err)
	}
	return nil
}

// removeTemp deletes the temporary copy and any SQLite side files named after
// it. The name is unique to this backup, so nothing else can own them.
func removeTemp(tmp string) {
	for _, p := range []string{tmp, tmp + "-journal", tmp + "-wal", tmp + "-shm"} {
		_ = os.Remove(p)
	}
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// fileURI builds a SQLite URI filename for an absolute path, percent-encoding
// characters such as '?', '#', and '%' that would otherwise end the path.
func fileURI(path string, q url.Values) string {
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}
