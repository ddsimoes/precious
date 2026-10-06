package fsaccess

import (
	"syscall"
	"time"
)

// sysInfo fills identity, link count, allocation, and change time from a
// Linux stat result.
func sysInfo(info *EntryInfo, sys any) {
	st, ok := sys.(*syscall.Stat_t)
	if !ok {
		return
	}
	info.Dev = uint64(st.Dev)
	info.Ino = uint64(st.Ino)
	info.Nlink = uint64(st.Nlink)
	info.Blocks = int64(st.Blocks)
	info.Ctime = time.Unix(st.Ctim.Unix())
}
