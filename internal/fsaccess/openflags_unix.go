//go:build unix

package fsaccess

import (
	"os"
	"syscall"
)

// openReadFlags opens a file read-only without blocking (an entry swapped for
// a FIFO between lstat and open must not hang the open) and without making a
// terminal device the controlling terminal.
const openReadFlags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOCTTY
