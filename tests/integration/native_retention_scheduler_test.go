//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

type nativePolicyService interface {
	SetNativeRetentionPolicy(context.Context, security.Principal, string, recovery.RetentionPolicyInput) (*recovery.NativeRetentionPolicy, error)
	ReconcileNativeRetentionPolicies(context.Context) error
}

func TestNativeRetentionPolicyServiceSchedulesOnlyAuthorizedOptIn(t *testing.T) {
	f, retirements, request := nativeRetirementFixture(t)
	inventory, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	backend := &nativeRetirementBackend{points: inventory.Points}
	enqueued := 0
	service := &recovery.NativeService{Store: f.store, Backend: backend, Authz: nativeRetirementAccess{}, Projects: projects.NewPgStore(f.db), Retirements: retirements, DeletionBackend: backend, EnqueueDeletion: func(context.Context, string, string) error { enqueued++; return nil }}
	policies, ok := any(service).(nativePolicyService)
	if !ok {
		t.Fatal("native retention has no policy service or scheduler")
	}
	p := security.Principal{OrgID: "org", UserID: "operator"}
	policy, err := policies.SetNativeRetentionPolicy(t.Context(), p, f.source.ID, recovery.RetentionPolicyInput{Policy: request.Policy, Enabled: true})
	if err != nil || enqueued != 0 {
		t.Fatal("saving a policy admitted deletion outside reconciliation", policy, enqueued, err)
	}
	if err = policies.ReconcileNativeRetentionPolicies(t.Context()); err != nil {
		t.Fatal(err)
	}
	accepted, err := retirements.NativeRetirementByRequest(t.Context(), "org", f.source.ID, nativePolicyRequestKeyForTest(policy))
	if err != nil || accepted.PolicyID != policy.ID || accepted.PolicyRevision != policy.Revision || enqueued != 1 {
		t.Fatal("native scheduler lost its due-time admission", accepted, enqueued, err)
	}
	if _, err = policies.SetNativeRetentionPolicy(t.Context(), p, f.source.ID, recovery.RetentionPolicyInput{Revision: policy.Revision, Policy: request.Policy, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err = service.RunNativeRetirement(t.Context(), "org", accepted.ID); err != nil {
		t.Fatal("pause prevented accepted native work from completing", err)
	}
	if err = policies.ReconcileNativeRetentionPolicies(t.Context()); err != nil || enqueued != 1 || backend.writes != 1 {
		t.Fatal("paused native scheduler admitted new work", enqueued, backend.writes, err)
	}
}

type nativePolicyAuthority struct{ retire bool }

func (a *nativePolicyAuthority) Require(ctx context.Context, p security.Principal, permission security.Permission, project string) error {
	if permission == security.BackupRetire && !a.retire {
		return security.ErrForbidden
	}
	return (nativeRetirementAccess{}).Require(ctx, p, permission, project)
}
func TestNativeRetentionPolicyRevokedOwnerBlocksAdmissionAndPauseNeedsNoCloud(t *testing.T) {
	f, retirements, request := nativeRetirementFixture(t)
	inventory, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	backend := &nativeRetirementBackend{points: inventory.Points}
	authority := &nativePolicyAuthority{retire: true}
	enqueued := 0
	service := &recovery.NativeService{Store: f.store, Backend: backend, Authz: authority, Projects: projects.NewPgStore(f.db), Retirements: retirements, DeletionBackend: backend, EnqueueDeletion: func(context.Context, string, string) error { enqueued++; return nil }}
	p := security.Principal{OrgID: "org", UserID: "operator"}
	policy, err := service.SetNativeRetentionPolicy(t.Context(), p, f.source.ID, recovery.RetentionPolicyInput{Policy: request.Policy, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	authority.retire = false
	if err = service.ReconcileNativeRetentionPolicies(t.Context()); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("scheduled native retention lost current owner authority", err)
	}
	saved, err := service.NativeRetentionPolicy(t.Context(), p, f.source.ID)
	if err != nil || saved.LastError == "" || saved.NextAt.Before(time.Now().Add(50*time.Minute)) || enqueued != 0 || backend.writes != 0 {
		t.Fatal("revoked policy created deletion or hid its failure", saved, enqueued, backend.writes, err)
	}
	authority.retire = true
	service.Backend = nil
	if _, err = service.SetNativeRetentionPolicy(t.Context(), p, f.source.ID, recovery.RetentionPolicyInput{Revision: policy.Revision, Policy: request.Policy, Enabled: false}); err != nil {
		t.Fatal("native policy could not pause without cloud access", err)
	}
	history, err := service.NativeRetentionPolicyHistory(t.Context(), p, f.source.ID, recovery.RetentionPolicyHistoryInput{Limit: 1})
	if err != nil || len(history.Events) != 1 || history.Events[0].Enabled || history.NextBefore != "2" {
		t.Fatal("native policy history lost the paused revision", history, err)
	}
	older, err := service.NativeRetentionPolicyHistory(t.Context(), p, f.source.ID, recovery.RetentionPolicyHistoryInput{Before: history.NextBefore, Limit: 1})
	if err != nil || len(older.Events) != 1 || !older.Events[0].Enabled || older.NextBefore != "" {
		t.Fatal("native policy cursor lost original authority", older, err)
	}
	if _, err = service.NativeRetentionPolicyHistory(t.Context(), security.Principal{OrgID: "foreign", UserID: "operator"}, f.source.ID, recovery.RetentionPolicyHistoryInput{}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("native policy history crossed organization", err)
	}
}
