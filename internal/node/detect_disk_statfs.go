//go:build linux || darwin

package node

import "syscall"

func diskGiB() int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0
	}
	return int64(st.Blocks*uint64(st.Bsize)) / (1 << 30) //nolint:gosec // disk sizes fit int64
}
