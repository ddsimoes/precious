// Package store owns precious's SQLite database (§8.1, design D6/D10): local-only
// placement, pragmas, a serialized writer, a small read pool, and versioned
// forward migrations.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"precious/migrations"
)

// FileName is the database file inside the state directory.
const FileName = "precious.db"

// legacyFileName is the v0.2 database file. Open reports one in the state
// directory and never opens it (design D6).
const legacyFileName = "curator.db"

// Options tunes Open. The zero value is the production configuration.
type Options struct {
	// ReadConns is the read pool size. Default 4.
	ReadConns int
	// Migrations replaces the embedded migrations (tests only).
	Migrations fs.FS
	// Logger receives the notice about a v0.2 database. Default slog.Default().
	Logger *slog.Logger
}

// Store is the opened database.
type Store struct {
	path string
	w    *sql.DB
	r    *sql.DB
}

// ErrNetworkFilesystem is returned when the state directory is on NFS/SMB/CIFS
// (detected on Linux only; see checkLocalFilesystem).
var ErrNetworkFilesystem = errors.New("store: the database requires local storage; the state directory is on a network filesystem")

// Open verifies the state directory is on local storage (where the platform
// can tell), opens the database (creating it with mode 0600 when missing),
// applies pragmas, and runs pending migrations. The state directory itself
// must already exist; creating it and checking its permissions is the
// caller's job (config.EnsureStateDir). A v0.2 database in the state
// directory is logged once and left untouched.
func Open(ctx context.Context, stateDir string, opts Options) (*Store, error) {
	if err := checkLocalFilesystem(stateDir); err != nil {
		return nil, err
	}
	logLegacyDatabase(stateDir, opts.Logger)
	path := filepath.Join(stateDir, FileName)
	// Pre-create owner-only so SQLite's -wal/-shm files inherit mode 0600.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: create database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("store: create database file: %w", err)
	}

	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("store: open writer: %w", err)
	}

	fsys := opts.Migrations
	if fsys == nil {
		fsys = migrations.FS
	}
	ms, err := LoadMigrations(fsys)
	if err != nil {
		w.Close()
		return nil, err
	}
	if err := migrate(ctx, w, ms); err != nil {
		w.Close()
		return nil, err
	}

	n := opts.ReadConns
	if n <= 0 {
		n = 4
	}
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("store: open readers: %w", err)
	}
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("store: open readers: %w", err)
	}
	return &Store{path: path, w: w, r: r}, nil
}

// logLegacyDatabase logs one line when stateDir holds a v0.2 database. The
// file is only stat'ed: R1 starts from an empty index and never reads it.
func logLegacyDatabase(stateDir string, log *slog.Logger) {
	legacy := filepath.Join(stateDir, legacyFileName)
	if _, err := os.Lstat(legacy); err != nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	log.Info("store: ignoring the v0.2 database; it is left untouched and never opened", "path", legacy)
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		// Writers take the write lock at BEGIN so concurrent processes (CLI
		// commands next to a running server) wait on busy_timeout instead of
		// failing on lock upgrade.
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Path is the database file path.
func (s *Store) Path() string { return s.path }

// Writer is the single-connection write handle. Prefer Write.
func (s *Store) Writer() *sql.DB { return s.w }

// Reader is the read-only pool. Prefer Read for multi-statement consistency.
func (s *Store) Reader() *sql.DB { return s.r }

// Write runs fn in one write transaction on the serialized writer. Keep
// transactions bounded (one batch of at most a few hundred rows).
func (s *Store) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return runTx(ctx, s.w, fn)
}

// Read runs fn in one read transaction on the read pool, giving it a
// consistent snapshot.
func (s *Store) Read(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return runTx(ctx, s.r, fn)
}

func runTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Checkpoint runs a passive WAL checkpoint; the server calls it periodically
// in addition to SQLite's auto-checkpoint.
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.w.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	return err
}

// Close closes both handles.
func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}
