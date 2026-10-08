package decisions

import "fmt"

// The quarantine of a source is the folder quarantineName at its top (r4
// design D1, D2), which every reader that shows or counts the disk leaves
// out. index exports the same name and conditions (index.QuarantineName,
// NotQuarantined, IsQuarantinePath); they are spelled out here because
// index's own tests import this package, so it cannot import index. A test
// checks that both agree.
const quarantineName = ".precious-quarantine"

// The quarantine's path bounds as SQL BLOB literals: the folder's own path,
// and the half-open range [Q/, Q0) of the paths below it.
var (
	quarantineHex = fmt.Sprintf("%x", quarantineName)
	quarantineSQL = "X'" + quarantineHex + "'"
	quarantineLo  = "X'" + quarantineHex + "2f'"
	quarantineHi  = "X'" + quarantineHex + "30'"
)

// notQuarantined is index.NotQuarantined: a residual condition, on the
// entries row aliased alias, that holds outside the quarantine. alias is
// always a constant identifier of this package's statements.
func notQuarantined(alias string) string {
	col := alias + ".path"
	return "NOT (" + col + " = " + quarantineSQL + " OR (" + col + " >= " + quarantineLo + " AND " + col + " < " +
		quarantineHi + "))"
}

// quarantinePath is index.IsQuarantinePath: whether an entry path of a
// source is its quarantine folder or lies below it.
func quarantinePath(path []byte) bool {
	n := len(quarantineName)
	return string(path) == quarantineName ||
		len(path) > n && path[n] == '/' && string(path[:n]) == quarantineName
}
