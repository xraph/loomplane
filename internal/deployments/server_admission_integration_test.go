//go:build integration

package deployments

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
)

// The connection id has to look like a real one, or the plan's connection
// uses never name it and the retiring-server trigger never runs.
const admissionConnection = "conn_01h00000000000000000000000"

// admissionTemplate migrates one database that each subtest copies, and
// returns the admin URL and the template's name.
func admissionTemplate(t *testing.T) (string, string) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the PostgreSQL server admission tests")
	}
	name := fmt.Sprintf("server_admission_%d", time.Now().UnixNano())
	admin := admissionAdmin(t, dsn)
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})
	if err := ops.Migrate(t.Context(), admissionURL(t, dsn, name, false), migrations.All()); err != nil {
		t.Fatal(err)
	}
	return dsn, name
}

func admissionAdmin(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	return admin
}

// admissionURL names database. With bounded set, every session it opens gives
// up on a lock after three seconds, so a self-wait fails the test instead of
// hanging it.
func admissionURL(t *testing.T, dsn, database string, bounded bool) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + database
	if bounded {
		q := u.Query()
		q.Set("lock_timeout", "3000")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// admissionFixture is a server connection with a verified daemon, a ready
// Cloud server that runs on it, and the first deployment of an approved plan
// that targets it. No deployment of the plan has succeeded yet, so every
// step write re-inserts the plan's connection uses.
type admissionCase struct {
	db     *pgdriver.PgDB
	runner *Runner
	plan   *planning.Plan
	action planning.Action
	// unbounded opens the same database without the lock timeout, for a
	// session that's meant to wait.
	unbounded func() *pgdriver.PgDB
}

func admissionFixture(t *testing.T, dsn, template string) admissionCase {
	t.Helper()
	name := fmt.Sprintf("%s_%d", template, time.Now().UnixNano())
	admin := admissionAdmin(t, dsn)
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})
	db := pgdriver.New()
	if err := db.Open(t.Context(), admissionURL(t, dsn, name, true)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj','org','One','one','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env','org','proj','production','production',now(),now())`,
		`INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('manifest','org','proj',1,'version: 1','digest','usr',now())`,
		`INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at) VALUES('cloud','org','proj','One',1,now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,details,status,created_by,created_at,updated_at) VALUES('` + admissionConnection + `','org','server','host','daemon','{"docker_id":"daemon"}','ready','usr',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,details,status,created_by,created_at,updated_at) VALUES('provider','org','hetzner','provider','account','{}','ready','usr',now(),now())`,
		`INSERT INTO loomplane_cloud_servers(id,org_id,cloud_id,connection_id,runtime_connection_id,provider,account_id,ownership,revision,status,body) VALUES('server','org','cloud','provider','` + admissionConnection + `','hetzner','account','managed',1,'ready','{"id":"server","org_id":"org","cloud_id":"cloud","runtime_connection_id":"` + admissionConnection + `","status":"ready"}')`,
		`INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,result,created_by,created_at) VALUES('plan','org','proj','env','production','approved','manifest',1,'{"connections":{"server":"` + admissionConnection + `"}}','usr',now())`,
		`INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at,steps) VALUES('deployment','org','proj','env','production','plan','reviewed','running','usr',now(),'[{"action_id":"service:api","status":"pending"}]')`,
	} {
		if _, err := db.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	var uses int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_connection_uses WHERE kind='plan' AND owner_id='plan' AND connection_id=$1`, admissionConnection).Scan(&uses); err != nil || uses != 1 {
		t.Fatalf("fixture plan does not use the server connection: %d %v", uses, err)
	}
	unbounded := func() *pgdriver.PgDB {
		waiting := pgdriver.New()
		if err := waiting.Open(t.Context(), admissionURL(t, dsn, name, false)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = waiting.Close() })
		return waiting
	}
	return admissionCase{
		db:        db,
		runner:    &Runner{store: NewPgStore(db)},
		plan:      &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Connections: map[string]string{"server": admissionConnection}},
		action:    planning.Action{ID: "service:api", Subject: planning.ServiceSubject, Name: "api", Op: planning.Create, Provider: "server", ConnectionID: admissionConnection},
		unbounded: unbounded,
	}
}

func retiringServer() *cloudinfra.Server {
	return &cloudinfra.Server{ID: "server", OrgID: "org", CloudID: "cloud", RuntimeConnectionID: admissionConnection, Status: "ready"}
}

// waitForLockWaiters returns once n sessions are queued on a lock.
func waitForLockWaiters(t *testing.T, db *pgdriver.PgDB, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions wait on a lock, want %d", waiting, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func serverUnavailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && strings.Contains(pgErr.Message, "server is not available")
}

func lockTimedOut(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

func TestDirectServerAdmission(t *testing.T) {
	dsn, template := admissionTemplate(t)
	t.Run("first deployment progress", func(t *testing.T) { testAdmissionAllowsFirstDeploymentProgress(t, dsn, template) })
	t.Run("retirement", func(t *testing.T) { testAdmissionSerializesRetirement(t, dsn, template) })
}

// A direct server deployment records its step progress while it holds host
// admission. The step write re-syncs the plan's connection uses, and that has
// to finish under the worker's own admission instead of waiting on it, even
// with a retirement already queued behind the admission.
func testAdmissionAllowsFirstDeploymentProgress(t *testing.T, dsn, template string) {
	c := admissionFixture(t, dsn, template)
	admission, _, err := c.runner.directServerAdmission(t.Context(), c.plan, c.action)
	if err != nil || admission == nil {
		t.Fatalf("admit: %v", err)
	}
	defer func() { _ = admission.Rollback() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if err = c.runner.store.UpdateStep(ctx, "org", "deployment", Step{ActionID: "service:api", Status: StepDeploying, StartedAt: &now}); err != nil {
		t.Fatalf("a first deployment's step write waits on its own host admission: %v", err)
	}

	// Open the waiting pool here: t.Fatal can't stop the test from a goroutine.
	waiting := cloudinfra.NewPgStore(c.unbounded())
	retiring := make(chan error, 1)
	go func() {
		retiring <- waiting.RetireServer(context.WithoutCancel(t.Context()), retiringServer())
	}()
	waitForLockWaiters(t, c.db, 1)
	finished := time.Now().UTC()
	if err = c.runner.store.UpdateStep(ctx, "org", "deployment", Step{ActionID: "service:api", Status: StepSucceeded, StartedAt: &now, FinishedAt: &finished}); err != nil {
		t.Fatalf("a queued retirement blocks the admitted deployment's step write: %v", err)
	}
	if err = admission.Rollback(); err != nil {
		t.Fatal(err)
	}
	// The plan still holds its connection use, so the queued retirement
	// refuses once the admission ends.
	if err = <-retiring; !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("retirement ignored the deployment's connection use: %v", err)
	}
}

// Retirement and admission exclude each other in both orders. A server can't
// be retired while a deployment holds its admission, and once retirement
// starts, no plan or binding can gain a use of the server, so no new
// deployment reaches it.
func testAdmissionSerializesRetirement(t *testing.T, dsn, template string) {
	c := admissionFixture(t, dsn, template)
	infra := cloudinfra.NewPgStore(c.db)

	admission, _, err := c.runner.directServerAdmission(t.Context(), c.plan, c.action)
	if err != nil || admission == nil {
		t.Fatalf("admit: %v", err)
	}
	if err = infra.RetireServer(t.Context(), retiringServer()); !lockTimedOut(err) {
		t.Fatalf("retirement did not wait for an admitted deployment: %v", err)
	}
	if err = admission.Rollback(); err != nil {
		t.Fatal(err)
	}

	// The plan still uses the connection, so retirement refuses outright.
	if err = infra.RetireServer(t.Context(), retiringServer()); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("retirement ignored the plan's connection use: %v", err)
	}

	// Once nothing uses the server, retirement waits for an open admission,
	// then starts.
	if _, err = c.db.Exec(t.Context(), `DELETE FROM loomplane_deployments WHERE id='deployment'`); err != nil {
		t.Fatal(err)
	}
	if _, err = c.db.Exec(t.Context(), `UPDATE loomplane_plans SET status='rejected' WHERE id='plan'`); err != nil {
		t.Fatal(err)
	}
	admission, _, err = c.runner.directServerAdmission(t.Context(), c.plan, c.action)
	if err != nil || admission == nil {
		t.Fatalf("admit: %v", err)
	}
	// Open the waiting pool here: t.Fatal can't stop the test from a goroutine.
	waiting := cloudinfra.NewPgStore(c.unbounded())
	retiring := make(chan error, 1)
	go func() {
		retiring <- waiting.RetireServer(context.WithoutCancel(t.Context()), retiringServer())
	}()
	waitForLockWaiters(t, c.db, 1)
	select {
	case err = <-retiring:
		t.Fatalf("retirement ran under an open admission: %v", err)
	default:
	}
	if err = admission.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = <-retiring; err != nil {
		t.Fatalf("retire unused server: %v", err)
	}

	if _, err = c.db.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,result,created_by,created_at) VALUES('late','org','proj','env','production','ready','manifest',1,'{"connections":{"server":"`+admissionConnection+`"}}','usr',now())`); !serverUnavailable(err) {
		t.Fatalf("a new plan used a retiring server: %v", err)
	}
	if _, err = c.db.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'binding','late')`, admissionConnection); !serverUnavailable(err) {
		t.Fatalf("a binding used a retiring server: %v", err)
	}
}
