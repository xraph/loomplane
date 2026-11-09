package currentauthority

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/go-utils/metrics"
	"github.com/xraph/grove"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	"github.com/xraph/warden/checklog"
	"github.com/xraph/warden/id"
	"github.com/xraph/warden/permission"
	"github.com/xraph/warden/policy"
	"github.com/xraph/warden/relation"
	"github.com/xraph/warden/role"
	"github.com/xraph/warden/store"
	wardenpg "github.com/xraph/warden/store/postgres"
)

type wardenPGFixture struct {
	c        *Coordinator
	main     *pgdriver.PgDB
	ordinary *wardenpg.Store
	router   *storeRouter
	engine   *warden.Engine
	schema   string
	metrics  metrics.Metrics
}

func newWardenPGFixture(t *testing.T) *wardenPGFixture {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN for isolated PostgreSQL 55471")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.Port != 55471 || (cfg.ConnConfig.Host != "localhost" && cfg.ConnConfig.Host != "127.0.0.1") {
		t.Fatal("authority fixture requires isolated PostgreSQL 55471")
	}
	schema := fmt.Sprintf("warden_authority_%d_%d", time.Now().UnixNano(), schemaSequence.Add(1))
	dsn, err = withDSNParams(dsn, map[string]string{"search_path": schema, "pool_max_conns": "1", "pool_min_conns": "0", "pool_min_idle_conns": "0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	main := pgdriver.New()
	if checkErr := main.Open(ctx, dsn, driver.WithPoolSize(1)); checkErr != nil {
		t.Fatal(checkErr)
	}
	db, err := grove.Open(main)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if checkErr := db.Close(); checkErr != nil {
			t.Error(checkErr)
		}
	})
	if _, checkErr := main.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); checkErr != nil {
		t.Fatal(checkErr)
	}
	t.Cleanup(func() {
		dropCtx, stopDrop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopDrop()
		if _, checkErr := main.Exec(dropCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); checkErr != nil {
			t.Error(checkErr)
		}
	})
	ordinary := wardenpg.New(db)
	if checkErr := ordinary.Migrate(ctx); checkErr != nil {
		t.Fatal(checkErr)
	}
	// These adjacent identity tables provide the fence/temporal fixture only.
	// They do not qualify installed Authsome or Keysmith principal validation.
	for _, table := range authorityTables {
		if strings.HasPrefix(table, "warden_") {
			continue
		}
		if _, checkErr := main.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, table}.Sanitize()+` (id text PRIMARY KEY, expires_at timestamptz, ban_expires timestamptz, banned boolean DEFAULT false)`); checkErr != nil {
			t.Fatal(checkErr)
		}
	}
	if _, checkErr := main.Exec(ctx, `CREATE TABLE candidates (id text PRIMARY KEY, authorized_at timestamptz NOT NULL)`); checkErr != nil {
		t.Fatal(checkErr)
	}
	sink := metrics.NewMetricsCollector("authority_fixture")
	native, err := openPools(ctx, dsn, sink)
	if err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{backend: native, reader: wardenpg.New(native.readerDB), schema: schema, telemetry: native.telemetry, source: func(ctx context.Context, tx driver.Tx, _ Binding) error {
		_, sourceErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, schema)
		return sourceErr
	}}
	router := &storeRouter{owner: c, ordinary: ordinary}
	engine, err := warden.NewEngine(warden.WithStore(router), warden.WithCache(NewFreshCache(c, warden.NewMemoryCache())))
	if err != nil {
		t.Fatal(err)
	}
	c.profile, err = captureEngineProfile(engine, router)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if checkErr := engine.Stop(ctx); checkErr != nil {
			t.Error(checkErr)
		}
		if checkErr := c.Shutdown(ctx); checkErr != nil {
			t.Error(checkErr)
		}
	})
	return &wardenPGFixture{c: c, main: main, ordinary: ordinary, router: router, engine: engine, schema: schema, metrics: sink}
}

