package federation

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// ListInput selects one authorized environment and a bounded ID cursor.
type ListInput struct {
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	After         string `json:"after,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// DestinationPage is ordered by immutable destination ID, not display name.
type DestinationPage struct {
	Destinations []*domain.Destination `json:"destinations"`
	Next         string                `json:"next,omitempty"`
}

// ListDestinations derives organization from the principal and validates the parent.
func (s *Service) ListDestinations(ctx context.Context, p security.Principal, in ListInput) (*DestinationPage, error) {
	if err := s.authorize(ctx, p, security.DestinationRead, in.ProjectID); err != nil {
		return nil, err
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > 100 || len(in.After) > 128 {
		return nil, ErrInvalid
	}
	env, err := s.environments.GetEnvironment(ctx, p.OrgID, in.ProjectID, in.EnvironmentID)
	if errors.Is(err, projects.ErrEnvironmentNotFound) || errors.Is(err, projects.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, domain.Scope{OrgID: p.OrgID, ProjectID: env.ProjectID, EnvironmentID: env.ID}, in.After, in.Limit+1)
	if err != nil {
		return nil, err
	}
	page := &DestinationPage{Destinations: rows}
	if len(rows) > in.Limit {
		page.Destinations = rows[:in.Limit]
		page.Next = rows[in.Limit-1].ID
	}
	for _, d := range page.Destinations {
		if err := s.invalidateCapabilities(ctx, d); err != nil {
			return nil, err
		}
	}
	return page, nil
}

func (s *Service) invalidateCapabilities(ctx context.Context, d *domain.Destination) error {
	conn, err := s.connection(ctx, d.OrgID, d.ConnectionID)
	if err != nil {
		return err
	}
	physical, identityErr := ConnectionIdentity(conn, s.now())
	if identityErr != nil || physical != d.Identity || d.Quarantined {
		d.Capabilities = domain.CapabilitySet{}
	}
	return nil
}

// RenameDestination updates only the display name. Physical identity is immutable.
func (s *Service) RenameDestination(ctx context.Context, p security.Principal, id, name string, generation int64) (*domain.Destination, error) {
	if err := s.authorize(ctx, p, security.DestinationManage, id); err != nil {
		return nil, err
	}
	if !namePattern.MatchString(name) || generation < 1 {
		return nil, ErrInvalid
	}
	d, err := s.store.Rename(ctx, p.OrgID, id, name, generation, s.now().UTC())
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, "federation.destination.rename", d)
	if err := s.invalidateCapabilities(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// UnregisterDestination removes an unused local alias. Provider resources are
// imported and remain untouched. Restrictive references serialize concurrent use.
func (s *Service) UnregisterDestination(ctx context.Context, p security.Principal, id string, generation int64) error {
	if err := s.authorize(ctx, p, security.DestinationManage, id); err != nil {
		return err
	}
	if generation < 1 {
		return ErrInvalid
	}
	d, err := s.store.Get(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if d.Generation != generation {
		return ErrConflict
	}
	if err := s.store.Delete(ctx, p.OrgID, id, generation); err != nil {
		return err
	}
	s.record(ctx, p, "federation.destination.unregister", d)
	return nil
}
