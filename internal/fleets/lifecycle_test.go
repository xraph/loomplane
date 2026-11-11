package fleets

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type actionFixtureStore struct {
	readFixtureStore
	reserved int
}

func (f *actionFixtureStore) ReserveAction(_ context.Context, _ FleetScope, _ security.Principal, id string, in fleet.ActionInput) (*fleet.Operation, error) {
	f.reserved++
	return &fleet.Operation{ID: "operation", InstanceID: id, Kind: in.Kind, Status: "waiting_approval"}, nil
}
func TestFleetActionChecksCallerBeforeReservation(t *testing.T) {
	store := &actionFixtureStore{}
	service := NewService(Deps{Store: store, Authz: createAuth{}})
	in := fleet.ActionInput{Kind: "suspend", IdempotencyKey: "suspend", ExpectedRevision: 1, ExpectedGeneration: 1, Suspend: &fleet.SuspendInput{}}
	p := security.Principal{OrgID: "org", UserID: "operator", ReadOnly: true}
	if _, err := service.RequestAction(t.Context(), p, "instance", in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read-only: %v", err)
	}
	p.ReadOnly = false
	service.authz = readAuthorizer{}
	if _, err := service.RequestAction(t.Context(), p, "instance", in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("missing mutation permission: %v", err)
	}
	service.authz = createAuth{}
	in.Resume = &fleet.ResumeInput{}
	if _, err := service.RequestAction(t.Context(), p, "instance", in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ambiguous payload: %v", err)
	}
	if store.reserved != 0 {
		t.Fatal("invalid caller/request reserved a mutation")
	}
}
