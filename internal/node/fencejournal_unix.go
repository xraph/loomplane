//go:build linux || darwin

package node

import (
	"errors"
	"os"
	"syscall"
)

// openLockFile opens path read-write, creating it 0600, and fails if it is a
// symlink.
func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
}

// flock takes an exclusive flock on f, waiting for it.
func flock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
