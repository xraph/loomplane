package client

import (
	"context"
	"encoding/json"
	"net/http"
)

// FenceRepair is a review of an SSH server's fence journal, or what
// repairing it did.
type FenceRepair struct {
	// Daemon is the Docker daemon the journal belongs to.
	Daemon string `json:"daemon"`
	// Dirty is the journal's mark: false, true for an unknown Docker change,
	// or the exec a helper was running.
	Dirty json.RawMessage `json:"dirty"`
	// Review is a digest of the journal as it was read. A repair names it.
	Review string `json:"review"`
	// Running lists the execs still running in Loomplane's containers. A
	// repair is refused while there are any.
	Running  []RunningExec `json:"running"`
	Repaired bool          `json:"repaired"`
}

// RunningExec is an exec still running in a container Loomplane owns.
type RunningExec struct {
	Container string `json:"container"`
	Name      string `json:"name"`
	Exec      string `json:"exec"`
}

// ReviewFenceRepair reads a server connection's fence journal without
// changing it.
func (c *Client) ReviewFenceRepair(ctx context.Context, connection string) (*FenceRepair, error) {
	var out FenceRepair
	err := c.do(ctx, http.MethodPost, "/api/v1/connections/"+esc(connection)+"/fence-repair/review", nil, &out)
	return &out, err
}

// RepairFence clears the dirty mark the review named.
func (c *Client) RepairFence(ctx context.Context, connection, review string) (*FenceRepair, error) {
	var out FenceRepair
	err := c.do(ctx, http.MethodPost, "/api/v1/connections/"+esc(connection)+"/fence-repair", map[string]string{"review": review}, &out)
	return &out, err
}
