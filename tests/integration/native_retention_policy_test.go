//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recovery"
)

func TestNativeRetentionPolicyPauseFencesAdmissionAndKeepsAcceptedWork(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	policies, ok := any(f.store).(recovery.NativeRetentionPolicyStore)
	if !ok {
		t.Fatal("native retention has no persisted opt-in policy")
	}
	guard := func(context.Context) error { return nil }
	in := &recovery.NativeRetentionPolicy{ID: "native-policy", OrgID: "org", ProjectID: "project", SourceID: f.source.ID, Policy: request.Policy, AuthorizedBy: "operator"}
	policy, err := policies.PutNativeRetentionPolicy(t.Context(), in, 0, guard)
	if err != nil || policy.Enabled || policy.Revision != 1 {
		t.Fatal("native retention did not default to paused", policy, err)
	}
	due, err := policies.DueNativeRetentionPolicies(t.Context())
	if err != nil || len(due) != 0 {
		t.Fatal("paused native policy was scheduled", due, err)
	}
	in.Enabled = true
	enabled, err := policies.PutNativeRetentionPolicy(t.Context(), in, policy.Revision, guard)
	if err != nil || enabled.Revision != 2 {
		t.Fatal("native policy enable failed", enabled, err)
	}
	if _, err = policies.PutNativeRetentionPolicy(t.Context(), in, policy.Revision, guard); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("stale native policy revision overwrote current decision", err)
	}
	due, err = policies.DueNativeRetentionPolicies(t.Context())
	if err != nil || len(due) != 1 {
		t.Fatal("enabled native policy was not due", due, err)
	}
	request.PolicyID, request.PolicyRevision, request.ExpectedPolicyAt = enabled.ID, enabled.Revision, enabled.NextAt
	request.RequestID = nativePolicyRequestKeyForTest(enabled)
	in.Enabled = false
	paused, err := policies.PutNativeRetentionPolicy(t.Context(), in, enabled.Revision, guard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AdmitNativeRetirement(t.Context(), request, guard); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("paused native policy admitted a stale selection", err)
	}
	in.Enabled = true
	current, err := policies.PutNativeRetentionPolicy(t.Context(), in, paused.Revision, guard)
	if err != nil {
		t.Fatal(err)
	}
	request.PolicyRevision, request.ExpectedPolicyAt = current.Revision, current.NextAt
	request.RequestID = nativePolicyRequestKeyForTest(current)
	accepted, err := store.AdmitNativeRetirement(t.Context(), request, guard)
	if err != nil {
		t.Fatal(err)
	}
	in.Enabled = false
	if _, err = policies.PutNativeRetentionPolicy(t.Context(), in, current.Revision, guard); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNativeRetirement(t.Context(), "org", accepted.ID, "accepted-before-pause")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNativeRetirement(t.Context(), claimed); err != nil {
		t.Fatal("pause revoked already-accepted deletion", err)
	}
	if _, err = policies.NativeRetentionPolicyForSource(t.Context(), "other-org", f.source.ID); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("native policy crossed organization", err)
	}
}

func TestNativeRetentionPolicyAdvanceLinksCommittedRequestAfterLostResponse(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	policies, ok := any(f.store).(recovery.NativeRetentionPolicyStore)
	if !ok {
		t.Fatal("native retention has no policy reconciliation store")
	}
	guard := func(context.Context) error { return nil }
	policy, err := policies.PutNativeRetentionPolicy(t.Context(), &recovery.NativeRetentionPolicy{ID: "lost-policy", OrgID: "org", ProjectID: "project", SourceID: f.source.ID, Policy: request.Policy, AuthorizedBy: "operator", Enabled: true}, 0, guard)
	if err != nil {
		t.Fatal(err)
	}
	request.PolicyID, request.PolicyRevision, request.ExpectedPolicyAt = policy.ID, policy.Revision, policy.NextAt
	// Admission and advancement must share one deterministic due-time key.
	request.RequestID = nativePolicyRequestKeyForTest(policy)
	accepted, err := store.AdmitNativeRetirement(t.Context(), request, guard)
	if err != nil {
		t.Fatal(err)
	}
	if err = policies.AdvanceNativeRetentionPolicy(t.Context(), policy, "", nil); err != nil {
		t.Fatal(err)
	}
	saved, err := policies.NativeRetentionPolicyForSource(t.Context(), "org", f.source.ID)
	if err != nil || saved.LastRetirementID != accepted.ID || !saved.NextAt.After(policy.NextAt) {
		t.Fatal("lost admission response was not linked before advancing", saved, err)
	}
	oldDue := saved.NextAt
	if err = policies.AdvanceNativeRetentionPolicy(t.Context(), policy, "", nil); err != nil {
		t.Fatal(err)
	}
	saved, err = policies.NativeRetentionPolicyForSource(t.Context(), "org", f.source.ID)
	if err != nil || !saved.NextAt.Equal(oldDue) || saved.NextAt.Before(time.Now()) {
		t.Fatal("stale scheduler advanced a newer evaluation", saved, err)
	}
}

func nativePolicyRequestKeyForTest(p *recovery.NativeRetentionPolicy) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("native-retention-v1\x00%s\x00%s\x00%s\x00%d\x00%d", p.OrgID, p.SourceID, p.ID, p.Revision, p.NextAt.UnixMicro())))
	return "native_retention_" + hex.EncodeToString(sum[:])
}
