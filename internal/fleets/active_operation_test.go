package fleets

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type activeOperationFixture struct {
	readFixtureStore
	reads int
}

func (f *activeOperationFixture) GetActiveOperation(_ context.Context, scope FleetScope, id string) (*fleet.Operation, error) {
	f.reads++
	if scope != (FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}) || id != "instance" {
		return nil, ErrNotFound
	}
	return &fleet.Operation{ID: "operation", InstanceID: id, Kind: "create", Status: "needs_attention", Generation: 1}, nil
}
func TestFleetActiveOperationRequiresCurrentScope(t *testing.T) {
	store := &activeOperationFixture{}
	keys := &readVerifier{}
	service := NewService(Deps{Store: store, Keys: keys})
	p := security.Principal{OrgID: "org", UserID: security.FleetSubject("key"), KeyID: "key", Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "project", FleetID: "fleet"}}
	op, err := service.GetActiveOperation(t.Context(), p, "instance")
	if err != nil || op.ID != "operation" {
		t.Fatalf("read: %+v %v", op, err)
	}
	keys.err = security.ErrForbidden
	before := store.reads
	if _, err = service.GetActiveOperation(t.Context(), p, "instance"); !errors.Is(err, security.ErrForbidden) || before != store.reads {
		t.Fatalf("revoked receipt disclosed: %v", err)
	}
}
