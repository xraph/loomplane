//go:build !linux

package firecracker

func NewDriver(HostConfig, *Store, []byte) (MachineDriver, error) { return nil, ErrUnavailable }

func prepareStateDirectory(string) error { return ErrUnavailable }
