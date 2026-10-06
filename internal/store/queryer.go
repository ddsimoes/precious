package store

import (
	"context"
	"database/sql"
)

// Queryer reads the database: the store's reader pool (*sql.DB) or a
// transaction (*sql.Tx), so that a read runs either on its own or inside a
// command's writing transaction.
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
