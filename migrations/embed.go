// Package migrations embeds the numbered SQL migrations (NNNN_name.sql). Each
// file runs in its own transaction; files must not contain PRAGMA statements or
// explicit transaction control.
package migrations

import "embed"

// FS holds every migration file.
//
//go:embed *.sql
var FS embed.FS
