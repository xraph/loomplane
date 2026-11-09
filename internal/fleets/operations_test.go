package fleets

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type createFixtureStore struct {
	readFixtureStore
	ReleaseStore
	OperationStore
	release  fleet.TemplateRelease
	reserved int
}

func (s *createFixtureStore) GetRelease(_ context.Context, scope FleetScope, id string) (*fleet.TemplateRelease, error) {
	if scope.OrgID != "org" || scope.ProjectID != "project" || scope.FleetID != "fleet" || id != s.release.ID {
		return nil, ErrNotFound
	}
	return &s.release, nil
}
func (s *createFixtureStore) ReserveCreate(_ context.Context, scope FleetScope, _ security.Principal, in fleet.CreateInstanceInput) (*fleet.OperationResult, error) {
	s.reserved++
	return &fleet.OperationResult{Instance: fleet.Instance{ID: "instance", FleetID: scope.FleetID, ExternalRef: in.ExternalRef}, Operation: fleet.Operation{ID: "operation", Status: "waiting_approval"}}, nil
}

type createAuth struct{}

func (createAuth) Require(_ context.Context, _ security.Principal, perm security.Permission, project string) error {
	if project != "project" || (perm != security.ProjectRead && perm != security.DeploymentCreate) {
		return security.ErrForbidden
	}
	return nil
}
func TestFleetCreateValidatesBeforeReservation(t *testing.T) {
	release, _, in := renderFixture(t)
	in.ExternalRef, in.IdempotencyKey = "synthetic", "create-synthetic"
	store := &createFixtureStore{release: release}
	s := NewService(Deps{Store: store, Authz: createAuth{}})
	p := security.Principal{OrgID: "org", UserID: "operator"}
	bad := in
	bad.ProfileID = "missing"
	if _, err := s.CreateInstance(t.Context(), p, "fleet", bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad profile: %v", err)
	}
	if store.reserved != 0 {
		t.Fatal("invalid input reserved quota")
	}
	p.ReadOnly = true
	if _, err := s.CreateInstance(t.Context(), p, "fleet", in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read-only create: %v", err)
	}
	p.ReadOnly = false
	got, err := s.CreateInstance(t.Context(), p, "fleet", in)
	if err != nil || got.Operation.Status != "waiting_approval" || got.Instance.ExternalRef != "synthetic" {
		t.Fatalf("reservation: %+v %v", got, err)
	}
}

type dispatchFixture struct {
	calls int
	fail  bool
}

func (d *dispatchFixture) RunFleetInstance(_ context.Context, id string) error {
	if id != "operation" {
		return ErrNotFound
	}
	d.calls++
	if d.fail {
		return context.DeadlineExceeded
	}
	return nil
}

type outboxFixture struct {
	readFixtureStore
	acknowledged bool
	failAck      bool
	deferred     int
}

func (o *outboxFixture) PendingOperations(context.Context, int) ([]OperationDispatch, error) {
	if o.acknowledged {
		return nil, nil
	}
	return []OperationDispatch{{OrgID: "org", OperationID: "operation"}}, nil
}
func (o *outboxFixture) MarkOperationDispatched(_ context.Context, org, id string) error {
	if org != "org" || id != "operation" {
		return ErrNotFound
	}
	if o.failAck {
		return context.DeadlineExceeded
	}
	o.acknowledged = true
	return nil
}
func (o *outboxFixture) DeferOperationDispatch(_ context.Context, org, id string) error {
	if org != "org" || id != "operation" {
		return ErrNotFound
	}
	o.deferred++
	return nil
}
func TestFleetCreateDispatchAmbiguousResponse(t *testing.T) {
	outbox := &outboxFixture{}
	s := NewService(Deps{Store: outbox})
	workflow := &dispatchFixture{fail: true}
	if err := s.DispatchPendingOperations(t.Context(), workflow, 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if outbox.acknowledged {
		t.Fatal("lost response erased pending work")
	}
	workflow.fail = false
	outbox.failAck = true
	if err := s.DispatchPendingOperations(t.Context(), workflow, 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	outbox.failAck = false
	if err := s.DispatchPendingOperations(t.Context(), workflow, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchPendingOperations(t.Context(), workflow, 10); err != nil {
		t.Fatal(err)
	}
	if workflow.calls != 3 || !outbox.acknowledged || outbox.deferred != 2 {
		t.Fatalf("dispatch replay: calls=%d ack=%t", workflow.calls, outbox.acknowledged)
	}
}

type executionFixture struct {
	readFixtureStore
	execution CreateExecution
}

func (s *executionFixture) LoadCreateExecution(context.Context, string) (*CreateExecution, error) {
	out := s.execution
	return &out, nil
}

func TestFleetCreateStaleObservation(t *testing.T) {
	release, instance, in := renderFixture(t)
	in.ExternalRef, in.IdempotencyKey = "synthetic", "create-synthetic"
	instance.ExternalRef = in.ExternalRef
	hash, err := reservationHash(in)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := RenderInstance(release, instance, in)
	if err != nil {
		t.Fatal(err)
	}
	store := &executionFixture{execution: CreateExecution{Scope: FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}, Instance: instance, Operation: fleet.Operation{ID: "operation", InstanceID: instance.ID, Kind: "create", Status: "waiting_approval", Generation: 1, RequestHash: hash}, Intent: CreateIntent{Principal: security.Principal{OrgID: "org", UserID: "operator"}, Input: in, Snapshot: *snapshot}}}
	s := NewService(Deps{Store: store})
	if _, err = s.LoadCreateExecution(t.Context(), "operation"); err != nil {
		t.Fatal(err)
	}
	store.execution.Instance.DesiredGeneration = 2
	if _, err = s.LoadCreateExecution(t.Context(), "operation"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale generation: %v", err)
	}
	store.execution.Instance.DesiredGeneration = 1
	store.execution.Intent.Snapshot.EnvironmentID = "sibling"
	if _, err = s.LoadCreateExecution(t.Context(), "operation"); !errors.Is(err, ErrConflict) {
		t.Fatalf("sibling environment: %v", err)
	}
}
