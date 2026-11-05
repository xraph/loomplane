package fleets

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type projectReadStore struct{ readFixtureStore }

func (projectReadStore) GetFleetForProject(_ context.Context, org, project string) (*fleet.Fleet, error) {
	if org != "org" || project != "project" {
		return nil, ErrNotFound
	}
	return &fleet.Fleet{ID: "fleet", OrgID: org, ProjectID: project}, nil
}
func TestFleetReadProjectScope(t *testing.T) {
	keys := &readVerifier{}
	s := NewService(Deps{Store: projectReadStore{}, Authz: readAuthorizer{}, Keys: keys})
	p := security.Principal{OrgID: "org", UserID: security.FleetSubject("key"), KeyID: "key", Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "project", FleetID: "fleet"}}
	f, err := s.GetProjectFleet(t.Context(), p, "project")
	if err != nil || f.ID != "fleet" {
		t.Fatalf("project fleet: %+v %v", f, err)
	}
	if _, err = s.GetProjectFleet(t.Context(), p, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign project: %v", err)
	}
	p.Fleet.FleetID = "sibling"
	if _, err = s.GetProjectFleet(t.Context(), p, "project"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sibling: %v", err)
	}
	p.Fleet.FleetID = "fleet"
	keys.err = security.ErrForbidden
	if _, err = s.GetProjectFleet(t.Context(), p, "project"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked: %v", err)
	}
}

type siblingReadStore struct{ projectReadStore }

func (s siblingReadStore) GetFleet(ctx context.Context, org, id string) (*fleet.Fleet, error) {
	if org == "org" && id == "sibling" {
		return &fleet.Fleet{ID: id, OrgID: org, ProjectID: "private-project"}, nil
	}
	return s.projectReadStore.GetFleet(ctx, org, id)
}
func (s siblingReadStore) GetInstance(ctx context.Context, org, id string) (*fleet.Instance, error) {
	if org == "org" && id == "private-instance" {
		return &fleet.Instance{ID: id, FleetID: "sibling"}, nil
	}
	return s.projectReadStore.GetInstance(ctx, org, id)
}
func (s siblingReadStore) GetOperation(ctx context.Context, org, id string) (*fleet.Operation, error) {
	if org == "org" && id == "private-operation" {
		return &fleet.Operation{ID: id, InstanceID: "private-instance"}, nil
	}
	return s.projectReadStore.GetOperation(ctx, org, id)
}
func TestFleetProjectReadHidesInaccessibleSibling(t *testing.T) {
	s := NewService(Deps{Store: siblingReadStore{}, Authz: readAuthorizer{}})
	p := security.Principal{OrgID: "org", UserID: "operator"}
	for _, id := range []string{"private-instance", "missing-instance"} {
		if _, err := s.GetFleetInstance(t.Context(), p, "fleet", id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("instance %s disclosed: %v", id, err)
		}
	}
	for _, id := range []string{"private-operation", "missing-operation"} {
		if _, err := s.GetFleetOperation(t.Context(), p, "fleet", id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("operation %s disclosed: %v", id, err)
		}
	}
	if got, err := s.GetFleetInstance(t.Context(), p, "fleet", "instance"); err != nil || got.ID != "instance" {
		t.Fatalf("own instance: %+v %v", got, err)
	}
	if got, err := s.GetFleetOperation(t.Context(), p, "fleet", "operation"); err != nil || got.ID != "operation" {
		t.Fatalf("own operation: %+v %v", got, err)
	}
	s.authz = readAuthorizer{err: security.ErrForbidden}
	if _, err := s.GetFleetInstance(t.Context(), p, "fleet", "instance"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked project: %v", err)
	}
}
