package store

import (
	"database/sql/driver"
	"fmt"

	sqlite "modernc.org/sqlite"

	"precious/internal/domain"
)

// The SQL functions every connection has, registered with the driver before
// any database opens. Migrations use them where SQL alone cannot compute what
// the scanner writes.
func init() {
	// precious_display_name(name BLOB) is domain.DisplayName: the text the
	// name index holds for a raw name (migration 0003, r2b design D7).
	sqlite.MustRegisterDeterministicScalarFunction("precious_display_name", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			switch v := args[0].(type) {
			case []byte:
				return domain.DisplayName(v), nil
			case string:
				return domain.DisplayName([]byte(v)), nil
			case nil:
				return nil, nil
			default:
				return nil, fmt.Errorf("precious_display_name: a name is a blob, not %T", v)
			}
		})
}
