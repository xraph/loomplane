//go:build integration

package deployments

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
)

type cleanupDeliveryRecorder struct{ calls map[string]int }

func (r cleanupDeliveryRecorder) StartFailedCleanup(_ context.Context, _ string, id string, _ int) error {
	r.calls[id]++
	return nil
}

func TestFailedCleanupRecoveryAdvancesPastDeliveredPendingAttempts(t *testing.T) {
	pg := reapDatabase(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('cleanup-manifest','org_1','proj_1',1,'version: 1','digest','usr_1',now())`,
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) SELECT 'cleanup-plan-'||i,'org_1','proj_1','env_1','production','approved','cleanup-manifest',1,'usr_1',now() FROM generate_series(1,101) i`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	store := NewPgStore(pg)
	for i := 1; i <= 101; i++ {
		finished := time.Now()
		id := fmt.Sprintf("cleanup-deployment-%03d", i)
		_, err := store.CreateDeployment(ctx, &Deployment{ID: id, OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production", PlanID: fmt.Sprintf("cleanup-plan-%d", i), Status: StatusCancelled, StartedBy: "usr_1", StartedAt: time.Now(), FinishedAt: &finished})
		if err != nil {
			t.Fatal(err)
		}
		_, err = pg.Exec(ctx, `INSERT INTO loomplane_failed_cleanups(org_id,deployment_id,revision,actor,checkpoint,updated_at) VALUES('org_1',$1,'review','{}',$2,now()+$3*interval '1 second')`, id, []byte("{}"), i)
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := map[string]int{}
	service := NewFailedCleanups(nil, store, cleanupDeliveryRecorder{calls})
	// First 100 workflows remain pending while blocked on provider infrastructure.
	if err := service.RecoverFailedCleanups(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverFailedCleanups(ctx); err != nil {
		t.Fatal(err)
	}
	if calls["cleanup-deployment-101"] != 1 {
		t.Fatal("accepted cleanup after the first 100 was never delivered", len(calls))
	}
	for id, n := range calls {
		if n != 1 {
			t.Errorf("already delivered %s was repeatedly scanned: %d", id, n)
		}
	}
}

func TestSQLActionLockLossRefusesCheckpoint(t *testing.T) {
	pg := reapDatabase(t)
	r := NewRunner(RunnerDeps{Store: NewPgStore(pg)})
	err := r.withSQLActionLock(t.Context(), &Deployment{OrgID: "org_1", EnvironmentID: "env_1"}, planning.Action{Name: "database"}, func(ctx context.Context) error {
		if err := checkSQLActionLock(ctx); err != nil {
			return err
		}
		var killed bool
		err := pg.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND pid<>pg_backend_pid() LIMIT 1`).Scan(&killed)
		if err != nil {
			return err
		}
		if !killed {
			t.Fatal("did not terminate the owned SQL lock connection")
		}
		if err := checkSQLActionLock(ctx); err == nil {
			t.Fatal("lost SQL lock still authorized a checkpoint")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
