package firecracker

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/xraph/loomplane/internal/secureconfig"
)

// RunnerHost is a Firecracker host that a Loomplane runner runs in its own
// process. It serves only build VMs, over the runner's node connection, so it
// has no listener, TLS or peers of its own: the node transport authenticates
// Loomplane, and the runner's Handler names the one peer it serves.
type RunnerHost struct {
	Host         *Host
	Capabilities Capabilities
	store        *Store
}

// OpenRunnerHost loads the host config at path, in the host agent's format,
// and runs the same checks the host agent does before it serves anything:
// KVM, the pinned Firecracker, jailer, kernel and guest image, the CNI
// plugins, cgroup v2 and the tools. hostID replaces the file's HostID,
// because a runner's host is its node. It needs Linux and root; elsewhere it
// returns ErrUnavailable.
func OpenRunnerHost(ctx context.Context, path, hostID string) (*RunnerHost, error) {
	if !identifier.MatchString(hostID) {
		return nil, ErrInvalid
	}
	var cfg HostConfig
	if err := secureconfig.ReadJSON(path, MaxControlBytes, true, &cfg); err != nil {
		return nil, err
	}
	cfg.HostID = hostID
	if len(cfg.Peers) > 0 {
		return nil, fmt.Errorf("%w: a runner's host takes no peers; Loomplane reaches it over the node connection", ErrInvalid)
	}
	if err := ValidatePlatform(cfg); err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(cfg.HostKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%w: HostKey must be 32 bytes in hex", ErrInvalid)
	}
	if xerr := prepareStateDirectory(cfg.StateDir); xerr != nil {
		return nil, xerr
	}
	store, err := OpenStore(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	driver, err := NewDriver(cfg, store, key)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	caps, err := driver.Check(ctx)
	if err == nil {
		err = platformReady(cfg, caps)
	}
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &RunnerHost{Host: &Host{Store: store, Driver: driver, Limits: cfg.Limits, Key: key}, Capabilities: caps, store: store}, nil
}

// Reconcile removes build VMs that outlived their build, at once and then
// every minute, until ctx ends, as the host agent does.
func (r *RunnerHost) Reconcile(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := r.Host.Reconcile(ctx); err != nil && ctx.Err() == nil {
			log.Print("Firecracker cleanup pending; inspect the ownership journal")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Close closes the host's journal. Build VMs keep running under the jailer
// until the next Reconcile, as they do when the host agent restarts.
func (r *RunnerHost) Close() error { return r.store.Close() }
