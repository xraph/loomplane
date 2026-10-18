package planning

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	federationstore "github.com/xraph/loomplane/internal/federation"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
)

type federationDestinationReader interface {
	GetFederationDestination(ctx context.Context, orgID, id string, now time.Time) (*domain.Destination, error)
}

// GetFederationDestination loads a persisted snapshot and invalidates capabilities
// if its checked connection metadata no longer matches the pinned identity.
func (s *PgStore) GetFederationDestination(ctx context.Context, orgID, id string, now time.Time) (*domain.Destination, error) {
	d, err := federationstore.NewPgStore(s.db).Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	c, err := connections.NewPgStore(s.db).GetConnection(ctx, orgID, d.ConnectionID)
	if err != nil {
		return nil, err
	}
	identity, identityErr := federationstore.ConnectionIdentity(c, now)
	if identityErr != nil || identity != d.Identity || d.Quarantined {
		d.Capabilities = domain.CapabilitySet{}
	}
	return d, nil
}

func (s *Service) federationInput(ctx context.Context, p *Plan, m *manifest.Manifest) (*FederationInput, error) {
	if m.Version != manifest.FederationVersion {
		return nil, nil
	}
	in := &FederationInput{Scope: domain.Scope{OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID}, Environment: p.Environment, Manifest: m, Now: s.now().UTC(), Destinations: map[string]*domain.Destination{}}
	reader, ok := s.store.(federationDestinationReader)
	if !ok || m.Federation == nil {
		return in, nil
	}
	for _, name := range federationKeys(m.Federation.Destinations) {
		id := m.Federation.Destinations[name].For(p.Environment)
		d, err := reader.GetFederationDestination(ctx, p.OrgID, id, in.Now)
		if errors.Is(err, federationstore.ErrNotFound) || errors.Is(err, connections.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if d.Scope == in.Scope {
			in.Destinations[id] = d
		}
	}
	return in, nil
}
