//go:build unix

package firecracker

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockStore(dir string) (*os.File, error) {
	fd, err := unix.Open(filepath.Join(dir, "agent.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "agent.lock")
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}
