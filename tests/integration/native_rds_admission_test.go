//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestNativeRDSTargetPinsPrivateRouteAndRejectsConnectionChanges(t *testing.T) {
	f, store, request, _ := nativeAdmissionFixture(t)
	route := manageddb.RDSRecoveryNetwork{VPCID: "vpc-recovery", SubnetGroup: "recovery-private", SecurityGroupID: "sg-copy", VerifierGroupID: "sg-worker"}
	settings := route.Settings()
	settings["region"] = "us-east-1"
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET cloud='aws',account='123456789012',details=$1 WHERE id='native-target-account'`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_targets SET provider='rds',region='us-east-1',revision=revision+1 WHERE id='native-target'`); err != nil {
		t.Fatal(err)
	}
	target, err := store.ReviewNativeTarget(t.Context(), "org", "project", "native-cloud", "native-target")
	if err != nil || target.NativeAccount != "123456789012" || target.RDSNetwork != route {
		t.Fatal("granted RDS target has no private reviewed route", target, err)
	}
	request.Target = target
	if err = store.CheckNativeTarget(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	settings["rds_recovery_verifier_group"] = "sg-replaced-worker"
	raw, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET details=$1 WHERE id='native-target-account'`, raw); err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNativeTarget(t.Context(), request); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("changed worker route retained copy authority", err)
	}
	delete(settings, "rds_recovery_verifier_group")
	raw, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET details=$1 WHERE id='native-target-account'`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReviewNativeTarget(t.Context(), "org", "project", "native-cloud", "native-target"); !errors.Is(err, recovery.ErrInvalid) {
		t.Fatal("partial RDS route admitted", err)
	}
}
