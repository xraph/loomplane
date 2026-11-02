//go:build linux

package firecracker

import (
	"os"
	"path/filepath"
	"syscall"
)

func rootOwnedPath(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return ErrInvalid
	}
	for current := path; ; current = filepath.Dir(current) {
		info, e := os.Lstat(current)
		if e != nil {
			return e
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		if current == "/" {
			return nil
		}
	}
}

func prepareStateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return ErrInvalid
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return rootOwnedPath(path)
}
