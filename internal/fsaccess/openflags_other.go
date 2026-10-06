//go:build !unix

package fsaccess

import "os"

// openReadFlags opens a file read-only. Without POSIX FIFOs and terminals
// there is no open that could block or claim a terminal.
const openReadFlags = os.O_RDONLY
