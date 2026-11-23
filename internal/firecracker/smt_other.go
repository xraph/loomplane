//go:build !linux

package firecracker

// SMTActive is unknown off Linux, where no runner runs.
func SMTActive() (bool, error) { return true, ErrUnavailable }
