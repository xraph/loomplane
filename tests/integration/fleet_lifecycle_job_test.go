//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/pkg/manifest"
)

func lifecycleFixture(t *testing.T) (fleetFixture, *deployments.PgStore, deployments.LifecycleJobRun) {
	t.Helper()
	f := newFleetFixture(t, 2)
	reserved, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_operations SET status='running' WHERE id=$1`, reserved.Operation.ID); err != nil {
		t.Fatal(err)
	}
	run := deployments.LifecycleJobRun{OrgID: f.scope.OrgID, ProjectID: f.scope.ProjectID, FleetID: f.scope.FleetID, InstanceID: reserved.Instance.ID, EnvironmentID: reserved.Instance.EnvironmentID, OperationID: reserved.Operation.ID, Generation: 1, OwnershipEpoch: 1, Name: "migrate", SchemaLockID: "resource/database/schema", Namespace: "instance-a", NamespaceUID: "namespace-uid", ConnectionID: "connection", ClusterCA: strings.Repeat("a", 64), Architecture: "arm64", CapabilityRevision: owned.LifecycleCapabilityRevision, Job: manifest.LifecycleJob{Phase: "pre_deploy", Image: "registry.example.test/migrate@sha256:" + strings.Repeat("a", 64), TimeoutSeconds: 60, MaxAttempts: 2, SchemaLock: "database"}}
	return f, deployments.NewPgStore(f.db), run
}
func TestLifecycleJobRetryUsesReceipt(t *testing.T) {
	f, store, run := lifecycleFixture(t)
	state, err := store.BeginLifecycleJob(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.IssueLifecycleJob(t.Context(), run, 1)
	if err != nil || !first {
		t.Fatal(first, err)
	}
	duplicate, err := store.IssueLifecycleJob(t.Context(), run, 1)
	if err != nil || duplicate {
		t.Fatal("duplicate issuance", duplicate, err)
	}
	receipt := state.Receipt
	receipt.JobUID = "job-uid"
	receipt.SecretUID = "secret-uid"
	receipt.State = "succeeded"
	if err = store.RecordLifecycleJob(t.Context(), run, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_job_receipts SET body='changed' WHERE run_id=$1`, run.ID()); err == nil {
		t.Fatal("terminal receipt mutated")
	}
	// A new store represents a worker recovering after the response was lost.
	recovered, err := store.BeginLifecycleJob(t.Context(), run)
	if err != nil || recovered.Receipt != receipt {
		t.Fatal(recovered, err)
	}
	if err = store.CleanLifecycleJob(t.Context(), run, receipt); err != nil {
		t.Fatal(err)
	}
	recovered, err = store.BeginLifecycleJob(t.Context(), run)
	if err != nil || !recovered.Cleaned || recovered.Receipt != receipt {
		t.Fatal(recovered, err)
	}
	changed := run
	changed.Job.Image = "registry.example.test/migrate@sha256:" + strings.Repeat("b", 64)
	if _, err = store.BeginLifecycleJob(t.Context(), changed); err == nil {
		t.Fatal("receipt accepted a changed job")
	}
}
func TestLifecycleJobSchemaLock(t *testing.T) {
	_, store, run := lifecycleFixture(t)
	if _, err := store.BeginLifecycleJob(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	other := run
	other.Name = "bootstrap"
	other.Namespace = "another-namespace"
	other.NamespaceUID = "another-namespace-uid"
	if _, err := store.BeginLifecycleJob(t.Context(), other); !errors.Is(err, owned.ErrLifecyclePending) {
		t.Fatal("schema lock bypassed", err)
	}
	state, err := store.BeginLifecycleJob(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.IssueLifecycleJob(t.Context(), run, 1); err != nil {
		t.Fatal(err)
	}
	receipt := state.Receipt
	receipt.JobUID = "job"
	receipt.SecretUID = "secret"
	receipt.State = "succeeded"
	if err = store.RecordLifecycleJob(t.Context(), run, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginLifecycleJob(t.Context(), other); !errors.Is(err, owned.ErrLifecyclePending) {
		t.Fatal("lock released before cleanup", err)
	}
	if err = store.CleanLifecycleJob(t.Context(), run, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginLifecycleJob(t.Context(), other); err != nil {
		t.Fatal("verified cleanup retained lock", err)
	}
}
func TestLifecycleJobFailedAttemptRequiresCleanup(t *testing.T) {
	_, store, run := lifecycleFixture(t)
	state, err := store.BeginLifecycleJob(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.IssueLifecycleJob(t.Context(), run, 1); err != nil {
		t.Fatal(err)
	}
	receipt := state.Receipt
	receipt.JobUID = "job"
	receipt.SecretUID = "secret"
	receipt.State = "failed"
	if err = store.RecordLifecycleJob(t.Context(), run, receipt); err != nil {
		t.Fatal(err)
	}
	state, err = store.BeginLifecycleJob(t.Context(), run)
	if err != nil || state.Receipt.Attempt != 1 {
		t.Fatal(state, err)
	}
	if err = store.CleanLifecycleJob(t.Context(), run, receipt); err != nil {
		t.Fatal(err)
	}
	state, err = store.BeginLifecycleJob(t.Context(), run)
	if err != nil || state.Receipt.Attempt != 2 || state.Issued {
		t.Fatal(state, err)
	}
	if err = store.RecordLifecycleJob(t.Context(), run, receipt); err == nil {
		t.Fatal("late prior attempt replaced current cursor")
	}
}

type lifecycleReceiptBackend struct {
	creates, observes, cleanups int
	receipt                     *owned.LifecycleJobReceipt
	lost                        bool
}

func (b *lifecycleReceiptBackend) CreateLifecycleJob(_ context.Context, run owned.LifecycleJobRun, attempt int) (*owned.LifecycleJobReceipt, error) {
	b.creates++
	b.receipt = &owned.LifecycleJobReceipt{RunID: run.ID(), Digest: run.Digest(), Name: run.AttemptName(attempt), NamespaceUID: run.NamespaceUID, JobUID: "job", SecretUID: "secret", Attempt: attempt, State: "succeeded"}
	if b.lost {
		b.lost = false
		return nil, errors.New("response lost")
	}
	return b.receipt, nil
}
func (b *lifecycleReceiptBackend) ObserveLifecycleJob(context.Context, owned.LifecycleJobRun, int) (*owned.LifecycleJobReceipt, error) {
	b.observes++
	return b.receipt, nil
}
func (b *lifecycleReceiptBackend) CleanupLifecycleJob(context.Context, owned.LifecycleJobRun, owned.LifecycleJobReceipt) error {
	b.cleanups++
	return nil
}
func TestLifecycleJobLostResponseDoesNotRepeatCommand(t *testing.T) {
	_, store, run := lifecycleFixture(t)
	backend := &lifecycleReceiptBackend{lost: true}
	runner := &deployments.LifecycleJobs{Store: store, Backend: backend, Authorize: func(context.Context, owned.LifecycleJobRun) error { return nil }}
	if _, err := runner.RunLifecycleJob(t.Context(), run); err == nil {
		t.Fatal("lost response hidden")
	}
	if _, err := runner.RunLifecycleJob(t.Context(), run); !errors.Is(err, owned.ErrLifecyclePending) {
		t.Fatal(err)
	}
	result, err := runner.RunLifecycleJob(t.Context(), run)
	if err != nil || result.State != "succeeded" {
		t.Fatal(result, err)
	}
	if _, err = runner.RunLifecycleJob(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if backend.creates != 1 || backend.observes != 1 || backend.cleanups != 1 {
		t.Fatal("completed command repeated", backend)
	}
}
