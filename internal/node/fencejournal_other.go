//go:build !linux && !darwin

package node

import (
	"errors"
	"os"
)

// Supported() refuses these systems. The journal needs flock, so it never
// opens here and exists only so the package compiles.
var errNoFlock = errors.New("the fence journal needs flock, which this system lacks")

func openLockFile(string) (*os.File, error) { return nil, errNoFlock }

func flock(*os.File) error { return errNoFlock }

func tryFlock(*os.File) (bool, error) { return false, errNoFlock }

func funlock(*os.File) error { return errNoFlock }
