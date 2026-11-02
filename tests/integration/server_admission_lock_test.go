//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// directPlanIn writes a direct plan for one service on runtime-1.
func directPlanIn(t *testing.T, f *poolFixture, id, env, name string) {
	t.Helper()
	f.plan(t, id, f.request(env, name), &cloudinfra.Placement{ConnectionID: "runtime-1"})
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(jsonb_set(jsonb_set(result,'{actions,0,service,cloud_placement}','null'),'{actions,0,connection_id}','"runtime-1"'),'{actions,0,service,target}','"runtime-1"') WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

// directPause is where pausedDirectApply holds a direct apply. Both are
// after admission. beforeEffects is the credential read, before the run's
// first organization-lock step (MarkWorkloadEffects). inExecutor is inside
// the executor's Apply, before the run records its binding.
type directPause string

const (
	beforeEffects directPause = "before effects"
	inExecutor    directPause = "in executor"
)

// pausedDirectApply runs the approved plan "direct" for one service on
// runtime-1 and holds it at pause. release lets it go on.
func pausedDirectApply(ctx context.Context, t *testing.T, f *poolFixture, name string, pause directPause) (done <-chan error, release func()) {
	const plan = "direct"
	t.Helper()
	executor := &blockingDirectPoolExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	accounts := &pausingAccounts{syncAccounts: syncAccounts{store: connections.NewPgStore(f.db)}, connection: "runtime-1", entered: make(chan struct{}), release: make(chan struct{})}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: accounts, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	dep, _, err := runner.Start(t.Context(), "org", plan)
	if err != nil || dep == "" {
		t.Fatalf("start %s: %q %v", plan, dep, err)
	}
	entered, held := executor.entered, executor.release
	if pause == beforeEffects {
		close(executor.release)
		entered, held = accounts.entered, accounts.release
		accounts.armed.Store(true)
	}
	out := make(chan error, 1)
	go func() {
		out <- runner.Run(ctx, "org", dep, "service:"+name, nil, func([]byte) error { return nil })
	}()
	select {
	case <-entered:
	case err = <-out:
		t.Fatalf("direct apply finished before its pause: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("direct apply never reached its pause %s", pause)
	}
	return out, func() { close(held) }
}

// frontendBinding records an unrelated Vercel web app's binding. PutBinding
// takes the organization lock only for a managed frontend, so the config
// says it is one.
func frontendBinding(ctx context.Context, f *poolFixture) error {
	raw, err := json.Marshal(planning.ServiceConfig{Provider: "vercel", Workload: provider.WebApp, Execution: provider.Serverless})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return f.plans.PutBinding(ctx, &planning.Binding{ID: "unrelated", OrgID: "org", ProjectID: "second", EnvironmentID: "second-env", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ConnectionID: "new", ExternalID: "web", Config: raw, CreatedAt: now, UpdatedAt: now})
}

// lockUser is a transaction that takes the organization lock and then the
// daemon's rows or locks, run while a direct apply on that daemon is paused.
type lockUser struct {
	name string
	run  func(ctx context.Context) error
}

// letThrough runs each user while the direct apply is paused and requires
// it to finish without waiting for the apply. It then releases the apply,
// which must finish too. A user that queues behind the apply would hang
// the run once it is released: the apply's next steps wait on the
// organization lock the user holds. Every wait is bounded, so the test
// reports a hang instead of becoming one.
func letThrough(t *testing.T, f *poolFixture, applyDone <-chan error, release func(), cancel context.CancelFunc, users ...lockUser) map[string]error {
	t.Helper()
	results := map[string]error{}
	var queued []string
	pending := map[string]<-chan error{}
	for _, user := range users {
		done := make(chan error, 1)
		go func(run func(context.Context) error) { done <- run(t.Context()) }(user.run)
		select {
		case err := <-done:
			results[user.name] = err
		case <-time.After(5 * time.Second):
			queued = append(queued, user.name)
			pending[user.name] = done
		}
	}
	report := ""
	if len(queued) > 0 {
		report = lockReport(f)
	}
	release()
	timeout := time.After(15 * time.Second)
	var applyErr error
	applyFinished := false
	for !applyFinished || len(pending) > 0 {
		select {
		case applyErr = <-applyDone:
			applyFinished = true
		case <-timeout:
			stuck := lockReport(f)
			cancel()
			t.Fatalf("deadlock: apply finished %v, still waiting on %v 15s after release\nwhile paused:\n%s\nafter release:\n%s", applyFinished, keys(pending), report, stuck)
		default:
			for name, done := range pending {
				select {
				case err := <-done:
					results[name] = err
					delete(pending, name)
				default:
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(queued) > 0 {
		t.Fatalf("%v waited on a paused direct apply\n%s", queued, report)
	}
	if applyErr != nil {
		t.Fatalf("direct apply: %v", applyErr)
	}
	return results
}

func keys(m map[string]<-chan error) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Approving another direct plan on the daemon takes the organization lock
// and then, in the plan's admission trigger, the daemon's connection row.
// It mustn't wait on a direct apply running there.
func TestServerAdmissionDirectApplyLetsApprovalThrough(t *testing.T) {
	for _, pause := range []directPause{beforeEffects, inExecutor} {
		t.Run(string(pause), func(t *testing.T) {
			f := newPoolFixture(t)
			if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members`); err != nil {
				t.Fatal(err)
			}
			directPlanIn(t, f, "direct", "env", "api")
			f.accept(t, "direct")
			directPlanIn(t, f, "other", "stage", "worker")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			applyDone, release := pausedDirectApply(ctx, t, f, "api", pause)
			results := letThrough(t, f, applyDone, release, cancel, lockUser{"plan approval", func(ctx context.Context) error {
				ok, err := f.plans.Decide(ctx, "org", "other", "approved", "usr", "", time.Now())
				if err == nil && !ok {
					err = errors.New("approval was refused")
				}
				return err
			}})
			if err := results["plan approval"]; err != nil {
				t.Fatalf("plan approval: %v", err)
			}
		})
	}
}

// Accepting a Cloud sync locks every participant connection and its daemon
// after the organization lock. A direct apply on one of those daemons
// mustn't hold it up. The direct plan was approved and started before the
// server joined the pool, as in TestGPUAdmissionSerializesPooledAfterDirect,
// and runs in an environment outside the Cloud, so the sync has no reason
// of its own to wait for it.
func TestServerAdmissionDirectApplyLetsCloudSyncThrough(t *testing.T) {
	// Only the executor pause: the server rejoins the pool during the pause,
	// so a run paused before its pool check would just be refused.
	for _, pause := range []directPause{inExecutor} {
		t.Run(string(pause), func(t *testing.T) {
			f, svc := sharedAcceptanceFixture(t)
			if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('side','org','proj','side','staging',now(),now())`); err != nil {
				t.Fatal(err)
			}
			p := reviewPoolChange(t, svc)
			var listed bool
			if err := f.sql.QueryRow(t.Context(), `SELECT snapshot->'connections' ? 'runtime-1' FROM loomplane_cloud_previews WHERE id=$1`, p.ID).Scan(&listed); err != nil || !listed {
				t.Fatalf("the sync must lock runtime-1: %v %v", listed, err)
			}
			var members []byte
			if err := f.sql.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(m)) FROM loomplane_server_pool_members m WHERE server_id='server-1'`).Scan(&members); err != nil {
				t.Fatal(err)
			}
			if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members WHERE server_id='server-1'`); err != nil {
				t.Fatal(err)
			}
			directPlanIn(t, f, "direct", "side", "worker")
			f.accept(t, "direct")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			applyDone, release := pausedDirectApply(ctx, t, f, "worker", pause)
			if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_server_pool_members SELECT * FROM jsonb_populate_recordset(NULL::loomplane_server_pool_members,$1::jsonb)`, members); err != nil {
				t.Fatal(err)
			}
			results := letThrough(t, f, applyDone, release, cancel, lockUser{"cloud sync", func(ctx context.Context) error {
				_, err := svc.ApplyCloudChange(ctx, security.Principal{OrgID: "org", UserID: "usr"}, "", p.ID, p.Revision, "beside-direct")
				return err
			}})
			if err := results["cloud sync"]; err != nil {
				t.Fatalf("cloud sync: %v", err)
			}
		})
	}
}

// A pool save that enrolls the daemon takes the organization lock first.
// While a direct apply runs there it must refuse at once rather than wait
// under that lock. Otherwise every frontend binding in the organization
// queues behind it, and the paused apply's own MarkWorkloadEffects (in the
// before-effects case) waits on it while it waits on the apply.
func TestServerAdmissionDirectApplyLetsBindingsPastPoolSave(t *testing.T) {
	for _, pause := range []directPause{beforeEffects, inExecutor} {
		t.Run(string(pause), func(t *testing.T) {
			f := newPoolFixture(t)
			if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members`); err != nil {
				t.Fatal(err)
			}
			directPlanIn(t, f, "direct", "env", "api")
			f.accept(t, "direct")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			applyDone, release := pausedDirectApply(ctx, t, f, "api", pause)
			next := *f.pool
			next.Revision++
			results := letThrough(t, f, applyDone, release, cancel,
				lockUser{"pool save", func(ctx context.Context) error { return f.store.SavePool(ctx, &next, 1) }},
				lockUser{"binding", func(ctx context.Context) error { return frontendBinding(ctx, f) }},
			)
			if err := results["pool save"]; !errors.Is(err, cloudinfra.ErrBusy) {
				t.Fatalf("pool save beside a direct apply: %v", err)
			}
			if err := results["binding"]; err != nil {
				t.Fatalf("binding: %v", err)
			}
			var members int
			if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_server_pool_members`).Scan(&members); err != nil || members != 0 {
				t.Fatalf("refused pool save enrolled %d members: %v", members, err)
			}
		})
	}
}

// The organization lock bounds its own wait and every later lock wait in
// its transaction, so a cycle through it fails instead of hanging.
func TestOrganizationLockBoundsLockWaits(t *testing.T) {
	_, _, db := cloudSyncDatabase(t)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = cloudprofiles.LockOrganization(t.Context(), tx, "org"); err != nil {
		t.Fatal(err)
	}
	var limit string
	if err = tx.QueryRow(t.Context(), `SELECT current_setting('lock_timeout')`).Scan(&limit); err != nil || limit != "1min" {
		t.Fatalf("lock_timeout after the organization lock: %q %v", limit, err)
	}
}

// Retiring a server waits for a direct apply on its daemon, as an admitted
// deployment always kept retirement out. It waits on the daemon's
// direct-apply lock before it takes the organization lock, so the apply's
// own organization-lock steps still get through, and once the apply is done
// the retirement sees the plan's connection use and refuses.
func TestServerAdmissionRetirementWaitsOutsideOrganizationLock(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members`); err != nil {
		t.Fatal(err)
	}
	directPlanIn(t, f, "direct", "env", "api")
	f.accept(t, "direct")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	applyDone, release := pausedDirectApply(ctx, t, f, "api", inExecutor)
	retired := make(chan error, 1)
	go func() {
		retired <- f.store.RetireServer(ctx, &cloudinfra.Server{ID: "server-1", OrgID: "org", CloudID: "cloud", RuntimeConnectionID: "runtime-1", Status: "ready"})
	}()
	select {
	case err := <-retired:
		release()
		t.Fatalf("retirement ran beside a direct apply: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	waitPoolSQL(t, f, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory' AND query LIKE '%direct-apply%')`)
	// The wait can last as long as a deploy, so it holds no table lock that
	// would block DDL behind it.
	var tables []string
	if err := f.sql.QueryRow(t.Context(), `SELECT coalesce(array_agg(DISTINCT l.relation::regclass::text),'{}') FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE a.datname=current_database() AND a.wait_event='advisory' AND a.query LIKE '%direct-apply%' AND l.locktype='relation' AND l.granted`).Scan(&tables); err != nil || len(tables) > 0 {
		release()
		t.Fatalf("retirement waits holding table locks %v: %v", tables, err)
	}
	results := letThrough(t, f, applyDone, release, cancel, lockUser{"binding", func(ctx context.Context) error { return frontendBinding(ctx, f) }})
	if err := results["binding"]; err != nil {
		t.Fatalf("binding beside a queued retirement: %v", err)
	}
	select {
	case err := <-retired:
		if !errors.Is(err, cloudinfra.ErrConflict) {
			t.Fatalf("retirement after the direct apply: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("retirement never finished\n%s", lockReport(f))
	}
}

// Nothing locks the connection row through a direct apply, so the
// connection can be re-verified against another daemon after admission.
// The run then refuses the credential instead of deploying through a
// daemon it never admitted. The action names no connection of its own and
// deploys through the plan's server connection, so the reviewed target
// settings check, which only covers an action's own connection, can't
// catch the move.
func TestServerAdmissionRefusesConnectionMovedAfterAdmission(t *testing.T) {
	f := newPoolFixture(t)
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_server_pool_members`); err != nil {
		t.Fatal(err)
	}
	directPlanIn(t, f, "direct", "env", "api")
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(jsonb_set(result,'{actions,0,connection_id}','""'),'{connections}','{"server":"runtime-1"}') WHERE id='direct'`); err != nil {
		t.Fatal(err)
	}
	f.accept(t, "direct")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	applyDone, release := pausedDirectApply(ctx, t, f, "api", beforeEffects)
	if _, err := f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET details=jsonb_set(details,'{docker_id}','"elsewhere"') WHERE id='runtime-1'`); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	select {
	case err := <-applyDone:
		if err == nil || !strings.Contains(err.Error(), "now reaches a different server than the one it was admitted on") {
			t.Fatalf("direct apply through a moved connection: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("direct apply never finished\n%s", lockReport(f))
	}
	var bound bool
	if err := f.sql.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM loomplane_bindings WHERE environment_id='env' AND name='api')`).Scan(&bound); err != nil || bound {
		t.Fatalf("a refused apply left a binding: %v %v", bound, err)
	}
}
