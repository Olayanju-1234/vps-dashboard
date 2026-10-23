package host

import (
	"fmt"
	"syscall"
)

// Statfs returns a filesystem's size and the space available to non-root users.
func Statfs(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("host: statfs %s: %w", path, err)
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs, nil
}
