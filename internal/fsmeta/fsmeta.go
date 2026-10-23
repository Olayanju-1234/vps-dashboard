// Package fsmeta describes files by metadata only: owner, mode, times.
package fsmeta

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// Stat returns a file's metadata without opening it. It does not follow a
// symlink: a link where a file should be is reported, not resolved.
func Stat(path string) state.FileMeta {
	m := state.FileMeta{Path: path}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m
	}
	if err != nil {
		m.Error = fmt.Sprintf("stat: %v", err)
		return m
	}
	m.Exists = true
	m.Size = fi.Size()
	m.ModTime = fi.ModTime().UTC()
	m.Mode = fmt.Sprintf("%04o", fi.Mode().Perm())
	if uid, gid, ok := owner(fi); ok {
		m.Owner = userName(uid)
		m.Group = groupName(gid)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		m.Error = "is a symlink; not followed"
	} else if !fi.Mode().IsRegular() {
		m.Error = "is not a regular file"
	}
	return m
}

// OpenRegular opens a file for reading only if it is a regular file, not a
// symlink, FIFO or device, and (when expectOwner is set) owned by that user.
// The probe can read anything, so it must not be steered to another file.
func OpenRegular(path, expectOwner string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("fsmeta: open: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("fsmeta: stat: %w", err)
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("fsmeta: not a regular file; not read")
	}
	if expectOwner != "" {
		if uid, _, ok := owner(fi); ok && userName(uid) != expectOwner {
			f.Close()
			return nil, fmt.Errorf("fsmeta: owned by %s, expected %s; not read", userName(uid), expectOwner)
		}
	}
	return f, nil
}

// Changed is the later of a path's mtime and ctime: a rename or chown that
// installs a deploy bumps ctime even when it preserves mtime.
func Changed(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("fsmeta: %w", err)
	}
	t := fi.ModTime()
	if c, ok := ctime(fi); ok && c.After(t) {
		t = c
	}
	return t.UTC(), nil
}

var (
	nameMu sync.Mutex
	users  = map[uint32]string{}
	groups = map[uint32]string{}
)

func userName(uid uint32) string {
	nameMu.Lock()
	defer nameMu.Unlock()
	if n, ok := users[uid]; ok {
		return n
	}
	n := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(n); err == nil {
		n = u.Username
	}
	users[uid] = n
	return n
}

func groupName(gid uint32) string {
	nameMu.Lock()
	defer nameMu.Unlock()
	if n, ok := groups[gid]; ok {
		return n
	}
	n := strconv.FormatUint(uint64(gid), 10)
	if g, err := user.LookupGroupId(n); err == nil {
		n = g.Name
	}
	groups[gid] = n
	return n
}

// UserName resolves a uid, for socket owners.
func UserName(uid uint32) string { return userName(uid) }
