package search

import "fmt"

// The quarantine of a source is the folder quarantineName at its top (r4
// design D1, D2), which every search and copy count leaves out. index
// exports the same name and conditions (index.QuarantineName,
// InQuarantine, NotQuarantined); they are spelled out here because index's
// own tests import this package (through decisions), so it cannot import
// index. A test checks that both agree.
const quarantineName = ".precious-quarantine"

// The quarantine's path bounds as SQL BLOB literals: the folder's own path,
// and the half-open range [Q/, Q0) of the paths below it. A Q || '/' in SQL
// would be TEXT, which compares above every BLOB path.
var (
	quarantineHex = fmt.Sprintf("%x", quarantineName)
	quarantineSQL = "X'" + quarantineHex + "'"
	quarantineLo  = "X'" + quarantineHex + "2f'"
	quarantineHi  = "X'" + quarantineHex + "30'"
)

// inQuarantine is index.InQuarantine: a residual condition, on the entries
// row aliased alias, that holds for the quarantine folder of its source and
// every entry below it. alias is always a constant identifier of this
// package's statements.
func inQuarantine(alias string) string {
	col := alias + ".path"
	return "(" + col + " = " + quarantineSQL + " OR (" + col + " >= " + quarantineLo + " AND " + col + " < " +
		quarantineHi + "))"
}

// notQuarantined is index.NotQuarantined, the negation of inQuarantine.
func notQuarantined(alias string) string { return "NOT " + inQuarantine(alias) }
