package fleets

import (
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/fleet"
)

func scalingWriterFixture() (ScalingPolicyRecord, ScalingWriterEvent, time.Time) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	p := ScalingPolicyRecord{Scope: FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}, InstanceID: "instance", EnvironmentID: "environment", OperationID: "operation", Generation: 2, OwnershipEpoch: 1, Input: fleet.ScaleInput{Service: "api", Policy: fleet.ScalingPolicy{Version: 1, Revision: 1, Mode: "manual", MinReplicas: 1, MaxReplicas: 1}}}
	e := ScalingWriterEvent{Sequence: 1, Phase: "revoking", ProofKind: "revoke_authorized", ProofID: "proof-1", ObservedAt: now, ExpiresAt: now.Add(time.Minute), DeploymentGeneration: 4, DeploymentResourceVersion: "version-4", Binding: ScalingWriterBinding{AuthorityID: "authority", AuthorityRevision: 1, ConnectionID: "connection", AttachmentID: "attachment", AttachmentRevision: 1, TrustRevision: 1, Namespace: "namespace", NamespaceUID: "namespace-uid", Deployment: "deployment", DeploymentUID: "deployment-uid", RuntimeRef: "runtime", RuntimeRevision: 1, OldWriter: "hpa", OldWriterUID: "old-hpa-uid"}}
	return p, e, now
}

func TestFleetScaleManualRevokesHPAJournal(t *testing.T) {
	p, event, now := scalingWriterFixture()
	var previous *ScalingWriterEvent
	for index, step := range []struct{ phase, proof, writer string }{{"revoking", "revoke_authorized", "unknown"}, {"quiesced", "writers_fenced", "none"}, {"applying", "apply_authorized", "unknown"}, {"active", "writer_observed", "manual"}} {
		event.Sequence = int64(index + 1)
		event.Phase = step.phase
		event.ProofKind = step.proof
		event.ProofID = step.proof
		if step.phase == "active" {
			event.WriterUID = "manual-lease"
		}
		if err := validateScalingWriterTransition(p, previous, event, now); err != nil {
			t.Fatalf("%s: %v", step.phase, err)
		}
		if got := event.ObservedWriter(p.Input.Policy); got != step.writer {
			t.Fatalf("phase %s claims writer %s", step.phase, got)
		}
		copyEvent := event
		previous = &copyEvent
	}
}

func TestFleetScaleWriterRejectsUnsafeTransition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ScalingWriterEvent, *ScalingWriterEvent)
	}{
		{"skip fencing", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) {
			n.Phase = "active"
			n.ProofKind = "writer_observed"
			n.WriterUID = "manual"
		}},
		{"intent is not fencing", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.ProofKind = "revoke_authorized" }},
		{"missing HPA is not fencing", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.ProofKind = "hpa_not_found" }},
		{"display metrics are not fencing", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.ProofKind = "metrics_observed" }},
		{"replacement namespace", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.Binding.NamespaceUID = "replacement" }},
		{"replacement deployment", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.Binding.DeploymentUID = "replacement" }},
		{"substitute authority", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.Binding.AuthorityID = "substitute" }},
		{"lost CAS", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.Sequence = 3 }},
		{"expired proof", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.ExpiresAt = n.ObservedAt }},
		{"stale proof", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.ObservedAt = n.ObservedAt.Add(-2 * time.Minute) }},
		{"future proof", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.ObservedAt = n.ObservedAt.Add(time.Second) }},
		{"old controller generation", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.DeploymentGeneration = 3 }},
		{"missing resource version", func(_ *ScalingWriterEvent, n *ScalingWriterEvent) { n.DeploymentResourceVersion = "" }},
		{"reused proof", func(p *ScalingWriterEvent, n *ScalingWriterEvent) { n.ProofID = p.ProofID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, previous, now := scalingWriterFixture()
			next := previous
			next.Sequence = 2
			next.Phase = "quiesced"
			next.ProofKind = "writers_fenced"
			next.ProofID = "proof-2"
			tc.change(&previous, &next)
			if err := validateScalingWriterTransition(p, &previous, next, now); err == nil {
				t.Fatal("unsafe transition accepted")
			}
		})
	}
}

func TestFleetScaleWriterUnknownRecovery(t *testing.T) {
	p, previous, now := scalingWriterFixture()
	unknown := previous
	unknown.Sequence = 2
	unknown.ProofID = "unknown-result"
	unknown.ProofKind = "effect_unknown"
	if err := validateScalingWriterTransition(p, &previous, unknown, now); err != nil {
		t.Fatal(err)
	}
	if unknown.ObservedWriter(p.Input.Policy) != "unknown" {
		t.Fatal("ambiguous revoke granted a writer")
	}
	recovered := unknown
	recovered.Sequence = 3
	recovered.Phase = "quiesced"
	recovered.ProofID = "observed-fence"
	recovered.ProofKind = "writers_fenced"
	if err := validateScalingWriterTransition(p, &unknown, recovered, now); err != nil {
		t.Fatal(err)
	}
}
