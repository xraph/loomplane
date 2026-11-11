package fleets

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/pkg/fleet"
)

type scalingTestRow struct {
	values []any
	err    error
}

func (r scalingTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("unexpected scaling scan shape")
	}
	for i := range dest {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(r.values[i]))
	}
	return nil
}

type scalingTestTx struct {
	driver.Tx
	rows    []scalingTestRow
	queries []string
	writes  [][]any
}

func (tx *scalingTestTx) QueryRow(_ context.Context, q string, _ ...any) driver.Row {
	tx.queries = append(tx.queries, q)
	if len(tx.rows) == 0 {
		return scalingTestRow{err: errors.New("unexpected scaling query")}
	}
	r := tx.rows[0]
	tx.rows = tx.rows[1:]
	return r
}
func (tx *scalingTestTx) Exec(_ context.Context, _ string, args ...any) (driver.Result, error) {
	tx.writes = append(tx.writes, args)
	return nil, nil
}
func scalingReservationFixture() (*scalingTestTx, FleetScope, fleet.ScaleInput) {
	return &scalingTestTx{rows: []scalingTestRow{
		{values: []any{"active"}},
		{values: []any{int64(2), int64(1), "environment", "ready"}},
		{values: []any{"scale", "waiting_approval", int64(2)}},
		{err: pgx.ErrNoRows},
	}}, FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}, fleet.ScaleInput{Service: "api", Policy: fleet.ScalingPolicy{Version: 1, Revision: 1, Mode: "manual", MinReplicas: 1, MaxReplicas: 1}}
}
func TestFleetScalePolicyReservation(t *testing.T) {
	tx, scope, in := scalingReservationFixture()
	if err := new(PgStore).ReserveScalingPolicyTx(t.Context(), tx, scope, "instance", "operation", 2, 1, in); err != nil {
		t.Fatal(err)
	}
	if len(tx.writes) != 1 {
		t.Fatalf("policy insert count: %d", len(tx.writes))
	}
	if !strings.Contains(tx.queries[0], "loomplane_fleets") || !strings.Contains(tx.queries[1], "loomplane_fleet_instances") || !strings.Contains(tx.queries[3], "FOR UPDATE") {
		t.Fatal("policy reservation must lock parent, instance, then latest revision")
	}
}
func TestFleetScalePolicyReservationRejectsStaleIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*scalingTestTx, *fleet.ScaleInput)
	}{
		{"foreign scope", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[0] = scalingTestRow{err: pgx.ErrNoRows} }},
		{"paused fleet", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[0].values[0] = "paused" }},
		{"old generation", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[1].values[0] = int64(3) }},
		{"old epoch", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[1].values[1] = int64(2) }},
		{"retired instance", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[1].values[3] = "retired" }},
		{"wrong operation kind", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[2].values[0] = "update" }},
		{"cancelled operation", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[2].values[1] = "cancelled" }},
		{"wrong operation generation", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[2].values[2] = int64(1) }},
		{"lost policy CAS", func(tx *scalingTestTx, _ *fleet.ScaleInput) { tx.rows[3] = scalingTestRow{values: []any{int64(1)}} }},
		{"invalid policy", func(_ *scalingTestTx, in *fleet.ScaleInput) { in.Policy.MaxReplicas = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, scope, in := scalingReservationFixture()
			tc.change(tx, &in)
			if err := new(PgStore).ReserveScalingPolicyTx(t.Context(), tx, scope, "instance", "operation", 2, 1, in); err == nil {
				t.Fatal("invalid reservation accepted")
			}
			if len(tx.writes) != 0 {
				t.Fatal("rejected request inserted a policy")
			}
		})
	}
}
