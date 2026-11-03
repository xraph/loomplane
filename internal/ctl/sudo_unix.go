//go:build linux || darwin

package ctl

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"syscall"
)

// userConfigDir is os.UserConfigDir under a home directory, for a user who
// isn't the one running.
var userConfigDir = func() string {
	if runtime.GOOS == "darwin" {
		return "Library/Application Support"
	}
	return ".config"
}()

// readOwnedBy reads path without following a symlink, and only when it is a
// regular file owned by uid that group and others can't write. Root reads it
// on that user's behalf, so it must be theirs and only theirs to change.
func readOwnedBy(path, uid string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || strconv.FormatUint(uint64(st.Uid), 10) != uid {
		return nil, fmt.Errorf("%s is not owned by that user", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s can be written by other users (mode %04o); run chmod 600 on it", path, fi.Mode().Perm())
	}
	return io.ReadAll(io.LimitReader(f, 1<<20))
}
