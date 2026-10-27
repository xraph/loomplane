//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
)

// A Dispatch store call that outlasts the durable worker's store timeout
// mustn't leave plans in planning, the way an overloaded Postgres did.
// Since Dispatch v1.9.3 a claim that times out leaves the worker polling and
// the health check unhealthy until a claim gets through. A task result that
// can't be saved still stops the worker once Dispatch's own retries run out,
// and the supervisor in internal/platform has to replace it. Loomplane
// doesn't set the worker's timing, so this relies on Dispatch v1.9.3's
// defaults: a 30-second lease and a store timeout of min(5s, lease/4).
//
// The deployment and capacity workers share one health check, so it only
// proves a worker failed and recovered, not which one. New plans run on the
// capacity build, so the plans prove the worker that runs them kept working.
func TestDispatch_aStoreTimeoutDoesNotStopDurableWork(t *testing.T) {
	last := time.Now()
	took := func(what string) {
		t.Logf("%s took %s", what, time.Since(last).Round(100*time.Millisecond))
		last = time.Now()
	}
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = newFakeGoogle(t).srv.URL
	cfg.Clouds.VercelEndpoint = fakeVercel(t, "vcl_0123456789abcdef").URL
	cfg.Clouds.AWSEndpoint = newFakeAWS(t).srv.URL
	cfg.Storage.URL = "mem://"
	r := start(t, cfg)
	defer r.stop()
	took("starting the app")
	app, ok := r.app.(forge.App)
	if !ok {
		t.Fatal("the running app has no extensions")
	}
	jobs, err := app.GetExtension("dispatch")
	if err != nil {
		t.Fatal(err)
	}

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	took("signing up")
	analyze(t, cfg, c, proj.ID)
	took("analysis")
	var draft struct {
		YAML string `json:"yaml"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/manifest/draft", nil, &draft)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": draft.YAML}, nil)

	create := func() string {
		t.Helper()
		var p planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &p)
		return p.ID
	}
	// planned waits for a plan to settle. The project has no cloud
	// connections, so a plan that ran ends blocked; ready is fine too.
	// A plan the failed worker had claimed waits out its 30-second lease
	// before a replacement claims it, so the wait allows for that.
	planned := func(id string) {
		t.Helper()
		var p planBody
		deadline := time.Now().Add(90 * time.Second)
		for {
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+id, nil, &p)
			if p.Status != "planning" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("plan %s still planning after the store timeout", id)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if (p.Status != "blocked" && p.Status != "ready") || p.Error != "" {
			t.Fatalf("plan %s ended %q: %s", id, p.Status, p.Error)
		}
		// Only a plan the planner ran has actions or problems.
		if len(p.Actions)+len(p.Problems) == 0 {
			t.Fatalf("plan %s settled %q without being computed: %+v", id, p.Status, p)
		}
	}
	planned(create())
	took("the first plan")

	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	// healthy fails unless the dispatch health check passes now, so a
	// worker that was already down can't count as the injected failure.
	healthy := func() {
		t.Helper()
		if health := jobs.Health(t.Context()); health != nil {
			t.Fatalf("dispatch unhealthy before the injected failure: %v", health)
		}
	}
	// injected waits until waiting() shows the store call stuck in
	// Postgres and the health check reports the worker failing in the way
	// the case expects: claims it can't make, or a stop.
	injected := func(what, failing string, within time.Duration, waiting func() bool) {
		t.Helper()
		seen, failed := false, false
		deadline := time.Now().Add(within)
		for !seen || !failed {
			if time.Now().After(deadline) {
				t.Fatalf("no injected failure after %s: %s %t, the worker %s %t", within, what, seen, failing, failed)
			}
			seen = seen || waiting()
			if health := jobs.Health(t.Context()); health != nil && strings.Contains(health.Error(), failing) {
				failed = true
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// recovered waits for the health check to pass again: a claim got
	// through, or the supervisor replaced a stopped worker, which never runs
	// again.
	recovered := func() {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for jobs.Health(t.Context()) != nil {
			if time.Now().After(deadline) {
				t.Fatalf("dispatch still unhealthy after the injected failure: %v", jobs.Health(t.Context()))
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// The worker times out while it's idle: a lock on the task table makes
	// its next claim wait past the store timeout, and the worker keeps
	// polling. A plan made once the lock is gone is planned.
	healthy()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), "SET LOCAL lock_timeout = '30s'"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), "LOCK TABLE dispatch_execution_tasks IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	injected("a query waiting on the lock", "can't claim tasks", 30*time.Second, func() bool {
		if _, clearErr := tx.Exec(t.Context(), "SELECT pg_stat_clear_snapshot()"); clearErr != nil {
			t.Fatal(clearErr)
		}
		var n int
		if scanErr := tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
			AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%dispatch_execution_tasks%'`).Scan(&n); scanErr != nil {
			t.Fatal(scanErr)
		}
		return n > 0
	})
	// Both workers' claims waited on the same lock; give the second
	// timeout a moment to land too.
	time.Sleep(500 * time.Millisecond)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	planned(create())
	recovered()
	took("the idle timeout")

	// The worker times out with a plan in flight: a trigger stalls the
	// write that marks a plan's activity done, so the claim and the
	// planner run and only that commit outlasts the store timeout. The
	// planner has saved the plan by then, so the plan's status can't show
	// recovery. Its Dispatch run has to complete instead, which takes a
	// second attempt at the activity once the stalled claim's lease runs
	// out. Earlier plans have all settled, so the trigger can only catch
	// the one made here.
	healthy()
	dropStall := func(ctx context.Context, db *pgx.Conn) error {
		_, dropErr := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_test_stall ON dispatch_execution_tasks;
			DROP FUNCTION IF EXISTS loomplane_test_stall()`)
		return dropErr
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if db, connErr := pgx.Connect(ctx, cfg.Database.URL); connErr == nil {
			_ = dropStall(ctx, db)
			_ = db.Close(ctx)
		}
	})
	defer func() { _ = dropStall(context.Background(), conn) }()
	if _, err = conn.Exec(t.Context(), `CREATE FUNCTION loomplane_test_stall() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_sleep(10); RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `CREATE TRIGGER loomplane_test_stall BEFORE UPDATE ON dispatch_execution_tasks FOR EACH ROW
		WHEN (NEW.done AND NOT OLD.done AND NEW.kind = 'activity' AND starts_with(NEW.workflow_id, 'plan_'))
		EXECUTE FUNCTION loomplane_test_stall()`); err != nil {
		t.Fatal(err)
	}
	inFlight := create()
	// Dispatch retries the save three times before the worker stops.
	injected("the activity's completion sleeping in the trigger", "stopped", 60*time.Second, func() bool {
		var n int
		if scanErr := conn.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
			AND wait_event='PgSleep'`).Scan(&n); scanErr != nil {
			t.Fatal(scanErr)
		}
		return n > 0
	})
	if err = dropStall(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		var state string
		var attempt int64
		if err = conn.QueryRow(t.Context(), `SELECT e.state, max(k.attempt) FROM dispatch_executions e
			JOIN dispatch_execution_tasks k USING (namespace, workflow_id, run_id)
			WHERE e.workflow_id=$1 AND k.kind='activity' GROUP BY e.state`, inFlight).Scan(&state, &attempt); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			if attempt < 2 {
				t.Fatalf("plan %s's run completed without retrying its stalled activity (attempt %d)", inFlight, attempt)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("plan %s's Dispatch run still %s after the store timeout", inFlight, state)
		}
		time.Sleep(200 * time.Millisecond)
	}
	planned(inFlight)
	recovered()
	took("the in-flight timeout")
}
