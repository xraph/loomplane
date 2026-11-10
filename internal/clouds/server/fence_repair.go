package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// FenceRepair is a review of a server's fence journal, or what repairing it
// did. A deployment clears a dirty mark itself only when the mark names an
// exec Docker shows finished. Anything else, such as a bare mark older
// helpers left, needs this reviewed repair.
type FenceRepair struct {
	// Daemon is the Docker daemon the journal belongs to.
	Daemon string `json:"daemon"`
	// Dirty is the journal's mark: false, true for an unknown Docker change,
	// or the exec a helper was running, as the journal holds it.
	Dirty any `json:"dirty"`
	// Review is a digest of the journal as it was read. A repair names it,
	// and is refused if the journal has changed since.
	Review string `json:"review"`
	// Running lists the execs still running in Loomplane's containers. A
	// repair is refused while there are any.
	Running []RunningExec `json:"running"`
	// Repaired says the repair cleared the mark. It stays false in a review
	// and when the journal was already clean.
	Repaired bool `json:"repaired"`
}

// RunningExec is an exec still running in a container Loomplane owns.
type RunningExec struct {
	Container string `json:"container"`
	Name      string `json:"name"`
	Exec      string `json:"exec"`
}

// ErrFenceRepairRefused is a repair the server's helper turned down, with
// its reason.
var ErrFenceRepairRefused = errors.New("fence repair refused")

var fenceReview = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ReviewFenceRepair reads the server's fence journal and the execs still
// running in Loomplane's containers, without changing anything.
func (c *Client) ReviewFenceRepair(ctx context.Context) (*FenceRepair, error) {
	return c.fenceRepair(ctx, "repair-check", "")
}

// RepairFence clears the journal's dirty mark and keeps its epochs. review
// is the digest ReviewFenceRepair returned. The helper refuses while any
// helper holds the daemon's lock, while an exec runs in a Loomplane
// container, or when the journal is no longer the one reviewed.
func (c *Client) RepairFence(ctx context.Context, review string) (*FenceRepair, error) {
	if !fenceReview.MatchString(review) {
		return nil, fmt.Errorf("%w: give the review digest a fence repair review returned", ErrInvalid)
	}
	return c.fenceRepair(ctx, "repair", review)
}

func (c *Client) fenceRepair(ctx context.Context, mode, review string) (*FenceRepair, error) {
	var body io.Reader
	if mode == "repair" {
		raw, err := json.Marshal(map[string]string{"review": review})
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	response, err := c.fencedRequest(ctx, mode, EffectFence{}, http.MethodPost, "/", body, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusOK {
		var out FenceRepair
		if err = json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("reading the fence helper's answer: %w", err)
		}
		return &out, nil
	}
	var refusal struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &refusal) == nil && refusal.Reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrFenceRepairRefused, refusal.Reason)
	}
	action := "review"
	if mode == "repair" {
		action = "repair"
	}
	return nil, fmt.Errorf("%w: the fence helper could not %s this server's journal; recheck the connection, since its Docker daemon may have changed", ErrFenceRepairRefused, action)
}
