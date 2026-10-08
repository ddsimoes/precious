package relations

import "fmt"

// quarantineName is index.QuarantineName, the folder at the top of each
// source that holds its quarantined entries (r4 design D1). relations
// cannot import index, which imports it through sources, so it renders the
// same residual itself; a test asserts that both agree (r4 design
// Addendum B1).
const quarantineName = ".precious-quarantine"

// quarantineHex is the folder's path as hex, for the BLOB literals: its own
// path, and the half-open range [Q/, Q0) of the paths below it.
var quarantineHex = fmt.Sprintf("%x", quarantineName)

// notQuarantined returns index.NotQuarantined(alias): the residual, on the
// entries row aliased alias, that leaves out the quarantine folder and
// everything below it (r4 design D2). alias is one of the package's own
// constant aliases.
func notQuarantined(alias string) string {
	col := alias + ".path"
	return "NOT (" + col + " = X'" + quarantineHex + "' OR (" + col + " >= X'" + quarantineHex + "2f' AND " + col +
		" < X'" + quarantineHex + "30'))"
}

// isQuarantinePath is index.IsQuarantinePath: whether a source-relative
// path is the quarantine folder or lies below it.
func isQuarantinePath(path []byte) bool {
	n := len(quarantineName)
	return len(path) >= n && string(path[:n]) == quarantineName && (len(path) == n || path[n] == '/')
}

// notQuarantinedE is notQuarantined("e"), rendered once.
var notQuarantinedE = notQuarantined("e")
