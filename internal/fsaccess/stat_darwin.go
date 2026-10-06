package fsaccess

import (
	"syscall"
	"time"
)

// sysInfo fills identity, link count, allocation, and change time from a
// Darwin stat result.
func sysInfo(info *EntryInfo, sys any) {
	st, ok := sys.(*syscall.Stat_t)
	if !ok {
		return
	}
	info.Dev = uint64(st.Dev)
	info.Ino = st.Ino
	info.Nlink = uint64(st.Nlink)
	info.Blocks = st.Blocks
	info.Ctime = time.Unix(st.Ctimespec.Unix())
}