func (f *wardenPGFixture) checkAndStage(ctx context.Context, a *Attempt, b Binding, req *warden.CheckRequest, want bool) error {
	d, err := a.Evaluate(ctx, func(ctx context.Context) error {
		result, err := f.engine.Check(ctx, req)
		if err != nil {
			return err
		}
		if result.Allowed != want {
			return fmt.Errorf("allowed=%t want=%t", result.Allowed, want)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.Stage(ctx, d, b, func(ctx context.Context, tx driver.Tx, at time.Time) error {
		_, err := tx.Exec(ctx, `INSERT INTO candidates(id,authorized_at) VALUES($1,$2)`, b.RecordID, at)
		return err
	})
}

func seedInheritedRBAC(t *testing.T, f *wardenPGFixture, resource bool) (*warden.CheckRequest, *assignment.Assignment) {
	t.Helper()
	ctx := context.Background()
	parent := &role.Role{ID: id.NewRoleID(), TenantID: "org-1", Name: "parent", Slug: "parent"}
	child := &role.Role{ID: id.NewRoleID(), TenantID: "org-1", Name: "child", Slug: "child", ParentSlug: parent.Slug}
	perm := &permission.Permission{ID: id.NewPermissionID(), TenantID: "org-1", Name: "document:read", Resource: "document", Action: "read"}
	for _, r := range []*role.Role{parent, child} {
		if checkErr := f.ordinary.CreateRole(ctx, r); checkErr != nil {
			t.Fatal(checkErr)
		}
	}
	if checkErr := f.ordinary.CreatePermission(ctx, perm); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := f.ordinary.AttachPermission(ctx, "org-1", parent.ID, permission.Ref{Name: perm.Name}); checkErr != nil {
		t.Fatal(checkErr)
	}
	assignment := &assignment.Assignment{ID: id.NewAssignmentID(), TenantID: "org-1", RoleID: child.ID, SubjectKind: "user", SubjectID: "user-1"}
	if resource {
		assignment.ResourceType = "document"
		assignment.ResourceID = "doc-1"
	}
	if checkErr := f.ordinary.CreateAssignment(ctx, assignment); checkErr != nil {
		t.Fatal(checkErr)
	}
	return &warden.CheckRequest{TenantID: "org-1", Subject: warden.Subject{Kind: warden.SubjectUser, ID: "user-1"}, Action: warden.Action{Name: "read"}, Resource: warden.Resource{Type: "document", ID: "doc-1"}}, assignment
}

func TestWardenPostgresFreshCacheAndInheritedRBAC(t *testing.T) {
	for _, resource := range []bool{false, true} {
		t.Run(fmt.Sprintf("resource=%v", resource), func(t *testing.T) {
			f := newWardenPGFixture(t)
			req, assignment := seedInheritedRBAC(t, f, resource)
			ctx := context.Background()
			warm, err := f.engine.Check(ctx, req)
			if err != nil || !warm.Allowed {
				t.Fatalf("warm=%v err=%v", warm, err)
			}
			b := testBinding()
			if checkErr := f.c.Run(ctx, b, func(ctx context.Context, a *Attempt) error { return f.checkAndStage(ctx, a, b, req, true) }); checkErr != nil {
				t.Fatal(checkErr)
			}
			// Direct SQL represents an independent authority writer and bypasses hooks.
			if checkErr := f.ordinary.DeleteAssignment(ctx, "org-1", assignment.ID); checkErr != nil {
				t.Fatal(checkErr)
			}
			cached, err := f.engine.Check(ctx, req)
			if err != nil || !cached.Allowed {
				t.Fatalf("ordinary cache changed=%v err=%v", cached, err)
			}
			b.RecordID = "after-revoke"
			b.AttemptID = "after-revoke"
			if checkErr := f.c.Run(ctx, b, func(ctx context.Context, a *Attempt) error { return f.checkAndStage(ctx, a, b, req, false) }); checkErr != nil {
				t.Fatal(checkErr)
			}
			cached, err = f.engine.Check(ctx, req)
			if err != nil || !cached.Allowed {
				t.Fatalf("current deny populated ordinary cache=%v err=%v", cached, err)
			}
			f.engine.Plugins().EmitRoleUnassigned(ctx, assignment)
			invalidated, err := f.engine.Check(ctx, req)
			if err != nil || invalidated.Allowed {
				t.Fatalf("automatic invalidator missed revoke=%v err=%v", invalidated, err)
			}
			if checkErr := f.engine.Stop(ctx); checkErr != nil {
				t.Fatal(checkErr)
			}
			count, err := f.ordinary.CountCheckLogs(ctx, &checklog.QueryFilter{TenantID: "org-1"})
			if err != nil || count < 6 {
				t.Fatalf("ordinary background logs=%d err=%v", count, err)
			}
		})
	}
}

func TestWardenPostgresABACAndRelations(t *testing.T) {
	f := newWardenPGFixture(t)
	req, _ := seedInheritedRBAC(t, f, false)
	ctx := context.Background()
	deny := &policy.Policy{ID: id.NewPolicyID(), TenantID: "org-1", Name: "deny-docs", Effect: policy.EffectDeny, IsActive: true, Actions: []string{"read"}, Resources: []string{"document"}, Subjects: []policy.SubjectMatch{{Kind: "user", ID: "user-1"}}}
	if checkErr := f.ordinary.CreatePolicy(ctx, deny); checkErr != nil {
		t.Fatal(checkErr)
	}
	b := testBinding()
	if checkErr := f.c.Run(ctx, b, func(ctx context.Context, a *Attempt) error { return f.checkAndStage(ctx, a, b, req, false) }); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := f.ordinary.DeletePolicy(ctx, "org-1", deny.ID); checkErr != nil {
		t.Fatal(checkErr)
	}
	req.Subject.ID = "relation-user"
	tuple := &relation.Tuple{ID: id.NewRelationID(), TenantID: "org-1", ObjectType: "document", ObjectID: "doc-1", Relation: "read", SubjectType: "user", SubjectID: "relation-user"}
	if checkErr := f.ordinary.CreateRelation(ctx, tuple); checkErr != nil {
		t.Fatal(checkErr)
	}
	b.RecordID = "relation"
	b.AttemptID = "relation"
	if checkErr := f.c.Run(ctx, b, func(ctx context.Context, a *Attempt) error { return f.checkAndStage(ctx, a, b, req, true) }); checkErr != nil {
		t.Fatal(checkErr)
	}
}

// A fixture wrapper injects a backend error into the installed parent-role
// lookup. Production routers have no embedded fallback Store.
type parentErrorStore struct {
	store.Store
	failure error
}

func (s parentErrorStore) GetRoleBySlug(context.Context, string, string, string) (*role.Role, error) {
	return nil, s.failure
}

func TestWardenPostgresSwallowedParentErrorPoisons(t *testing.T) {
	f := newWardenPGFixture(t)
	req, _ := seedInheritedRBAC(t, f, false)
	failure := errors.New("fixture parent lookup failed")
	f.c.reader = parentErrorStore{Store: f.c.reader, failure: failure}
	err := f.c.Run(context.Background(), testBinding(), func(ctx context.Context, a *Attempt) error {
		_, _ = f.engine.Check(ctx, req) // Engine may skip the broken inherited role.
		_, err := a.Evaluate(ctx, func(context.Context) error { return nil })
		return err
	})
	if !errors.Is(err, failure) || !errors.Is(err, ErrPoisoned) {
		t.Fatalf("swallowed backend error=%v", err)
	}
}

type mutatingCheckHook struct {
	target        store.Store
	before, after int
}

func (p *mutatingCheckHook) Name() string { return "fixture-mutation" }
func (p *mutatingCheckHook) OnBeforeCheck(ctx context.Context, _ any) error {
	p.before++
	return p.target.CreateRole(ctx, &role.Role{ID: id.NewRoleID(), TenantID: "org-1", Name: "hook-role", Slug: "hook-role"})
}
func (p *mutatingCheckHook) OnAfterCheck(context.Context, any, any) error { p.after++; return nil }

func TestWardenPostgresHookPoisonAndProfileRejection(t *testing.T) {
	f := newWardenPGFixture(t)
	req, _ := seedInheritedRBAC(t, f, false)
	hook := &mutatingCheckHook{target: f.router}
	f.engine.Plugins().Register(hook)
	if _, checkErr := captureEngineProfile(f.engine, f.router); !errors.Is(checkErr, ErrUnavailable) {
		t.Fatal("mutating plugin accepted by installed profile")
	}
	// Deliberate mechanism-only fixture: verify refusal even when a caller's
	// unsupported hook swallows the write error. No installed allowlist exception.
	f.c.profile = &profile{}
	err := f.c.Run(context.Background(), testBinding(), func(ctx context.Context, a *Attempt) error {
		_, _ = f.engine.Check(ctx, req)
		_, err := a.Evaluate(ctx, func(context.Context) error { return nil })
		return err
	})
	if !errors.Is(err, ErrReadOnly) || !errors.Is(err, ErrPoisoned) || hook.before != 1 {
		t.Fatalf("hook poison=%v before=%d", err, hook.before)
	}
	if _, checkErr := f.ordinary.GetRoleBySlug(context.Background(), "org-1", "", "hook-role"); !errors.Is(checkErr, warden.ErrRoleNotFound) {
		t.Fatalf("forbidden hook reached SQL: %v", checkErr)
	}
	// Ordinary contexts still run both hooks and their original Store effects.
	if _, checkErr := f.engine.Check(context.Background(), req); checkErr != nil {
		t.Fatal(checkErr)
	}
	if hook.before != 2 || hook.after == 0 {
		t.Fatalf("ordinary hooks=%d/%d", hook.before, hook.after)
	}
	if _, checkErr := f.ordinary.GetRoleBySlug(context.Background(), "org-1", "", "hook-role"); checkErr != nil {
		t.Fatal(checkErr)
	}
}

func TestWardenPostgresProfileCheckedBeforeSeal(t *testing.T) {
	f := newWardenPGFixture(t)
	req, _ := seedInheritedRBAC(t, f, false)
	b := testBinding()
	err := f.c.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error {
		if checkErr := f.checkAndStage(ctx, a, b, req, true); checkErr != nil {
			return checkErr
		}
		f.engine.Plugins().Register(&namedPlugin{name: "warden-cache-invalidator"})
		return nil
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("profile changed before seal=%v", err)
	}
	var count int
	if checkErr := f.main.QueryRow(context.Background(), `SELECT count(*) FROM candidates`).Scan(&count); checkErr != nil || count != 0 {
		t.Fatalf("candidate=%d checkErr=%v", count, checkErr)
	}
}

type pendingReadStore struct {
	store.Store
	entered chan struct{}
}

func (s pendingReadStore) Ping(ctx context.Context) error {
	close(s.entered)
	<-ctx.Done()
	return ctx.Err()
}

func TestWardenPostgresPendingReaderPreventsSeal(t *testing.T) {
	f := newWardenPGFixture(t)
	req, _ := seedInheritedRBAC(t, f, false)
	b := testBinding()
	entered := make(chan struct{})
	done := make(chan error, 1)
	original := f.c.reader
	err := f.c.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error {
		if checkErr := f.checkAndStage(ctx, a, b, req, true); checkErr != nil {
			return checkErr
		}
		f.c.reader = pendingReadStore{Store: original, entered: entered}
		go func() { done <- f.router.Ping(ctx) }()
		<-entered
		return nil
	})
	if !errors.Is(err, ErrPending) {
		t.Fatalf("pending reader sealed=%v", err)
	}
	if readErr := <-done; !errors.Is(readErr, context.Canceled) {
		t.Fatalf("reader not canceled=%v", readErr)
	}
	f.c.reader = original
	if checkErr := f.c.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error { return f.checkAndStage(ctx, a, b, req, true) }); checkErr != nil {
		t.Fatal("later clean attempt", checkErr)
	}
}

func TestWardenPostgresConnectionQualification(t *testing.T) {
	f := newWardenPGFixture(t)
	native, ok := f.c.backend.(*nativeBackend)
	if !ok {
		t.Fatal("fixture backend")
	}
	ctx := context.Background()
	proof, err := inspectConnections(ctx, f.main, native, f.schema)
	if err != nil || proof == nil {
		t.Fatalf("connection profile=%v", err)
	}
	if len(f.metrics.ListMetricsByType(metrics.MetricTypeHistogram)) == 0 {
		t.Fatal("owned connection inspection produced no operation metrics")
	}

	if _, checkErr := inspectConnections(ctx, f.main, native, "wrong_schema"); !errors.Is(checkErr, ErrUnavailable) {
		t.Fatalf("wrong schema=%v", checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `ALTER TABLE warden_roles ENABLE ROW LEVEL SECURITY`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := inspectConnections(ctx, f.main, native, f.schema); !errors.Is(checkErr, ErrUnavailable) {
		t.Fatalf("RLS=%v", checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `ALTER TABLE warden_roles DISABLE ROW LEVEL SECURITY`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `ALTER TABLE keysmith_keys RENAME COLUMN expires_at TO removed_expiry`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := inspectConnections(ctx, f.main, native, f.schema); !errors.Is(checkErr, ErrUnavailable) {
		t.Fatalf("missing temporal column=%v", checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `ALTER TABLE keysmith_keys RENAME COLUMN removed_expiry TO expires_at`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `ALTER TABLE keysmith_scopes RENAME TO removed_scopes`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := inspectConnections(ctx, f.main, native, f.schema); !errors.Is(checkErr, ErrUnavailable) {
		t.Fatalf("missing table=%v", checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `ALTER TABLE removed_scopes RENAME TO keysmith_scopes`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `CREATE FUNCTION fixture_trigger() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'; CREATE TRIGGER fixture_unknown BEFORE INSERT ON warden_roles FOR EACH ROW EXECUTE FUNCTION fixture_trigger()`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := inspectConnections(ctx, f.main, native, f.schema); !errors.Is(checkErr, ErrUnavailable) {
		t.Fatalf("unknown trigger=%v", checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `DROP TRIGGER fixture_unknown ON warden_roles; DROP FUNCTION fixture_trigger()`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := f.main.Exec(ctx, `SET ROLE pg_read_all_data`); checkErr != nil {
		t.Fatal(checkErr)
	}
	_, roleErr := inspectConnections(ctx, f.main, native, f.schema)
	if _, checkErr := f.main.Exec(ctx, `RESET ROLE`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if !errors.Is(roleErr, ErrUnavailable) {
		t.Fatalf("changed effective role=%v", roleErr)
	}
	// This cluster grants PUBLIC execution by default. Revoke it only inside a
	// transaction, inspect the denied session, then roll back the entire fixture.
	// Other sessions never observe an ACL change and no role is granted access.
	tx, err := f.main.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, checkErr := tx.Exec(ctx, `REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM PUBLIC`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := tx.Exec(ctx, `SET LOCAL ROLE pg_read_all_data`); checkErr != nil {
		t.Fatal(checkErr)
	}
	_, profileErr := inspectConnection(ctx, func(ctx context.Context, q string, args ...any) profileScanner { return tx.QueryRow(ctx, q, args...) }, f.schema)
	if checkErr := tx.Rollback(); checkErr != nil {
		t.Fatal(checkErr)
	}
	var privilegePG *pgconn.PgError
	if !errors.As(profileErr, &privilegePG) || privilegePG.Code != "42501" || !errors.Is(profileErr, ErrUnavailable) {
		t.Fatalf("missing inspection privilege=%v", profileErr)
	}
	if _, checkErr := inspectConnections(ctx, f.main, native, f.schema); checkErr != nil {
		t.Fatal("restored profile", checkErr)
	}
}

func TestWardenPostgresReaderDDLTimeoutAndRetainedScope(t *testing.T) {
	f := newWardenPGFixture(t)
	req, assigned := seedInheritedRBAC(t, f, false)
	native, ok := f.c.backend.(*nativeBackend)
	if !ok {
		t.Fatal("fixture backend")
	}
	ddlCtx, stopDDL := context.WithTimeout(context.Background(), AttemptLimit+CleanupLimit)
	defer stopDDL()
	ddlResult := make(chan error, 1)
	var retained context.Context
	var writerPID, readerPID, oid uint32
	queued, waiting := false, false
	var readFailure error
	err := f.c.Run(context.Background(), testBinding(), func(ctx context.Context, a *Attempt) error {
		retained = ctx
		if checkErr := a.Tx().QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); checkErr != nil {
			return checkErr
		}
		if checkErr := native.reader.QueryRow(ctx, `SELECT pg_backend_pid(),'warden_roles'::regclass::oid`).Scan(&readerPID, &oid); checkErr != nil {
			return checkErr
		}
		go func() {
			_, err := f.main.Exec(ddlCtx, `ALTER TABLE warden_roles ADD COLUMN reader_probe int`)
			ddlResult <- err
		}()
		if checkErr := waitDDLQualificationLock(ctx, a, oid, 0, "AccessExclusiveLock"); checkErr != nil {
			return checkErr
		}
		queued = true
		_, err := a.Evaluate(ctx, func(ctx context.Context) error {
			readDone := make(chan error, 1)
			readerCtx, stopReader := context.WithCancel(ctx)
			defer stopReader()
			go func() { _, err := f.router.GetRole(readerCtx, "org-1", assigned.RoleID); readDone <- err }()
			// Test-only observation on the idle writer connection. The public writer
			// adapter correctly refuses SQL while the reader owns the shared gate.
			for {
				rows, queryErr := a.tx.Query(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND relation=$2 AND mode='AccessShareLock' AND NOT granted)`, readerPID, oid)
				if queryErr != nil {
					stopReader()
					<-readDone
					return queryErr
				}
				waiting, queryErr = pgx.CollectExactlyOneRow(rows, pgx.RowTo[bool])
				if queryErr != nil {
					stopReader()
					<-readDone
					return queryErr
				}
				if waiting {
					break
				}
				select {
				case <-ctx.Done():
					stopReader()
					<-readDone
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
			readFailure = <-readDone
			return readFailure
		})
		return err
	})
	if !queued || !waiting {
		t.Fatalf("DDL reader schedule incomplete queued=%t waiting=%t err=%v", queued, waiting, err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(readFailure, &pgErr) || pgErr.Code != "55P03" || !errors.Is(err, ErrPoisoned) {
		t.Fatalf("reader timeout=%v Run=%v", readFailure, err)
	}
	var locks int
	probeCtx, stopProbe := context.WithTimeout(context.Background(), CleanupLimit)
	defer stopProbe()
	if checkErr := native.reader.QueryRow(probeCtx, `SELECT count(*) FROM pg_locks WHERE pid=$1 AND mode='ShareLock' AND granted`, writerPID).Scan(&locks); checkErr != nil || locks != 0 {
		t.Fatalf("remaining authority locks=%d checkErr=%v", locks, checkErr)
	}
	select {
	case err := <-ddlResult:
		if err != nil {
			t.Fatal("DDL completion", err)
		}
	case <-ddlCtx.Done():
		t.Fatal("DDL completion deadline", ddlCtx.Err())
	}
	if _, checkErr := f.router.GetRole(retained, "org-1", assigned.RoleID); !errors.Is(checkErr, ErrExpired) {
		t.Fatalf("retained scope=%v", checkErr)
	}
	var count int
	if checkErr := f.main.QueryRow(context.Background(), `SELECT count(*) FROM candidates`).Scan(&count); checkErr != nil || count != 0 {
		t.Fatalf("timeout candidates=%d checkErr=%v", count, checkErr)
	}
	b := testBinding()
	if checkErr := f.c.Run(context.Background(), b, func(ctx context.Context, a *Attempt) error { return f.checkAndStage(ctx, a, b, req, true) }); checkErr != nil {
		t.Fatal("later clean attempt", checkErr)
	}
}

func TestWardenPostgresShutdownDrainsReaderOnly(t *testing.T) {
	f := newWardenPGFixture(t)
	req, assigned := seedInheritedRBAC(t, f, false)
	entered := make(chan struct{})
	f.c.reader = pendingReadStore{Store: f.c.reader, entered: entered}
	done := make(chan error, 1)
	go func() {
		done <- f.c.Run(context.Background(), testBinding(), func(ctx context.Context, a *Attempt) error {
			_, err := a.Evaluate(ctx, func(ctx context.Context) error { return f.router.Ping(ctx) })
			return err
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), AttemptLimit)
	defer cancel()
	if checkErr := f.c.Shutdown(ctx); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := <-done; !errors.Is(checkErr, ErrPoisoned) {
		t.Fatalf("shutdown Run=%v", checkErr)
	}
	if _, checkErr := f.ordinary.GetRole(ctx, "org-1", assigned.RoleID); checkErr != nil {
		t.Fatal("shared main closed", checkErr)
	}
	if _, checkErr := f.engine.Check(ctx, req); checkErr != nil {
		t.Fatal("ordinary engine after owned shutdown", checkErr)
	}
	if checkErr := f.c.Run(ctx, testBinding(), func(context.Context, *Attempt) error { t.Fatal("shutdown authority ran"); return nil }); !errors.Is(checkErr, ErrUnavailable) {
		t.Fatalf("shutdown admission=%v", checkErr)
	}
}

func TestWardenPostgresWrongDatabaseLeavesOrdinaryStoreUsable(t *testing.T) {
	f := newWardenPGFixture(t)
	_, assigned := seedInheritedRBAC(t, f, false)
	source := os.Getenv("LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN")
	primary, err := pgxpool.ParseConfig(source)
	if err != nil {
		t.Fatal("fixture configuration")
	}
	other := "postgres"
	if primary.ConnConfig.Database == other {
		other = "template1"
	}
	// Deliberately construct a different local fixture connection. The production
	// override helper must continue refusing database identity changes.
	wrongURL := url.URL{Scheme: "postgres", User: url.UserPassword(primary.ConnConfig.User, primary.ConnConfig.Password), Host: net.JoinHostPort(primary.ConnConfig.Host, strconv.Itoa(int(primary.ConnConfig.Port))), Path: "/" + other}
	wrongURL.RawQuery = url.Values{"sslmode": {"disable"}, "search_path": {f.schema}}.Encode()
	wrongDSN := wrongURL.String()
	parsed, err := pgxpool.ParseConfig(wrongDSN)
	if err != nil || parsed.ConnConfig.Database != other || parsed.ConnConfig.Host != primary.ConnConfig.Host || parsed.ConnConfig.Port != primary.ConnConfig.Port || parsed.ConnConfig.User != primary.ConnConfig.User || parsed.ConnConfig.Password != primary.ConnConfig.Password {
		t.Fatal("wrong-database fixture was not selected")
	}
	// Replace the existing two owned pools so the fixture still has one main,
	// one writer and one reader, with no additional reader pool.
	if checkErr := f.c.backend.close(); checkErr != nil {
		t.Fatal(checkErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), AttemptLimit)
	defer cancel()
	wrong, err := openPools(ctx, wrongDSN, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.c.backend = wrong
	f.c.reader = wardenpg.New(wrong.readerDB)
	var database string
	if checkErr := wrong.reader.QueryRow(ctx, `SELECT current_database()`).Scan(&database); checkErr != nil || database != other {
		t.Fatalf("actual wrong database=%q checkErr=%v", database, checkErr)
	}
	if _, checkErr := inspectConnections(ctx, f.main, wrong, f.schema); !errors.Is(checkErr, ErrUnavailable) {
		t.Fatalf("wrong database accepted=%v", checkErr)
	}
	if _, checkErr := f.ordinary.GetRole(ctx, "org-1", assigned.RoleID); checkErr != nil {
		t.Fatal("ordinary store changed after qualification refusal", checkErr)
	}
}

func TestWardenPostgresSourceCannotReadBeforeFence(t *testing.T) {
	f := newWardenPGFixture(t)
	source := f.c.source
	f.c.source = func(ctx context.Context, tx driver.Tx, b Binding) error {
		if err := source(ctx, tx, b); err != nil {
			return err
		}
		_ = f.router.Ping(ctx)
		return nil
	}
	err := f.c.Run(context.Background(), testBinding(), func(context.Context, *Attempt) error {
		t.Fatal("poisoned source reached work before a safe fence")
		return nil
	})
	if !errors.Is(err, ErrPoisoned) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("pre-fence read=%v", err)
	}
}
