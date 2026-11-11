package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type namespaceRetryKeys struct{ expected security.Principal }

func (k namespaceRetryKeys) ValidatePrincipal(_ context.Context, p security.Principal) error {
	if !namespaceEqual(p, k.expected) {
		return security.ErrForbidden
	}
	return nil
}

type namespaceRetryQualification struct{ value NamespaceQualification }

func (q namespaceRetryQualification) QualifyInstanceNamespace(context.Context, fleets.OperationAuthority, fleets.InstanceSnapshot, ManagedResource) (NamespaceQualification, error) {
	return q.value, nil
}
func (namespaceRetryQualification) RevalidateInstanceNamespaceQualification(context.Context, InstanceNamespaceRecord, *ClusterOperation) error {
	return nil
}

func TestNamespacePostgresPublicReserveRetryWithAdvancedClock(t *testing.T) {
	for _, failStart := range []bool{false, true} {
		name := "normal"
		if failStart {
			name = "workflow-start-failure"
		}
		t.Run(name, func(t *testing.T) { namespacePublicRetry(t, failStart) })
	}
}

type namespaceRetryWorkflow struct{ fail bool }

var errNamespaceWorkflowStart = errors.New("fixture workflow start failed")

func (w *namespaceRetryWorkflow) StartClusterOperation(context.Context, string, string, int) error {
	if w.fail {
		return errNamespaceWorkflowStart
	}
	return nil
}

