package connections

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// GPUInventory reads a server or cluster connection's GPUs live. It doesn't change the stored connection.
func (s *Service) GPUInventory(ctx context.Context, p security.Principal, id string) (*provider.GPUInventory, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountRead, ""); err != nil {
		return nil, err
	}
	c, err := s.Get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if c.Cloud != Server && c.Cloud != Kubernetes {
		return nil, fmt.Errorf("%w: GPUs can be read from a Docker server or a Kubernetes cluster", ErrInvalid)
	}
	c, credential, err := s.Credential(ctx, p.OrgID, id)
	if err != nil {
		if errors.Is(err, ErrNotReady) {
			return nil, fmt.Errorf("%w: recheck this connection before reading its GPUs", ErrUnavailable)
		}
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	read := s.GPUReader
	if read == nil {
		read = readGPUs
	}
	inv, err := read(ctx, c, credential)
	if err != nil {
		return nil, fmt.Errorf("%w: GPUs could not be read; recheck the connection", ErrUnavailable)
	}
	return &inv, nil
}

func readGPUs(ctx context.Context, c *Connection, credential []byte) (provider.GPUInventory, error) {
	switch c.Cloud {
	case Server:
		cfg, err := server.ParseConfig(credential, c.Details)
		if err != nil {
			return provider.GPUInventory{}, err
		}
		maps.Copy(cfg.Details, c.Details)
		native, err := server.Dial(ctx, cfg)
		if err != nil {
			return provider.GPUInventory{}, err
		}
		defer native.Close()
		return native.GPUs(ctx)
	case Kubernetes:
		cfg, err := kubernetes.ParseConfig(credential, c.Details)
		if err != nil {
			return provider.GPUInventory{}, err
		}
		if cfg.Details["endpoint"] != c.Details["endpoint"] || cfg.Details["cluster_ca"] != c.Details["cluster_ca"] {
			return provider.GPUInventory{}, errors.New("the checked Kubernetes cluster identity changed")
		}
		maps.Copy(cfg.Details, c.Details)
		native, err := kubernetes.New(cfg)
		if err != nil {
			return provider.GPUInventory{}, err
		}
		return native.GPUs(ctx)
	}
	return provider.GPUInventory{}, ErrInvalid
}
