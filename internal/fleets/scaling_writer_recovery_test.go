package fleets

import "testing"

func TestFleetScaleWriterApplyingUnknownNeverActivates(t *testing.T) {
	policy, applying, now := scalingWriterFixture()
	applying.Sequence = 3
	applying.Phase = "applying"
	applying.ProofKind = "apply_authorized"
	applying.ProofID = "apply-proof"
	unknown := applying
	unknown.Sequence = 4
	unknown.ProofKind = "effect_unknown"
	unknown.ProofID = "unknown-proof"
	if err := validateScalingWriterTransition(policy, &applying, unknown, now); err != nil {
		t.Fatal(err)
	}
	if unknown.ObservedWriter(policy.Input.Policy) != "unknown" || unknown.WriterUID != "" {
		t.Fatal("ambiguous apply claims a writer")
	}
	active := unknown
	active.Sequence = 5
	active.Phase = "active"
	active.WriterUID = "observed-manual-lease"
	active.ProofID = "observed-proof"
	for _, proof := range []string{"apply_authorized", "effect_unknown", "writers_fenced"} {
		active.ProofKind = proof
		if err := validateScalingWriterTransition(policy, &unknown, active, now); err == nil {
			t.Fatalf("%s activated an unknown writer", proof)
		}
	}
	active.ProofKind = "writer_observed"
	if err := validateScalingWriterTransition(policy, &unknown, active, now); err != nil {
		t.Fatal(err)
	}
	if active.ObservedWriter(policy.Input.Policy) != "manual" {
		t.Fatal("verified writer observation missing")
	}
}