func namespacePublicRetry(t *testing.T, failStart bool) {
	t.Helper()
	f := newNamespacePGFixture(t)
	store := f.transactions.store
	resource, err := store.GetManagedResource(t.Context(), f.action.Record.OrgID, f.action.Record.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	actor := f.action.Principal
	fleetService := fleets.NewService(fleets.Deps{Store: fleets.NewPgStore(f.db), Projects: projects.NewPgStore(f.db), Keys: namespaceRetryKeys{expected: actor}, Authz: namespaceFixtureAuth{}})
	qualification := NamespaceQualification{ID: f.action.Record.Pins.QualificationID, ClusterID: resource.ID, ClusterCA: resource.Observation.CAHash, Incarnation: resource.Incarnation, EndpointFingerprint: endpointFingerprint(resource.Observation.Endpoint), ClusterRevision: resource.Revision, ExpiresAt: f.action.Record.QualificationExpiresAt, Policy: *f.action.Operation.Input.Attachment.InstancePolicy}
	s := NewService(Deps{ManagedClusters: config.ManagedClustersConfig{Enabled: true}, Clusters: store, Clouds: cloudprofiles.NewPgStore(f.db), RuntimeConnections: connections.NewPgStore(f.db), InstanceNamespaces: f.transactions, InstanceAuthority: fleetService, InstanceIsolation: namespaceRetryQualification{value: qualification}})
	workflow := &namespaceRetryWorkflow{fail: failStart}
	s.ClusterWorkflow = workflow
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	input := InstanceNamespaceInput{Authority: f.action.Record.CurrentAuthority, Snapshot: f.action.Snapshot, ClusterID: resource.ID}
	first, err := s.ReserveInstanceNamespace(t.Context(), actor, input)
	if first == nil || (failStart && !errors.Is(err, errNamespaceWorkflowStart)) || (!failStart && err != nil) {
		t.Fatalf("first public reserve: %v", err)
	}
	original, err := store.GetClusterOperation(t.Context(), actor.OrgID, f.action.Record.CloudID, first.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	workflow.fail = false
	now = now.Add(time.Second)
	retry, err := s.ReserveInstanceNamespace(t.Context(), actor, input)
	if err != nil {
		t.Fatalf("retry after successful public reserve with advanced clock: %v", err)
	}
	if !namespaceEqual(first, retry) {
		t.Fatal("public retry changed the original reservation")
	}
	after, err := store.GetClusterOperation(t.Context(), actor.OrgID, f.action.Record.CloudID, first.OperationID)
	if err != nil || !namespaceEqual(original, after) {
		t.Fatalf("public retry rewrote original input or deadline: %v", err)
	}
	changed := qualification
	changed.ID = "changed-qualification"
	s.InstanceIsolation = namespaceRetryQualification{value: changed}
	if _, err = s.ReserveInstanceNamespace(t.Context(), actor, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed qualification replayed: %v", err)
	}
	s.InstanceIsolation = namespaceRetryQualification{value: qualification}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_grants SET status='revoked',revision=revision+1 WHERE id=$1`, input.Authority.Request.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveInstanceNamespace(t.Context(), actor, input); err == nil {
		t.Fatal("revoked grant replayed public reservation")
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("public retry added an effect: %d %v", count, err)
	}
}

func TestNamespacePostgresConcurrentPublicReserve(t *testing.T) {
	f := newNamespacePGFixture(t)
	store := f.transactions.store
	resource, err := store.GetManagedResource(t.Context(), f.action.Record.OrgID, f.action.Record.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	actor := f.action.Principal
	fleetService := fleets.NewService(fleets.Deps{Store: fleets.NewPgStore(f.db), Projects: projects.NewPgStore(f.db), Keys: namespaceRetryKeys{expected: actor}, Authz: namespaceFixtureAuth{}})
	qualification := NamespaceQualification{ID: f.action.Record.Pins.QualificationID, ClusterID: resource.ID, ClusterCA: resource.Observation.CAHash, Incarnation: resource.Incarnation, EndpointFingerprint: endpointFingerprint(resource.Observation.Endpoint), ClusterRevision: resource.Revision, ExpiresAt: f.action.Record.QualificationExpiresAt, Policy: *f.action.Operation.Input.Attachment.InstancePolicy}
	s := NewService(Deps{ManagedClusters: config.ManagedClustersConfig{Enabled: true}, Clusters: store, Clouds: cloudprofiles.NewPgStore(f.db), RuntimeConnections: connections.NewPgStore(f.db), InstanceNamespaces: f.transactions, InstanceAuthority: fleetService, InstanceIsolation: namespaceRetryQualification{value: qualification}})

	blocker, observer := namespaceFixtureConnection(t, f), namespaceFixtureConnection(t, f)
	hold, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err = hold.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('cloud-organization/org',0))`); err != nil {
		t.Fatal(err)
	}
	firstTime := time.Now().UTC().Truncate(time.Microsecond)
	firstBuilt, secondBuilt := make(chan struct{}), make(chan struct{})
	s.now = func() time.Time { close(firstBuilt); return firstTime }
	second := NewService(Deps{ManagedClusters: config.ManagedClustersConfig{Enabled: true}, Clusters: store, Clouds: s.Clouds, RuntimeConnections: s.RuntimeConnections, InstanceNamespaces: f.transactions, InstanceAuthority: fleetService, InstanceIsolation: s.InstanceIsolation})
	second.now = func() time.Time { close(secondBuilt); return firstTime.Add(time.Second) }
	input := InstanceNamespaceInput{Authority: f.action.Record.CurrentAuthority, Snapshot: f.action.Snapshot, ClusterID: resource.ID}
	type outcome struct {
		receipt *InstanceNamespaceReceipt
		err     error
	}
	firstDone, secondDone := make(chan outcome, 1), make(chan outcome, 1)
	go func() { r, e := s.ReserveInstanceNamespace(t.Context(), actor, input); firstDone <- outcome{r, e} }()
	select {
	case <-firstBuilt:
	case <-time.After(3 * time.Second):
		t.Fatal("first missing-history preflight not completed")
	}
	namespaceAwaitBlocked(t, observer, blocker.PgConn().PID())
	go func() {
		r, e := second.ReserveInstanceNamespace(t.Context(), actor, input)
		secondDone <- outcome{r, e}
	}()
	select {
	case <-secondBuilt:
	case <-time.After(3 * time.Second):
		t.Fatal("second missing-history preflight not completed")
	}
	if releaseErr := hold.Rollback(t.Context()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	var first, retry outcome
	select {
	case first = <-firstDone:
	case <-time.After(8 * time.Second):
		t.Fatal("first call did not finish")
	}
	select {
	case retry = <-secondDone:
	case <-time.After(8 * time.Second):
		t.Fatal("concurrent retry did not finish")
	}
	if first.err != nil || retry.err != nil || first.receipt == nil || !namespaceEqual(first.receipt, retry.receipt) {
		t.Fatalf("concurrent public reserve: first=%v second=%v", first.err, retry.err)
	}
	operation, err := store.GetClusterOperation(t.Context(), actor.OrgID, f.action.Record.CloudID, first.receipt.OperationID)
	if err != nil || !operation.CreatedAt.Equal(firstTime) {
		t.Fatalf("concurrent retry replaced original time: %v", err)
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent retry added effect: %d %v", count, err)
	}
	changed := actor
	changed.UserID = "different-caller"
	if _, err = second.ReserveInstanceNamespace(t.Context(), changed, input); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("changed caller replayed: %v", err)
	}
	changedQualification := qualification
	changedQualification.ID = "changed-qualification"
	second.InstanceIsolation = namespaceRetryQualification{value: changedQualification}
	if _, err = second.ReserveInstanceNamespace(t.Context(), actor, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed pins replayed: %v", err)
	}
}
