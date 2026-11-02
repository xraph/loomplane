//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func TestNativeRetirementAdmissionRejectsUnsafeSelectionAndAuthority(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	for _, scenario := range []string{"missing-proof", "future-proof", "partial", "stale", "witness-missing", "copy-in-use", "denied", "wrong-org"} {
		t.Run(scenario, func(t *testing.T) {
			t.Cleanup(func() {
				for _, q := range []string{
					`UPDATE loomplane_native_restores SET verified_at=now()-interval '1 hour' WHERE id='witness-copy'`,
					`UPDATE loomplane_native_sources SET complete=true,observed_at=now(),state='ready'`,
					`UPDATE loomplane_native_points SET present=true`,
					`DELETE FROM loomplane_native_restores WHERE id='active-copy'`,
				} {
					if _, err := f.sql.Exec(context.Background(), q); err != nil {
						t.Error(err)
					}
				}
			})
			query := ""
			switch scenario {
			case "missing-proof":
				query = `UPDATE loomplane_native_restores SET verified_at=NULL WHERE id='witness-copy'`
			case "future-proof":
				query = `UPDATE loomplane_native_restores SET verified_at=now()+interval '1 hour' WHERE id='witness-copy'`
			case "partial":
				query = `UPDATE loomplane_native_sources SET complete=false`
			case "stale":
				query = `UPDATE loomplane_native_sources SET observed_at=now()-interval '10 minutes'`
			case "witness-missing":
				query = `UPDATE loomplane_native_points SET present=false WHERE body->>'name'='witness'`
			case "copy-in-use":
				query = `INSERT INTO loomplane_native_restores(id,org_id,project_id,environment_id,source_id,point_id,request_id,source,point,target,status,created_by,created_at,expires_at)
 SELECT 'active-copy',r.org_id,r.project_id,r.environment_id,r.source_id,p.id,'active-copy-request',r.source,p.body||jsonb_build_object('id',p.id,'source_id',p.source_id),jsonb_build_object('connection_id','account'),'queued','operator',now(),now()+interval '1 hour' FROM loomplane_native_restores r JOIN loomplane_native_points p ON p.source_id=r.source_id AND p.body->>'name'='old' WHERE r.id='witness-copy'`
			}
			if query != "" {
				if _, err := f.sql.Exec(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			guard := func(context.Context) error { return nil }
			in := *request
			if scenario == "denied" {
				guard = func(context.Context) error { return security.ErrForbidden }
			}
			if scenario == "wrong-org" {
				in.OrgID = "other-org"
			}
			if _, err := store.AdmitNativeRetirement(t.Context(), &in, guard); err == nil {
				t.Fatal("unsafe native deletion admitted", scenario)
			}
			var count int
			if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_native_retirements`).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected native deletion left an intent", count, err)
			}
		})
	}
}

func TestNativeRetirementFailureKeepsBarrierAndOriginalActor(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	guard := func(context.Context) error { return nil }
	r, err := store.AdmitNativeRetirement(t.Context(), request, guard)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNativeRetirement(t.Context(), "org", r.ID, "worker-one")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveNativeRetirementIntent(t.Context(), claimed, true); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishNativeRetirement(t.Context(), claimed, recovery.ErrNativeUnavailable); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RetryNativeRetirement(t.Context(), "org", r.ID, "other-operator", guard); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("retry reassigned the native deletion owner", err)
	}
	resumed, err := store.RetryNativeRetirement(t.Context(), "org", r.ID, "operator", guard)
	if err != nil || !resumed.Attempted || resumed.ID != r.ID {
		t.Fatal("retry lost original native intent", resumed, err)
	}
	// A changed captured source must not pass the lease's original-scope check.
	next, err := store.ClaimNativeRetirement(t.Context(), "org", r.ID, "worker-two")
	if err != nil {
		t.Fatal(err)
	}
	wrong := *next
	wrong.Source.PhysicalID = "db-replacement"
	if err = store.CheckNativeRetirement(t.Context(), &wrong); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("native deletion adopted a replacement source", err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET verified_at=NULL WHERE id='witness-copy'`); err != nil {
		t.Fatal(err)
	}
	if err = store.CheckNativeRetirement(t.Context(), next); !errors.Is(err, recovery.ErrConflict) {
		t.Fatal("lost witness SQL proof still authorized deletion", err)
	}
}

func TestNativeRetirementOldAbsentHistoryDoesNotBlockCurrentRetention(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	_, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_native_points(org_id,source_id,provider_id,created_at,body,present,last_seen_at)
 SELECT p.org_id,p.source_id,'arn:aws:rds:us-east-1:123456789012:snapshot:gone-'||n::text,p.created_at,
 p.body||jsonb_build_object('provider_id','arn:aws:rds:us-east-1:123456789012:snapshot:gone-'||n::text,'name','gone-'||n::text),false,p.last_seen_at
 FROM loomplane_native_points p CROSS JOIN generate_series(1,1001) n WHERE p.id=$1`, request.PointID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AdmitNativeRetirement(t.Context(), request, func(context.Context) error { return nil }); err != nil {
		t.Fatal("absent native history exhausted the live retention bound", err)
	}
}
