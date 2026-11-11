package fleets

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetActionHashKeepsReplayPins(t *testing.T) {
	in := fleet.ActionInput{Kind: "update", IdempotencyKey: "first", ExpectedRevision: 4, ExpectedGeneration: 3, Update: &fleet.UpdateInput{ReleaseID: "release"}}
	original, err := actionRequestHash(in)
	if err != nil {
		t.Fatal(err)
	}
	in.IdempotencyKey = "second"
	same, err := actionRequestHash(in)
	if err != nil || same != original {
		t.Fatalf("key changed canonical body: %v", err)
	}
	in.ExpectedRevision++
	changed, err := actionRequestHash(in)
	if err != nil || changed == original {
		t.Fatalf("lost expected revision: %v", err)
	}
	in.ExpectedRevision--
	in.Update.Parameters = map[string]fleet.ParameterValue{}
	reset, err := actionRequestHash(in)
	if err != nil || reset == original {
		t.Fatalf("parameter reset collapsed into retain: %v", err)
	}
	in.Update.Parameters = nil
	in.Update.SecretRefs = map[string]fleet.SecretRef{}
	reset, err = actionRequestHash(in)
	if err != nil || reset == original {
		t.Fatalf("secret reset collapsed into retain: %v", err)
	}
}

type actionReadFixture struct {
	readFixtureStore
	hash  string
	reads int
}

func (f *actionReadFixture) GetInstance(ctx context.Context, org, id string) (*fleet.Instance, error) {
	instance, err := f.readFixtureStore.GetInstance(ctx, org, id)
	if err == nil {
		instance.Revision = 9
		instance.DesiredGeneration = 8
	}
	return instance, err
}
func (f *actionReadFixture) FindAction(_ context.Context, scope FleetScope, id string, in fleet.ActionInput, hash string) (*fleet.Operation, error) {
	f.reads++
	if scope != (FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}) || id != "instance" || in.IdempotencyKey != "accepted" {
		return nil, ErrNotFound
	}
	if hash != f.hash {
		return nil, ErrConflict
	}
	return &fleet.Operation{ID: "operation", InstanceID: id, Generation: 4, Kind: "update", Status: "succeeded"}, nil
}
func TestFleetFindActionRecoversWithoutNewAdmission(t *testing.T) {
	in := fleet.ActionInput{Kind: "update", IdempotencyKey: "accepted", ExpectedRevision: 4, ExpectedGeneration: 3, Update: &fleet.UpdateInput{ReleaseID: "release"}}
	hash, err := actionRequestHash(in)
	if err != nil {
		t.Fatal(err)
	}
	store := &actionReadFixture{hash: hash}
	keys := &readVerifier{}
	service := NewService(Deps{Store: store, Authz: readAuthorizer{}, Keys: keys})
	p := security.Principal{OrgID: "org", UserID: security.FleetSubject("key"), KeyID: "key", Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "project", FleetID: "fleet"}}
	op, err := service.FindAction(t.Context(), p, "instance", in)
	if err != nil || op.ID != "operation" {
		t.Fatalf("lost response after instance advanced: %+v %v", op, err)
	}
	in.IdempotencyKey = "absent"
	if _, err = service.FindAction(t.Context(), p, "instance", in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent request: %v", err)
	}
	keys.err = security.ErrForbidden
	before := store.reads
	if _, err = service.FindAction(t.Context(), p, "instance", in); !errors.Is(err, security.ErrForbidden) || store.reads != before {
		t.Fatalf("revoked lookup leaked receipt: %v", err)
	}
}
