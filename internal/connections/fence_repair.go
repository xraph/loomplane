package connections

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
)

// ErrFenceRepairRefused is a fence repair the server's helper turned down.
// Its message says why and what to do.
var ErrFenceRepairRefused = server.ErrFenceRepairRefused

// FenceRepairInput accepts a fence repair review.
type FenceRepairInput struct {
	// Review is the digest a review of the same journal returned.
	Review string `json:"review"`
}

// ReviewFenceRepair reads a server connection's fence journal: its dirty
// mark, a digest of it, and the execs still running in Loomplane's
// containers. It changes nothing.
func (s *Service) ReviewFenceRepair(ctx context.Context, p security.Principal, id string) (*server.FenceRepair, error) {
	return s.fenceRepair(ctx, p, id, "")
}

// RepairFence clears the dirty mark on a server connection's fence journal,
// keeping its epochs, once an operator has reviewed it. The server refuses
// while a helper is alive, while an exec runs in a Loomplane container, or
// when the journal has changed since the review.
func (s *Service) RepairFence(ctx context.Context, p security.Principal, id string, in FenceRepairInput) (*server.FenceRepair, error) {
	if in.Review == "" {
		return nil, fmt.Errorf("%w: review the fence journal first, then repair with that review", ErrInvalid)
	}
	return s.fenceRepair(ctx, p, id, in.Review)
}

func (s *Service) fenceRepair(ctx context.Context, p security.Principal, id, review string) (*server.FenceRepair, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	c, err := s.Get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if c.Cloud != Server {
		return nil, fmt.Errorf("%w: only a server connection, over SSH or a joined node, has a fence journal", ErrInvalid)
	}
	c, credential, err := s.Credential(ctx, p.OrgID, id)
	if err != nil {
		if errors.Is(err, ErrNotReady) {
			return nil, fmt.Errorf("%w: recheck this connection before repairing its fence journal", ErrUnavailable)
		}
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	repair := s.FenceRepairer
	if repair == nil {
		repair = s.repairFence
	}
	out, err := repair(ctx, c, credential, review)
	switch {
	case errors.Is(err, server.ErrFenceRepairRefused):
		return nil, err
	case errors.Is(err, server.ErrInvalid):
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	case err != nil:
		return nil, fmt.Errorf("%w: the server could not be reached; recheck the connection", ErrUnavailable)
	}
	if out.Repaired {
		s.record(ctx, "cloud_connection.fence_repair", p, c)
	}
	return out, nil
}

func (s *Service) repairFence(ctx context.Context, c *Connection, credential []byte, review string) (*server.FenceRepair, error) {
	cfg, err := server.ParseConfig(credential, c.Details)
	if err != nil {
		return nil, err
	}
	maps.Copy(cfg.Details, c.Details)
	native, err := server.Connect(ctx, cfg, s.nodeStreams())
	if err != nil {
		return nil, err
	}
	defer native.Close()
	if review == "" {
		return native.ReviewFenceRepair(ctx)
	}
	return native.RepairFence(ctx, review)
}
