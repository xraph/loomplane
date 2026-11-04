package fleets

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type readFixtureStore struct {
	Store
	ReadStore
}

func (readFixtureStore) GetFleet(_ context.Context, org, id string) (*fleet.Fleet, error) {
	if org != "org" || id != "fleet" {
		return nil, ErrNotFound
	}
	return &fleet.Fleet{ID: id, OrgID: org, ProjectID: "project"}, nil
}
func (readFixtureStore) GetInstance(_ context.Context, org, id string) (*fleet.Instance, error) {
	if org != "org" || id != "instance" {
		return nil, ErrNotFound
	}
	return &fleet.Instance{ID: id, FleetID: "fleet"}, nil
}
func (readFixtureStore) GetOperation(_ context.Context, org, id string) (*fleet.Operation, error) {
	if org != "org" || id != "operation" {
		return nil, ErrNotFound
	}
	return &fleet.Operation{ID: id, InstanceID: "instance"}, nil
}

type readAuthorizer struct{ err error }

func (a readAuthorizer) Require(_ context.Context, _ security.Principal, permission security.Permission, project string) error {
	if permission != security.ProjectRead || project != "project" {
		return security.ErrForbidden
	}
	return a.err
}

type readVerifier struct{ err error }

func (v *readVerifier) ValidatePrincipal(context.Context, security.Principal) error { return v.err }

func TestFleetReadScopeAndRevocation(t *testing.T) {
	keys := &readVerifier{}
	s := NewService(Deps{Store: readFixtureStore{}, Authz: readAuthorizer{}, Keys: keys})
	p := security.Principal{OrgID: "org", UserID: security.FleetSubject("key"), KeyID: "key", Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "project", FleetID: "fleet"}}
	got, err := s.GetOperation(t.Context(), p, "operation")
	if err != nil || got.ID != "operation" {
		t.Fatalf("operation: %+v %v", got, err)
	}
	p.Fleet.FleetID = "other"
	if _, err = s.GetOperation(t.Context(), p, "operation"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sibling fleet disclosed: %v", err)
	}
	p.Fleet.FleetID = "fleet"
	keys.err = security.ErrForbidden
	if _, err = s.GetInstance(t.Context(), p, "instance"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked key read: %v", err)
	}
	p = security.Principal{OrgID: "other", UserID: "operator"}
	if _, err = s.GetInstance(t.Context(), p, "instance"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign instance disclosed: %v", err)
	}
}

func TestFleetReadHumanPermission(t *testing.T) {
	s := NewService(Deps{Store: readFixtureStore{}, Authz: readAuthorizer{err: security.ErrForbidden}})
	p := security.Principal{OrgID: "org", UserID: "operator", ReadOnly: true}
	if _, err := s.GetOperation(t.Context(), p, "operation"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("missing permission: %v", err)
	}
	s.authz = readAuthorizer{}
	if _, err := s.GetInstance(t.Context(), p, "instance"); err != nil {
		t.Fatal(err)
	}
}

func TestFleetReadPaginationValidation(t *testing.T) {
	s := NewService(Deps{Store: readFixtureStore{}, Authz: readAuthorizer{}})
	p := security.Principal{OrgID: "org", UserID: "operator"}
	for _, in := range []fleet.ListInstancesInput{{Limit: -1}, {Limit: 201}, {After: "invalid"}} {
		if _, err := s.ListInstances(t.Context(), p, "fleet", in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid pagination %+v: %v", in, err)
		}
	}
}
