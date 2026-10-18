package connections

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
)

type storageReader func(context.Context, *Connection, []byte) ([]owned.StoredVolume, error)

// StorageVolumes inspects safe metadata for one checked connection. Secrets
// stay inside the adapter, and project authorization filters every result.
func (s *Service) StorageVolumes(ctx context.Context, p security.Principal, connection string) ([]owned.StoredVolume, error) {
	return s.storageVolumes(ctx, p, connection, readStorageVolumes)
}

func (s *Service) storageVolumes(ctx context.Context, p security.Principal, connection string, read storageReader) ([]owned.StoredVolume, error) {
	if err := s.authz.Require(ctx, p, security.InfrastructureRead, ""); err != nil {
		return nil, err
	}
	c, err := s.Get(ctx, p, connection)
	if err != nil {
		return nil, err
	}
	if c.Cloud != Server && c.Cloud != Kubernetes {
		return nil, fmt.Errorf("%w: storage inventory needs a Docker or Kubernetes connection", ErrInvalid)
	}
	c, credential, err := s.Credential(ctx, p.OrgID, connection)
	if err != nil {
		if errors.Is(err, ErrNotReady) {
			return nil, fmt.Errorf("%w: recheck this connection before inspecting storage", ErrUnavailable)
		}
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	volumes, err := read(ctx, c, credential)
	if err != nil {
		return nil, fmt.Errorf("%w: storage inventory could not be read; recheck the connection and its storage permissions", ErrUnavailable)
	}
	visible := make([]owned.StoredVolume, 0, len(volumes))
	for _, volume := range volumes {
		if volume.ConnectionID != c.ID || volume.ProjectID == "" {
			continue
		}
		if checkErr := s.authz.Require(ctx, p, security.ProjectRead, volume.ProjectID); checkErr != nil {
			if errors.Is(checkErr, security.ErrForbidden) {
				continue
			}
			return nil, checkErr
		}
		visible = append(visible, volume)
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].Name < visible[j].Name })
	return visible, nil
}

func readStorageVolumes(ctx context.Context, c *Connection, credential []byte) ([]owned.StoredVolume, error) {
	switch c.Cloud {
	case Server:
		cfg, err := server.ParseConfig(credential, c.Details)
		if err != nil {
			return nil, err
		}
		maps.Copy(cfg.Details, c.Details)
		native, err := server.Dial(ctx, cfg)
		if err != nil {
			return nil, err
		}
		defer native.Close()
		return native.StorageVolumes(ctx, c.OrgID, c.ID)
	case Kubernetes:
		cfg, err := kubernetes.ParseConfig(credential, c.Details)
		if err != nil {
			return nil, err
		}
		if cfg.Details["endpoint"] != c.Details["endpoint"] || cfg.Details["cluster_ca"] != c.Details["cluster_ca"] {
			return nil, errors.New("the checked Kubernetes cluster identity changed")
		}
		maps.Copy(cfg.Details, c.Details)
		native, err := kubernetes.New(cfg)
		if err != nil {
			return nil, err
		}
		return native.StorageVolumes(ctx, c.OrgID, c.ID)
	}
	return nil, ErrInvalid
}
