// Package storetest gives tests a migrated state database without running
// every migration again for each test. The first use in a test process
// migrates one template database with store.Open; each test then gets a
// byte copy of it in its own state directory, which store.Open finds
// current. Tests of the migrations themselves open an empty directory with
// store.Open instead.
//
// Migrating costs about a second per database under the race detector, and
// the SQLite driver's allocator is process-wide, so per-test migrations
// serialize a package's parallel tests.
package storetest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"precious/internal/store"
)

var (
	once     sync.Once
	template []byte
	errTmpl  error
)

// migrated returns the bytes of a freshly migrated, closed database.
func migrated() ([]byte, error) {
	once.Do(func() {
		dir, err := os.MkdirTemp("", "precious-storetest-")
		if err != nil {
			errTmpl = err
			return
		}
		defer os.RemoveAll(dir)
		st, err := store.Open(context.Background(), dir, store.Options{})
		if err != nil {
			errTmpl = err
			return
		}
		if err := st.Close(); err != nil {
			errTmpl = err
			return
		}
		// Closing the last connection checkpoints the write-ahead log into
		// the database file and removes it; a log left behind would hold
		// pages the copy lacks.
		if fi, err := os.Stat(filepath.Join(dir, store.FileName+"-wal")); err == nil && fi.Size() > 0 {
			errTmpl = errors.New("storetest: the template database kept a non-empty write-ahead log after Close")
			return
		}
		template, errTmpl = os.ReadFile(filepath.Join(dir, store.FileName))
	})
	return template, errTmpl
}

// Dir returns a new state directory holding a copy of the migrated
// database, for tests that open, close, and reopen the store themselves.
func Dir(t testing.TB) string {
	t.Helper()
	db, err := migrated()
	if err != nil {
		t.Fatalf("storetest: migrate the template database: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, store.FileName), db, 0o600); err != nil {
		t.Fatalf("storetest: copy the template database: %v", err)
	}
	return dir
}

// Open opens a store on a new Dir and closes it when the test ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), Dir(t), store.Options{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
