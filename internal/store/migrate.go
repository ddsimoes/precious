package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Migration is one numbered SQL file.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

var migrationFile = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// ErrSchemaTooNew is returned when the database was migrated by a newer binary.
var ErrSchemaTooNew = errors.New("store: database schema is newer than this binary supports")

// LoadMigrations reads NNNN_name.sql files from fsys. Versions must be unique
// and contiguous from 1.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var ms []Migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", e.Name(), err)
		}
		ms = append(ms, Migration{Version: v, Name: m[2], SQL: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	for i, m := range ms {
		if m.Version != i+1 {
			return nil, fmt.Errorf("store: migrations must be contiguous from 0001; found %04d at position %d", m.Version, i+1)
		}
	}
	if len(ms) == 0 {
		return nil, errors.New("store: no migrations found")
	}
	return ms, nil
}

// SchemaVersion returns the highest applied migration version (0 when none).
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

func migrate(ctx context.Context, db *sql.DB, ms []Migration) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	applied := map[int]string{}
	rows, err := db.QueryContext(ctx, `SELECT version, name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			rows.Close()
			return err
		}
		applied[v] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	latest := ms[len(ms)-1].Version
	for v := range applied {
		if v > latest {
			return fmt.Errorf("%w: database has version %d, binary supports up to %d", ErrSchemaTooNew, v, latest)
		}
	}
	for _, m := range ms {
		if name, ok := applied[m.Version]; ok {
			if name != m.Name {
				return fmt.Errorf("store: migration %04d applied as %q but binary has %q", m.Version, name, m.Name)
			}
			continue
		}
		if err := runTx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				m.Version, m.Name, time.Now().UnixMilli())
			return err
		}); err != nil {
			return fmt.Errorf("store: migration %04d_%s failed: %w", m.Version, m.Name, err)
		}
	}
	return nil
}
