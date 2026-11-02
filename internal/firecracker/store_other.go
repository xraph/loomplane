//go:build !unix

package firecracker

import "os"

func lockStore(string) (*os.File, error) { return nil, ErrUnavailable }
