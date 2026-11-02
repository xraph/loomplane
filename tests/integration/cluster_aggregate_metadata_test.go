//go:build integration

package integration

import (
	"testing"

	"github.com/xraph/grove/driver"
)

func TestClusterWorkloadMetadataRejectsCorruptReservation(t *testing.T) {
	f, _, in, _ := clusterWorkloadFixture(t)
	reserve := func(tx driver.Tx) error {
		_, err := f.store.ReserveClusterWorkload(t.Context(), tx, in, clusterWorkloadGuard)
		return err
	}
	if err := workloadTx(t, f, reserve); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{`2`, `null`, `"1"`, `missing`} {
		t.Run(version, func(t *testing.T) {
			if _, err := f.sql.Exec(t.Context(), `ALTER TABLE loomplane_cluster_workload_holds DISABLE TRIGGER cluster_hold_metadata`); err != nil {
				t.Fatal(err)
			}
			if version == "missing" {
				if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_workload_holds SET body=body-'schema_version'`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_workload_holds SET body=jsonb_set(body,'{schema_version}',$1::jsonb)`, version); err != nil {
				t.Fatal(err)
			}
			if _, err := f.sql.Exec(t.Context(), `ALTER TABLE loomplane_cluster_workload_holds ENABLE TRIGGER cluster_hold_metadata`); err != nil {
				t.Fatal(err)
			}
			if err := workloadTx(t, f, reserve); err == nil {
				t.Fatal("idempotent reservation accepted invalid metadata")
			}
			if err := workloadTx(t, f, func(tx driver.Tx) error { _, _, err := f.store.ClusterWorkloadUsage(t.Context(), tx, in); return err }); err == nil {
				t.Fatal("accounting accepted invalid metadata")
			}
			if err := workloadTx(t, f, func(tx driver.Tx) error {
				return f.store.ReleaseUnstartedClusterWorkload(t.Context(), tx, in, clusterWorkloadGuard)
			}); err == nil {
				t.Fatal("release accepted invalid metadata")
			}
			if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_workload_holds SET body=body-'schema_version'`); err == nil {
				t.Fatal("legacy writer overwrote unsupported stored metadata")
			}
		})
	}
}
