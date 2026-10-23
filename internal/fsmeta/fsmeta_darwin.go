package fsmeta

import (
	"io/fs"
	"syscall"
	"time"
)

func owner(fi fs.FileInfo) (uint32, uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return st.Uid, st.Gid, true
}

func ctime(fi fs.FileInfo) (time.Time, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(st.Ctimespec.Sec, st.Ctimespec.Nsec), true
}
