//go:build !linux && !darwin

package node

import "os"

// Supported() refuses these systems, so the ownership and symlink checks are
// no-ops here and exist only so the package compiles.
func fileOwner(os.FileInfo) (uint32, bool) { return 0, false }

func fileGroup(os.FileInfo) (uint32, bool) { return 0, false }

func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

func syncDir(string) error { return nil }
