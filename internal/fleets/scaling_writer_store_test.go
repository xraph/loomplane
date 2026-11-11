package fleets

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

type scalingTestVerifier struct {
	calls  int
	err    error
	policy ScalingPolicyRecord
	event  ScalingWriterEvent
}

func (v *scalingTestVerifier) VerifyScalingWriterTransition(_ context.Context, _ driver.Tx, p ScalingPolicyRecord, e ScalingWriterEvent) error {
	v.calls++
	v.policy = p
	v.event = e
	return v.err
}

func scalingWriterStoreFixture(t *testing.T) (*scalingTestTx, ScalingPolicyRecord, ScalingWriterEvent) {
	t.Helper()
	p, e, _ := scalingWriterFixture()
	e.ObservedAt = time.Now().UTC()
	e.ExpiresAt = e.ObservedAt.Add(time.Minute)
	raw, err := json.Marshal(p.Input)
	if err != nil {
		t.Fatal(err)
	}
	return &scalingTestTx{rows: []scalingTestRow{
		{values: []any{"active"}},
		{values: []any{int64(2), int64(1), "environment", "provisioning"}},
		{values: []any{"environment", "operation", int64(2), int64(1), string(raw), e.ObservedAt, int64(1)}},
		{values: []any{"scale", "provisioning", int64(2)}},
		{err: pgx.ErrNoRows},
	}}, p, e
}

func TestFleetScaleWriterJournalRequiresVerifiedAuthority(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier *scalingTestVerifier
		status   string
		wantErr  bool
	}{
		{"no verifier", nil, "provisioning", true},
		{"revoked authority", &scalingTestVerifier{err: ErrApprovalRequired}, "provisioning", true},
		{"waiting approval", &scalingTestVerifier{}, "waiting_approval", true},
		{"waiting capacity", &scalingTestVerifier{}, "waiting_capacity", true},
		{"needs attention", &scalingTestVerifier{}, "needs_attention", true},
		{"cancelled", &scalingTestVerifier{}, "cancelled", true},
		{"verified", &scalingTestVerifier{}, "provisioning", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, p, e := scalingWriterStoreFixture(t)
			tx.rows[3].values[1] = tc.status
			var verifier ScalingWriterVerifier
			if tc.verifier != nil {
				verifier = tc.verifier
			}
			_, err := new(PgStore).AdvanceScalingWriterTx(t.Context(), tx, p.Scope, p.InstanceID, p.OperationID, p.Input.Service, 1, e, verifier)
			if (err != nil) != tc.wantErr {
				t.Fatalf("journal error: %v", err)
			}
			if tc.wantErr && len(tx.writes) > 0 {
				t.Fatal("unverified event persisted")
			}
			if !tc.wantErr && (len(tx.writes) != 1 || tc.verifier.calls != 1 || tc.verifier.policy.OperationID != p.OperationID || tc.verifier.event.Binding != e.Binding) {
				t.Fatal("journal bypassed scoped verifier")
			}
		})
	}
}

func TestFleetScaleWriterJournalReplayAndConflict(t *testing.T) {
	for _, alter := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact replay", true: "changed replay"}[alter], func(t *testing.T) {
			tx, p, e := scalingWriterStoreFixture(t)
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			tx.rows[4] = scalingTestRow{values: []any{int64(1), string(raw)}}
			if alter {
				e.ProofID = "substitute-proof"
			}
			v := &scalingTestVerifier{}
			_, err = new(PgStore).AdvanceScalingWriterTx(t.Context(), tx, p.Scope, p.InstanceID, p.OperationID, p.Input.Service, 1, e, v)
			if alter && !errors.Is(err, ErrConflict) || !alter && err != nil {
				t.Fatalf("replay: %v", err)
			}
			if len(tx.writes) != 0 || v.calls != 0 {
				t.Fatal("replay created a new transition")
			}
		})
	}
}

func TestFleetScaleWriterJournalStalePolicy(t *testing.T) {
	tx, p, e := scalingWriterStoreFixture(t)
	tx.rows[2].values[6] = int64(2)
	_, err := new(PgStore).AdvanceScalingWriterTx(t.Context(), tx, p.Scope, p.InstanceID, p.OperationID, p.Input.Service, 1, e, &scalingTestVerifier{})
	if !errors.Is(err, ErrConflict) || len(tx.writes) != 0 {
		t.Fatalf("superseded policy: %v", err)
	}
}
