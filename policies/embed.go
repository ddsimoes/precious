// Package policies embeds precious's versioned policy files. Each file records
// its own version, and every result derived from it records that version.
// Loading and validation belong to internal/rules, which applies them.
package policies

import _ "embed"

// Markers is the markers file: signals and file kinds.
//
//go:embed markers/v3.toml
var Markers []byte

// Rules is the rules file: categories, traits, and their conditions.
//
//go:embed rules/v2.toml
var Rules []byte
