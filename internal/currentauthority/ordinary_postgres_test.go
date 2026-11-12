package currentauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	authid "github.com/xraph/authsome/id"
	"github.com/xraph/grove"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"
	"github.com/xraph/grove/observability"
	"github.com/xraph/grove/observability/telemetrytest"
	keyid "github.com/xraph/keysmith/id"
	keypg "github.com/xraph/keysmith/store/postgres"
	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
	"github.com/xraph/warden/policy"
	"github.com/xraph/warden/role"
	"github.com/xraph/warden/store"
	wardenpg "github.com/xraph/warden/store/postgres"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

type ordinaryPGFixture struct {
	c       *Coordinator
	main    *pgdriver.PgDB
	db      *grove.DB
	store   *wardenpg.Store
	engine  *warden.Engine
	authz   *security.Authorizer
	schema  string
	dsn     string
	role    uint32
	binding Binding
	env     string
	project string
	probe   *ordinaryCheckProbe
}

func newOrdinaryPGFixture(t *testing.T) *ordinaryPGFixture {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN for isolated PostgreSQL 55471")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.Port != 55471 || (cfg.ConnConfig.Host != "localhost" && cfg.ConnConfig.Host != "127.0.0.1") {
		t.Fatal("requires isolated PostgreSQL 55471")
	}
	schema := fmt.Sprintf("ordinary_authority_%d", time.Now().UnixNano())
	dsn, err = withDSNParams(dsn, map[string]string{"search_path": schema, "pool_max_conns": "1", "pool_min_conns": "0", "pool_min_idle_conns": "0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	main := pgdriver.New()
	if err = main.Open(ctx, dsn, driver.WithPoolSize(1)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if checkErr := main.Close(); checkErr != nil {
			t.Error(checkErr)
		}
	})
	if _, err = main.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, checkErr := main.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); checkErr != nil {
			t.Error(checkErr)
		}
	})
	// Run the actual complete installed groups. Known project triggers and their
	// dependencies are retained, never replaced by stripped fixture tables.
	if _, err = migrate.NewOrchestrator(pgmigrate.New(main), migrations.All()...).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(main)
	if err != nil {
		t.Fatal(err)
	}
	f := &ordinaryPGFixture{main: main, db: db, schema: schema, dsn: dsn, store: wardenpg.New(db), env: authid.NewEnvironmentID().String(), project: "proj_01h455vb4pex5vsknk084sn02q"}
	if err = main.QueryRow(ctx, "SELECT current_user::regrole::oid::bigint").Scan(&f.role); err != nil {
		t.Fatal(err)
	}
	b := testBinding()
	b.Actor = security.Principal{UserID: authid.NewUserID().String(), OrgID: authid.NewOrgID().String()}
	b.AppID = authid.NewAppID().String()
	f.binding = b
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO authsome_apps(id,name,slug) VALUES($1,'Authority','authority')`, []any{b.AppID}},
		{`INSERT INTO authsome_environments(id,app_id,name,slug,type) VALUES($1,$2,'Prod','prod','production')`, []any{f.env, b.AppID}},
		{`INSERT INTO authsome_users(id,app_id,env_id,email,email_verified) VALUES($1,$2,$3,'authority@example.invalid',true)`, []any{b.Actor.UserID, b.AppID, f.env}},
		{`INSERT INTO authsome_organizations(id,app_id,env_id,name,slug,created_by) VALUES($1,$2,$4,'Workspace','workspace',$3)`, []any{b.Actor.OrgID, b.AppID, b.Actor.UserID, f.env}},
		{`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES($1,$2,$3,'member')`, []any{authid.NewMemberID().String(), b.Actor.OrgID, b.Actor.UserID}},
		{`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES($1,$2,'Project','project','ready',$3,now(),now())`, []any{f.project, b.Actor.OrgID, b.Actor.UserID}},
		{`CREATE TABLE authority_candidates(id text PRIMARY KEY,authorized_at timestamptz NOT NULL)`, nil},
	} {
		if _, err = main.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	native, err := openPools(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.c = &Coordinator{backend: native, reader: wardenpg.New(native.readerDB), schema: schema, telemetry: native.telemetry, source: func(ctx context.Context, tx driver.Tx, _ Binding) error {
		_, checkErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`)
		return checkErr
	}}
	router := &storeRouter{owner: f.c, ordinary: f.store}
	f.probe = &ordinaryCheckProbe{Cache: NewFreshCache(f.c, warden.NewMemoryCache())}
	f.engine, err = warden.NewEngine(warden.WithStore(router), warden.WithCache(f.probe))
	if err != nil {
		t.Fatal(err)
	}
	f.authz = security.NewAuthorizer(f.engine, router, nil)
	f.c.profile, err = captureEngineProfile(f.engine, router)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		if checkErr := f.engine.Stop(cleanupCtx); checkErr != nil {
			t.Error(checkErr)
		}
		if checkErr := f.c.Shutdown(cleanupCtx); checkErr != nil {
			t.Error(checkErr)
		}
	})
	if err = f.authz.EnsureTenant(ctx, b.Actor.OrgID); err != nil {
		t.Fatal(err)
	}
	role, err := f.store.GetRoleBySlug(ctx, b.Actor.OrgID, "", security.RoleDeveloper)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.CreateAssignment(ctx, &assignment.Assignment{ID: wardenid.NewAssignmentID(), TenantID: b.Actor.OrgID, SubjectKind: "user", SubjectID: b.Actor.UserID, RoleID: role.ID, GrantedBy: security.SyncGrantedBy, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	qualified, err := inspectOrdinaryConnections(ctx, main, native, schema, f.role)
	if err != nil {
		t.Fatal(err)
	}
	if qualified == nil || len(qualified.Tables) != 17 {
		t.Fatal("incomplete ordinary profile")
	}
	f.c.profile.ordinary = &ordinaryProfile{authorizer: f.authz, appID: b.AppID, requireEmail: true, migrationRole: f.role}
	return f
}

// The probe delegates caching and authority to the real engine. It observes
// the checks without changing the installed plugin set.
type ordinaryCheckProbe struct {
	warden.Cache
	before func(context.Context, string, string, *warden.CheckRequest)
	after  func(context.Context, *warden.CheckResult)
}

func (p *ordinaryCheckProbe) Get(ctx context.Context, tenant, namespace string, req *warden.CheckRequest) (*warden.CheckResult, bool) {
	if p.before != nil {
		p.before(ctx, tenant, namespace, req)
	}
	return p.Cache.Get(ctx, tenant, namespace, req)
}

func (p *ordinaryCheckProbe) Set(ctx context.Context, tenant, namespace string, req *warden.CheckRequest, result *warden.CheckResult) {
	p.Cache.Set(ctx, tenant, namespace, req, result)
	if p.after != nil {
		p.after(ctx, result)
	}
}

func (f *ordinaryPGFixture) run(t *testing.T, permission security.Permission, resource string) error {
	t.Helper()
	return f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
		d, err := a.EvaluateOrdinary(ctx, permission, resource)
		if err != nil {
			return err
		}
		ns, _ := security.ProjectNamespace(resource)
		want := ordinaryRequirementProof{permission: permission, resource: resource, namespace: ns}
		if d.kind != ordinaryDecision || d.ordinary == nil || d.ordinary.count != 1 || d.ordinary.requirements[0] != want {
			return ErrDecision
		}
		return stageOrdinaryPGCandidate(ctx, a, d, b)
	})
}

func (f *ordinaryPGFixture) runAll(t *testing.T, requirements []OrdinaryRequirement) error {
	t.Helper()
	return f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
		d, err := a.EvaluateOrdinaryAll(ctx, requirements)
		if err != nil {
			return err
		}
		return stageOrdinaryPGCandidate(ctx, a, d, b)
	})
}

func (f *ordinaryPGFixture) runCandidate(t *testing.T, work func(context.Context, *Attempt, Binding) error) error {
	t.Helper()
	b := f.binding
	b.RecordID = fmt.Sprintf("candidate-%d", time.Now().UnixNano())
	b.AttemptID = b.RecordID
	result := f.c.OrdinaryAuthority().RunOrdinary(t.Context(), b, func(ctx context.Context, a *Attempt) error {
		return work(ctx, a, b)
	})
	assertOrdinaryInstallationCandidate(t, f, b.RecordID, result)
	return result
}

func stageOrdinaryPGCandidate(ctx context.Context, a *Attempt, d *Decision, b Binding) error {
	return a.Stage(ctx, d, b, func(ctx context.Context, tx driver.Tx, at time.Time) error {
		_, err := tx.Exec(ctx, `INSERT INTO authority_candidates(id,authorized_at) VALUES($1,$2)`, b.RecordID, at)
		return err
	})
}

func TestOrdinaryPostgresInstalledProfileAndCurrentPerson(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	ctx := t.Context()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.main.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	requirements := []OrdinaryRequirement{
		{Permission: security.ProjectRead, ResourceID: f.project},
		{Permission: security.DeploymentRead, ResourceID: f.project},
	}
	t.Run("supported installed schema and ordinary proof", func(t *testing.T) {
		if err := f.run(t, security.ProjectRead, f.project); err != nil {
			t.Fatal(err)
		}
	})
	for _, tt := range []struct{ name, change, restore string }{
		{"unverified", `UPDATE authsome_users SET email_verified=false`, `UPDATE authsome_users SET email_verified=true`},
		{"banned", `UPDATE authsome_users SET banned=true`, `UPDATE authsome_users SET banned=false`},
		{"deleted", `UPDATE authsome_users SET deleted_at=now()`, `UPDATE authsome_users SET deleted_at=NULL`},
		{"failed downgrade sync", `UPDATE authsome_members SET role='viewer'`, `UPDATE authsome_members SET role='member'`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			exec(tt.change)
			defer exec(tt.restore)
			if err := f.runAll(t, requirements); err == nil {
				t.Fatal("ineligible person accepted")
			}
		})
	}
	t.Run("exact saved key and persisted scopes", func(t *testing.T) {
		kid := keyid.NewKeyID()
		scope := keyid.NewScopeID()
		old := f.binding
		defer func() { f.binding = old }()
		exec(`INSERT INTO keysmith_keys(id,tenant_id,app_id,name,prefix,hint,key_hash,created_by,metadata,expires_at) VALUES($1,$2,$3,'Saved','lp','hint','hash',$4,'{"access":"write"}',now()+interval '1 hour')`, kid.String(), old.Actor.OrgID, old.AppID, old.Actor.UserID)
		f.binding.Actor.KeyID = kid.String()
		if err := f.runAll(t, requirements); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO keysmith_scopes(id,tenant_id,app_id,name) VALUES($1,$2,$3,'test')`, scope.String(), old.Actor.OrgID, old.AppID)
		exec(`INSERT INTO keysmith_key_scopes(key_id,scope_id) VALUES($1,$2)`, kid.String(), scope.String())
		k, err := keypg.New(f.main).Keys().Get(ctx, kid)
		if err != nil {
			t.Fatal(err)
		}
		if len(k.Scopes) != 0 {
			t.Fatal("fixture did not reproduce unhydrated GetKey")
		}
		if err = f.runAll(t, requirements); err == nil {
			t.Fatal("persisted scopes accepted")
		}
		exec(`DELETE FROM keysmith_key_scopes WHERE key_id=$1`, kid.String())
		exec(`UPDATE keysmith_keys SET revoked_at=now() WHERE id=$1`, kid.String())
		if err = f.runAll(t, requirements); err == nil {
			t.Fatal("revoked saved key accepted")
		}
		exec(`UPDATE keysmith_keys SET revoked_at=NULL WHERE id=$1`, kid.String())
		for _, mutate := range []struct {
			set        string
			value, old any
		}{
			{"created_by=$2", authid.NewUserID().String(), old.Actor.UserID},
			{"metadata=$2::jsonb", `{"access":"read"}`, `{"access":"write"}`},
			{"expires_at=$2", time.Now().Add(-time.Hour), time.Now().Add(time.Hour)},
		} {
			exec(`UPDATE keysmith_keys SET `+mutate.set+` WHERE id=$1`, kid.String(), mutate.value)
			if err = f.runAll(t, requirements); !errors.Is(err, apikeys.ErrRejected) {
				t.Fatalf("ineligible key (%s) accepted: %v", mutate.set, err)
			}
			exec(`UPDATE keysmith_keys SET `+mutate.set+` WHERE id=$1`, kid.String(), mutate.old)
		}
		exec(`UPDATE authsome_users SET email_verified=false`)
		if err = f.runAll(t, requirements); err == nil {
			t.Fatal("unverified key creator accepted")
		}
		exec(`UPDATE authsome_users SET email_verified=true`)
	})
	t.Run("different current person or organization", func(t *testing.T) {
		original := f.binding
		defer func() { f.binding = original }()
		f.binding.Actor.UserID = authid.NewUserID().String()
		if err := f.runAll(t, requirements); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("different user=%v", err)
		}
		f.binding = original
		f.binding.Actor.OrgID = authid.NewOrgID().String()
		if err := f.runAll(t, requirements); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("different organization=%v", err)
		}
	})
	t.Run("generic decision forbidden", func(t *testing.T) {
		err := f.c.Run(ctx, f.binding, func(ctx context.Context, a *Attempt) error {
			_, err := a.Evaluate(ctx, func(context.Context) error { return nil })
			return err
		})
		if !errors.Is(err, ErrDecision) {
			t.Fatal(err)
		}
	})
	t.Run("finite project profile mutations", func(t *testing.T) {
		var raw []byte
		if err := f.main.QueryRow(ctx, ordinarySchemaSQL, f.schema, authorityTables[:]).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var baseline ordinarySchema
		if err := json.Unmarshal(raw, &baseline); err != nil {
			t.Fatal(err)
		}
		if err := baseline.validate(f.schema, f.role); err != nil {
			t.Fatal(err)
		}
		for _, change := range []func(*ordinarySchema){
			func(p *ordinarySchema) { p.Functions[0].Body += " " }, func(p *ordinarySchema) { p.Functions[0].Owner++ }, func(p *ordinarySchema) { p.Functions[0].Security = true }, func(p *ordinarySchema) { p.Functions[0].Config = []string{"search_path=other"} },
			func(p *ordinarySchema) { p.Triggers[0].Enabled = "D" }, func(p *ordinarySchema) { p.Triggers[0].Arguments = "ff" }, func(p *ordinarySchema) { p.Triggers[0].Type = 0 }, func(p *ordinarySchema) { p.Triggers[0].FunctionSchema = "other" }, func(p *ordinarySchema) { p.Triggers = append(p.Triggers, p.Triggers[0]) },
			func(p *ordinarySchema) { p.Indexes = nil }, func(p *ordinarySchema) { p.ForeignKeys = nil }, func(p *ordinarySchema) {
				for i := range p.ForeignKeys {
					if p.ForeignKeys[i].Table == "keysmith_key_scopes" {
						p.ForeignKeys[i].Valid = false
					}
				}
			},
		} {
			var changed ordinarySchema
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			change(&changed)
			if err := changed.validate(f.schema, f.role); err == nil {
				t.Fatal("unsupported installed schema accepted")
			}
		}
	})
	t.Run("actual cardinality and scope FK drift", func(t *testing.T) { testOrdinarySchemaCardinalityAndScopes(t, f) })
	t.Run("actual complete assignment pages", func(t *testing.T) { testOrdinaryRealPagination(t, f) })
	t.Run("team source agreement and reinvitation", func(t *testing.T) { testOrdinaryTeamAgreement(t, f) })
	t.Run("temporal interval", func(t *testing.T) { testOrdinaryTemporalInterval(t, f) })
	t.Run("independent writer fence", func(t *testing.T) { testOrdinaryWriterFence(t, f) })
	t.Run("source project reverse order", func(t *testing.T) { testOrdinaryProjectReverseOrder(t, f) })
	if err := f.runAll(t, requirements); err != nil {
		t.Fatal("later clean attempt:", err)
	}
}

func testOrdinaryProjectReverseOrder(t *testing.T, f *ordinaryPGFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	producer, err := pgx.ConnectConfig(ctx, cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), CleanupLimit)
		defer stop()
		if checkErr := producer.Close(closeCtx); checkErr != nil {
			t.Error(checkErr)
		}
	}()
	// Keep PostgreSQL deadlock detection behind the fixed authority lock limit
	// so this fixture deterministically observes the authority as the victim.
	if _, err = producer.Exec(ctx, `SET deadlock_timeout='10s'`); err != nil {
		t.Fatal(err)
	}
	var writerPID uint32
	sourceHeld := make(chan struct{})
	allowFence := make(chan struct{})
	runDone := make(chan error, 1)
	dmlDone := make(chan error, 1)
	original := f.c.source
	f.c.source = func(ctx context.Context, tx driver.Tx, b Binding) error {
		if checkErr := original(ctx, tx, b); checkErr != nil {
			return checkErr
		}
		if _, checkErr := tx.Exec(ctx, `SET LOCAL deadlock_timeout='10s'`); checkErr != nil {
			return checkErr
		}
		if checkErr := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); checkErr != nil {
			return checkErr
		}
		close(sourceHeld)
		select {
		case <-allowFence:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { f.c.source = original }()
	workReached := false
	go func() {
		runDone <- f.c.Run(ctx, f.binding, func(context.Context, *Attempt) error {
			workReached = true
			return errors.New("unexpected evaluation after reversed locks")
		})
	}()
	select {
	case <-sourceHeld:
	case checkErr := <-runDone:
		t.Fatalf("source phase failed: %v", checkErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		_, checkErr := producer.Exec(ctx, `UPDATE loomplane_projects SET name=name WHERE id=$1`, f.project)
		dmlDone <- checkErr
	}()
	queued := false
	for !queued {
		err = f.main.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)
   AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND relation=$2::regclass AND mode='RowExclusiveLock' AND granted)`, producer.PgConn().PID(), pgx.Identifier{f.schema, "loomplane_projects"}.Sanitize()).Scan(&queued)
		if err != nil {
			close(allowFence)
			t.Fatal(err)
		}
		if !queued {
			select {
			case checkErr := <-dmlDone:
				close(allowFence)
				t.Fatalf("DML did not queue in actual source trigger: %v", checkErr)
			case <-ctx.Done():
				close(allowFence)
				t.Fatal(ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	close(allowFence)
	var runErr error
	select {
	case runErr = <-runDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if runErr == nil || workReached {
		t.Fatalf("reversed schedule reached authority work: %v", runErr)
	}
	select {
	case err = <-dmlDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The authority lock limit wins; the queued producer finishes only after
	// rollback releases the source gate.
	if err != nil && !isLockScheduleError(err) {
		t.Fatal(err)
	}
	if !isLockScheduleError(runErr) {
		t.Fatalf("unexpected authority failure: %v", runErr)
	}
	var locks int
	if err = f.main.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=$1 AND (locktype='advisory' OR (relation=$2::regclass AND mode='RowExclusiveLock'))`, producer.PgConn().PID(), pgx.Identifier{f.schema, "loomplane_projects"}.Sanitize()).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("producer locks=%d err=%v", locks, err)
	}
	if err = f.main.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=$1 AND (locktype='advisory' OR mode='ShareLock')`, writerPID).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("authority locks=%d err=%v", locks, err)
	}
	f.c.source = original
	if err = f.run(t, security.ProjectRead, f.project); err != nil {
		t.Fatal("clean attempt after reverse order:", err)
	}
}

func isLockScheduleError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "40P01" || pgErr.Code == "57014"))
}

func testOrdinaryTeamAgreement(t *testing.T, f *ordinaryPGFixture) {
	ctx := t.Context()
	requirements := []OrdinaryRequirement{
		{Permission: security.DeploymentRead, ResourceID: f.project},
		{Permission: security.ProjectDelete, ResourceID: f.project},
	}
	org, user := f.binding.Actor.OrgID, f.binding.Actor.UserID
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.main.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var member string
	if err := f.main.QueryRow(ctx, `SELECT id FROM authsome_members WHERE org_id=$1 AND user_id=$2`, org, user).Scan(&member); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{"team-one", "team-two"} {
		exec(`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES($1,$2,$1,$1,$3)`, team, org, user)
		exec(`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by) VALUES($1,$2,$3,$4,$3)`, team, org, user, member)
	}
	defer exec(`DELETE FROM loomplane_teams WHERE org_id=$1`, org)
	addGrant := func(team string) {
		exec(`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by) VALUES($1,$1,$2,'admin',$3,$4)`, team, org, f.project, user)
	}
	addGrant("team-one")
	role, err := f.store.GetRoleBySlug(ctx, org, "", security.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	ns, _ := security.ProjectNamespace(f.project)
	row := &assignment.Assignment{ID: wardenid.NewAssignmentID(), TenantID: org, SubjectKind: "user", SubjectID: user, RoleID: role.ID, NamespacePath: ns, GrantedBy: security.SyncGrantedBy, Metadata: map[string]any{"sources": []string{"team-one"}}, CreatedAt: time.Now()}
	if err = f.store.CreateAssignment(ctx, row); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if checkErr := f.store.DeleteAssignment(ctx, org, row.ID); checkErr != nil {
			t.Error(checkErr)
		}
	}()
	if err = f.runAll(t, requirements); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM loomplane_team_grants WHERE id='team-one'`)
	addGrant("team-two")
	if err = f.runAll(t, requirements); err != nil {
		t.Fatal("equal-role source replacement:", err)
	}
	exec(`DELETE FROM loomplane_team_grants WHERE id='team-two'`)
	if err = f.runAll(t, requirements); err == nil {
		t.Fatal("last winning source removal accepted")
	}
	addGrant("team-two")
	exec(`UPDATE loomplane_projects SET org_id='other' WHERE id=$1`, f.project)
	if err = f.runAll(t, requirements); err == nil {
		t.Fatal("cross-workspace project grant accepted")
	}
	exec(`UPDATE loomplane_projects SET org_id=$1 WHERE id=$2`, org, f.project)
	exec(`DELETE FROM authsome_members WHERE id=$1`, member)
	if err = f.run(t, security.ProjectRead, f.project); err == nil {
		t.Fatal("removed member accepted")
	}
	exec(`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES($1,$2,$3,'member')`, authid.NewMemberID().String(), org, user)
	if err = f.runAll(t, requirements); err == nil {
		t.Fatal("reinvitation revived stale team membership")
	}
	saved, err := f.store.GetAssignment(ctx, org, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(saved.Metadata)
	if err != nil || string(data) != `{"sources":["team-one"]}` {
		t.Fatalf("historical provenance changed: %s %v", data, err)
	}
}

func testOrdinaryTemporalInterval(t *testing.T, f *ordinaryPGFixture) {
	ctx := t.Context()
	org := f.binding.Actor.OrgID
	r, err := f.store.GetRoleBySlug(ctx, org, "", security.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	boundary := start.Add(time.Second)
	end := start.Add(2 * time.Second)
	row := &assignment.Assignment{ID: wardenid.NewAssignmentID(), TenantID: org, SubjectKind: "user", SubjectID: f.binding.Actor.UserID, RoleID: r.ID, ResourceType: "project", ResourceID: f.project, GrantedBy: "manual", ExpiresAt: &boundary, CreatedAt: start}
	if err = f.store.CreateAssignment(ctx, row); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if checkErr := f.store.DeleteAssignment(ctx, org, row.ID); checkErr != nil {
			t.Error(checkErr)
		}
	}()
	deny := &policy.Policy{ID: wardenid.NewPolicyID(), TenantID: org, Name: "temporal-deny", Effect: policy.EffectDeny, IsActive: true, Actions: []string{"unrelated"}, Resources: []string{"project"}, NotAfter: &boundary}
	if err = f.store.CreatePolicy(ctx, deny); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if checkErr := f.store.DeletePolicy(ctx, org, deny.ID); checkErr != nil {
			t.Error(checkErr)
		}
	}()
	original := f.c.now
	defer func() { f.c.now = original }()
	calls := 0
	f.c.now = func() time.Time {
		calls++
		if calls == 1 {
			return start
		}
		return end
	}
	if err = f.run(t, security.ProjectRead, f.project); !errors.Is(err, ErrTemporal) {
		t.Fatalf("assignment and deny expiry crossing=%v", err)
	}
	f.c.now = original
	if err = f.run(t, security.ProjectRead, f.project); err != nil {
		t.Fatal("stable interval:", err)
	}
}

func testOrdinaryWriterFence(t *testing.T, f *ordinaryPGFixture) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := pgx.ConnectConfig(ctx, cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), CleanupLimit)
		defer cleanupCancel()
		if checkErr := writer.Close(cleanupCtx); checkErr != nil {
			t.Error(checkErr)
		}
	}()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	done := make(chan error, 1)
	writeDone := make(chan error, 1)
	b := f.binding
	b.RecordID = "writer-fence"
	b.AttemptID = b.RecordID
	go func() {
		done <- f.c.Run(ctx, b, func(ctx context.Context, a *Attempt) error {
			close(entered)
			select {
			case <-proceed:
			case <-ctx.Done():
				return ctx.Err()
			}
			d, checkErr := a.EvaluateOrdinary(ctx, security.ProjectRead, f.project)
			if checkErr != nil {
				return checkErr
			}
			return a.Stage(ctx, d, b, func(ctx context.Context, tx driver.Tx, at time.Time) error {
				_, writeErr := tx.Exec(ctx, `INSERT INTO authority_candidates(id,authorized_at)VALUES($1,$2)`, b.RecordID, at)
				return writeErr
			})
		})
	}()
	select {
	case <-entered:
	case checkErr := <-done:
		t.Fatal(checkErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		_, checkErr := writer.Exec(ctx, `UPDATE authsome_users SET banned=true WHERE id=$1`, b.Actor.UserID)
		writeDone <- checkErr
	}()
	waiting := false
	for !waiting {
		if err = f.main.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND relation=$2::regclass AND NOT granted AND mode='RowExclusiveLock')`, writer.PgConn().PID(), pgx.Identifier{f.schema, "authsome_users"}.Sanitize()).Scan(&waiting); err != nil {
			close(proceed)
			t.Fatal(err)
		}
		if !waiting {
			select {
			case checkErr := <-writeDone:
				close(proceed)
				t.Fatalf("writer bypassed fence: %v", checkErr)
			case <-ctx.Done():
				close(proceed)
				t.Fatal(ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	close(proceed)
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-writeDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, checkErr := f.main.Exec(t.Context(), `UPDATE authsome_users SET banned=false WHERE id=$1`, b.Actor.UserID); checkErr != nil {
			t.Error(checkErr)
		}
	}()
	if err = f.run(t, security.ProjectRead, f.project); err == nil {
		t.Fatal("later candidate ignored committed ban")
	}
	var count int
	if err = f.main.QueryRow(ctx, `SELECT count(*) FROM authority_candidates WHERE id=$1`, b.RecordID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("earlier candidate=%d err=%v", count, err)
	}
}

func testOrdinarySchemaCardinalityAndScopes(t *testing.T, f *ordinaryPGFixture) {
	ctx := t.Context()
	native, ok := f.c.backend.(*nativeBackend)
	if !ok {
		t.Fatal("missing native fixture")
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.main.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	index := pgx.Identifier{f.schema, "idx_authsome_members_user_org"}.Sanitize()
	exec("DROP INDEX " + index)
	duplicate := authid.NewMemberID().String()
	exec(`INSERT INTO authsome_members(id,org_id,user_id,role)VALUES($1,$2,$3,'member')`, duplicate, f.binding.Actor.OrgID, f.binding.Actor.UserID)
	if _, err := inspectOrdinaryConnections(ctx, f.main, native, f.schema, f.role); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing cardinality index=%v", err)
	}
	// The fixture's private prior profile is deliberately retained to test the
	// independent exact-one-row defense, not to qualify this drifted schema.
	if err := f.run(t, security.ProjectRead, f.project); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("duplicate membership=%v", err)
	}
	exec(`DELETE FROM authsome_members WHERE id=$1`, duplicate)
	exec("CREATE UNIQUE INDEX idx_authsome_members_user_org ON " + pgx.Identifier{f.schema, "authsome_members"}.Sanitize() + " (user_id,org_id)")
	var constraint, definition string
	if err := f.main.QueryRow(ctx, `SELECT k.conname,pg_get_constraintdef(k.oid) FROM pg_constraint k WHERE k.conrelid=$1::regclass AND k.contype='f' AND k.confrelid=$2::regclass`, pgx.Identifier{f.schema, "keysmith_key_scopes"}.Sanitize(), pgx.Identifier{f.schema, "keysmith_scopes"}.Sanitize()).Scan(&constraint, &definition); err != nil {
		t.Fatal(err)
	}
	exec("ALTER TABLE " + pgx.Identifier{f.schema, "keysmith_key_scopes"}.Sanitize() + " DROP CONSTRAINT " + pgx.Identifier{constraint}.Sanitize())
	var kid string
	if err := f.main.QueryRow(ctx, `SELECT id FROM keysmith_keys ORDER BY id LIMIT 1`).Scan(&kid); err != nil {
		t.Fatal(err)
	}
	orphan := keyid.NewScopeID().String()
	exec(`INSERT INTO keysmith_key_scopes(key_id,scope_id) VALUES($1,$2)`, kid, orphan)
	if _, err := inspectOrdinaryConnections(ctx, f.main, native, f.schema, f.role); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing scope FK=%v", err)
	}
	prior := f.binding
	f.binding.Actor.KeyID = kid
	err := f.run(t, security.ProjectRead, f.project)
	f.binding = prior
	if err == nil {
		t.Fatal("orphan persisted link accepted")
	}
	exec(`DELETE FROM keysmith_key_scopes WHERE key_id=$1 AND scope_id=$2`, kid, orphan)
	exec("ALTER TABLE " + pgx.Identifier{f.schema, "keysmith_key_scopes"}.Sanitize() + " ADD CONSTRAINT " + pgx.Identifier{constraint}.Sanitize() + " " + definition)
	if _, err := inspectOrdinaryConnections(ctx, f.main, native, f.schema, f.role); err != nil {
		t.Fatal("restored schema:", err)
	}
}

func testOrdinaryRealPagination(t *testing.T, f *ordinaryPGFixture) {
	ctx := t.Context()
	org, user := f.binding.Actor.OrgID, f.binding.Actor.UserID
	viewer, err := f.store.GetRoleBySlug(ctx, org, "", security.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 1024)
	for i := range ids {
		ids[i] = wardenid.NewAssignmentID().String()
	}
	if _, err = f.main.Exec(ctx, `INSERT INTO warden_assignments(id,tenant_id,role_id,subject_kind,subject_id,resource_type,resource_id,expires_at,granted_by,created_at)
  SELECT x,$2,$3,'user',$4,'project',x,now()+interval '1 hour','manual',clock_timestamp() FROM unnest($1::text[])x`, ids, org, viewer.ID.String(), user); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, checkErr := f.main.Exec(t.Context(), `DELETE FROM warden_assignments WHERE id=ANY($1::text[])`, ids); checkErr != nil {
			t.Error(checkErr)
		}
	}()
	if err = f.run(t, security.ProjectRead, f.project); err != nil {
		t.Fatal("all custom pages:", err)
	}
	admin, err := f.store.GetRoleBySlug(ctx, org, "", security.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	stale := &assignment.Assignment{ID: wardenid.NewAssignmentID(), TenantID: org, SubjectKind: "user", SubjectID: user, RoleID: admin.ID, GrantedBy: "legacy", CreatedAt: time.Now().Add(time.Hour)}
	if err = f.store.CreateAssignment(ctx, stale); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if checkErr := f.store.DeleteAssignment(t.Context(), org, stale.ID); checkErr != nil {
			t.Error(checkErr)
		}
	}()
	if err = f.run(t, security.ProjectRead, f.project); err == nil {
		t.Fatal("last-page broad stale role accepted")
	}
	var count int
	if err = f.main.QueryRow(ctx, `SELECT count(*) FROM warden_assignments WHERE id=ANY($1::text[])`, ids).Scan(&count); err != nil || count != len(ids) {
		t.Fatalf("custom grants changed=%d err=%v", count, err)
	}
}

func TestOrdinaryPostgresShadowAggregateCannotFabricateFacts(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	ctx := t.Context()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.main.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	step := pgx.Identifier{f.schema, "authority_shadow_step"}.Sanitize()
	aggregate := pgx.Identifier{f.schema, "jsonb_agg"}.Sanitize()
	exec("CREATE FUNCTION " + step + `(state pg_catalog.jsonb, item pg_catalog.jsonb) RETURNS pg_catalog.jsonb LANGUAGE sql IMMUTABLE AS $$
  SELECT state || pg_catalog.jsonb_build_array(item || '{"banned":false,"verified":true,"revoked":false,"scopes":false}'::pg_catalog.jsonb)
 $$`)
	exec("CREATE AGGREGATE " + aggregate + `(pg_catalog.jsonb) (SFUNC = ` + step + `, STYPE = pg_catalog.jsonb, INITCOND = '[]')`)
	native, ok := f.c.backend.(*nativeBackend)
	if !ok {
		t.Fatal("missing native fixture")
	}
	if _, err := inspectOrdinaryConnections(ctx, f.main, native, f.schema, f.role); err != nil {
		t.Fatal("profile with shadow aggregate:", err)
	}
	var fabricated []byte
	if err := f.main.QueryRow(ctx, "SELECT "+aggregate+`(pg_catalog.jsonb_build_object('banned',true,'verified',false))`).Scan(&fabricated); err != nil {
		t.Fatal(err)
	}
	var witness []struct {
		Banned   bool `json:"banned"`
		Verified bool `json:"verified"`
	}
	if err := json.Unmarshal(fabricated, &witness); err != nil {
		t.Fatal(err)
	}
	if len(witness) != 1 || witness[0].Banned || !witness[0].Verified {
		t.Fatal("shadow did not fabricate eligibility")
	}
	readFacts := func() ordinaryFacts {
		t.Helper()
		var captured ordinaryFacts
		runErr := f.c.Run(ctx, f.binding, func(ctx context.Context, a *Attempt) error {
			facts, _, err := a.readOrdinaryFacts(ctx)
			if err != nil {
				return err
			}
			captured = facts
			return ErrIncomplete // Observe facts without staging a candidate.
		})
		if !errors.Is(runErr, ErrIncomplete) {
			t.Fatalf("fact observation: %v", runErr)
		}
		return captured
	}
	exec(`UPDATE authsome_users SET banned=true,email_verified=false WHERE id=$1`, f.binding.Actor.UserID)
	person := readFacts()
	if !person.banned || person.verified {
		t.Error("application aggregate fabricated person facts")
	}
	if err := f.run(t, security.ProjectRead, f.project); err == nil {
		t.Error("banned/unverified person issued a candidate through shadow aggregate")
	}
	exec(`UPDATE authsome_users SET banned=false,email_verified=true WHERE id=$1`, f.binding.Actor.UserID)
	kid, scope := keyid.NewKeyID(), keyid.NewScopeID()
	exec(`INSERT INTO keysmith_keys(id,tenant_id,app_id,name,prefix,hint,key_hash,created_by,metadata,expires_at,revoked_at)
 VALUES($1,$2,$3,'Shadow','lp','hint','shadow-hash',$4,'{"access":"write"}',now()+interval '1 hour',now())`, kid.String(), f.binding.Actor.OrgID, f.binding.AppID, f.binding.Actor.UserID)
	exec(`INSERT INTO keysmith_scopes(id,tenant_id,app_id,name) VALUES($1,$2,$3,'shadow-scope')`, scope.String(), f.binding.Actor.OrgID, f.binding.AppID)
	exec(`INSERT INTO keysmith_key_scopes(key_id,scope_id) VALUES($1,$2)`, kid.String(), scope.String())
	f.binding.Actor.KeyID = kid.String()
	saved := readFacts()
	if !saved.keyRevoked || !saved.keyScopes {
		t.Error("application aggregate fabricated key facts")
	}
	if err := f.run(t, security.ProjectRead, f.project); err == nil {
		t.Error("revoked/scoped key issued a candidate through shadow aggregate")
	}
	// A real effective team row ensures all three fact aggregates have input.
	// Its viewer grant is redundant with the member's root developer role.
	var member string
	if err := f.main.QueryRow(ctx, `SELECT id FROM authsome_members WHERE org_id=$1 AND user_id=$2`, f.binding.Actor.OrgID, f.binding.Actor.UserID).Scan(&member); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO loomplane_teams(id,org_id,name,slug,created_by) VALUES('shadow-team',$1,'Shadow','shadow',$2)`, f.binding.Actor.OrgID, f.binding.Actor.UserID)
	exec(`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by) VALUES('shadow-team',$1,$2,$3,$2)`, f.binding.Actor.OrgID, f.binding.Actor.UserID, member)
	exec(`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,created_by) VALUES('shadow-grant','shadow-team',$1,'viewer',$2)`, f.binding.Actor.OrgID, f.binding.Actor.UserID)
	exec(`UPDATE keysmith_keys SET revoked_at=NULL WHERE id=$1`, kid.String())
	exec(`DELETE FROM keysmith_key_scopes WHERE key_id=$1`, kid.String())
	exec("CREATE OR REPLACE FUNCTION " + step + `(state pg_catalog.jsonb, item pg_catalog.jsonb) RETURNS pg_catalog.jsonb LANGUAGE plpgsql IMMUTABLE AS $$
 BEGIN RAISE EXCEPTION 'authority shadow aggregate invoked' USING ERRCODE='P0001'; END $$`)
	if _, err := inspectOrdinaryConnections(ctx, f.main, native, f.schema, f.role); err != nil {
		t.Fatal("qualified profile with sentinel:", err)
	}
	if err := f.run(t, security.ProjectRead, f.project); err != nil {
		t.Error("qualified person/key/team reads invoked shadow aggregate:", err)
	}
	// Show that the raising aggregate is still installed and callable. Success
	// above is evidence of noninvocation, not cleanup or an inert overload.
	var ignored []byte
	err := f.main.QueryRow(ctx, "SELECT "+aggregate+`(pg_catalog.jsonb_build_object('probe',true))`).Scan(&ignored)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "authority shadow aggregate invoked" {
		t.Fatalf("sentinel was not live: %v", err)
	}
	var banned, verified, revoked, scoped bool
	if err = f.main.QueryRow(ctx, `SELECT banned,email_verified FROM authsome_users WHERE id=$1`, f.binding.Actor.UserID).Scan(&banned, &verified); err != nil {
		t.Fatal(err)
	}
	if err = f.main.QueryRow(ctx, `SELECT revoked_at IS NOT NULL,EXISTS(SELECT 1 FROM keysmith_key_scopes WHERE key_id=$1) FROM keysmith_keys WHERE id=$1`, kid.String()).Scan(&revoked, &scoped); err != nil {
		t.Fatal(err)
	}
	if banned || !verified || revoked || scoped {
		t.Fatal("qualified reads changed persisted eligibility")
	}
}

type ordinaryCheckReader struct {
	store.Store
	slugs           []string
	assignmentPages int
	failResource    string
	readFailure     error
}

func (r *ordinaryCheckReader) GetRoleBySlug(ctx context.Context, tenant, namespace, slug string) (*role.Role, error) {
	r.slugs = append(r.slugs, slug)
	return r.Store.GetRoleBySlug(ctx, tenant, namespace, slug)
}

func (r *ordinaryCheckReader) ListAssignments(ctx context.Context, filter *assignment.ListFilter) ([]*assignment.Assignment, error) {
	r.assignmentPages++
	return r.Store.ListAssignments(ctx, filter)
}

func (r *ordinaryCheckReader) ListRolesForSubjectOnResource(ctx context.Context, tenant string, namespaces []string, kind, subject, resourceType, resourceID string) ([]wardenid.RoleID, error) {
	if r.readFailure != nil && resourceID == r.failResource {
		return nil, r.readFailure
	}
	return r.Store.ListRolesForSubjectOnResource(ctx, tenant, namespaces, kind, subject, resourceType, resourceID)
}

func TestOrdinaryPostgresConjunction(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	t.Run("canonical proof and one validation", func(t *testing.T) { testOrdinaryConjunctionProof(t, f) })
	t.Run("first middle and last denial with stale cache", func(t *testing.T) { testOrdinaryConjunctionDenials(t, f) })
	t.Run("error after an allow", func(t *testing.T) { testOrdinaryConjunctionReadError(t, f) })
	t.Run("one interval", func(t *testing.T) { testOrdinaryConjunctionInterval(t, f) })
	t.Run("cancellation and reader lifecycle", func(t *testing.T) { testOrdinaryConjunctionInterruption(t, f) })
	t.Run("single decision and stage", func(t *testing.T) { testOrdinaryConjunctionDecisionLifecycle(t, f) })
}

func testOrdinaryConjunctionProof(t *testing.T, f *ordinaryPGFixture) {
	ordered := []OrdinaryRequirement{
		{Permission: security.DeploymentRead, ResourceID: f.project},
		{Permission: security.ProjectRead},
		{Permission: security.ProjectRead, ResourceID: f.project},
		{Permission: security.ProjectUpdate, ResourceID: f.project},
	}
	full := make([]OrdinaryRequirement, MaxOrdinaryRequirements)
	for i := range full {
		full[i] = OrdinaryRequirement{Permission: security.ProjectRead, ResourceID: fmt.Sprintf("proj_%026d", i)}
	}
	reader, ok := f.c.backend.(*nativeBackend)
	if !ok {
		t.Fatal("missing native reader")
	}
	baseReader := f.c.reader
	baseClock := f.c.now
	t.Cleanup(func() {
		f.c.reader, f.c.now = baseReader, baseClock
		reader.reader.SetMetrics(nil)
		f.probe.before, f.probe.after = nil, nil
	})
	for _, tc := range []struct {
		name         string
		requirements []OrdinaryRequirement
	}{
		{"one exact empty resource", ordered[1:2]},
		{"one exact opaque resource", []OrdinaryRequirement{{Permission: security.ProjectRead, ResourceID: " project "}}},
		{"two resources for one permission", ordered[1:3]},
		{"two actions on one resource", ordered[2:]},
		{"permutation across resource types", ordered},
		{"maximum", full},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var previous ordinaryProof
			for _, reverse := range []bool{false, true} {
				input := slices.Clone(tc.requirements)
				if reverse {
					slices.Reverse(input)
				}
				queries := telemetrytest.NewMetrics()
				reader.reader.SetMetrics(queries)
				counts := &ordinaryCheckReader{Store: baseReader}
				f.c.reader = counts
				checks, completed, clockCalls := 0, 0, 0
				f.c.now = func() time.Time {
					clockCalls++
					return time.Now()
				}
				f.probe.before = func(ctx context.Context, tenant, ns string, req *warden.CheckRequest) {
					if ctx.Value(scopeKey{}) == nil || tenant != f.binding.Actor.OrgID ||
						req.TenantID != tenant || req.Subject.Kind != warden.SubjectUser || req.Subject.ID != f.binding.Actor.UserID {
						t.Fatalf("unbound engine check tenant=%q request=%+v", tenant, req)
					}
					if checks >= len(tc.requirements) || checks != completed {
						t.Fatalf("engine check order started=%d completed=%d", checks, completed)
					}
					want := tc.requirements[checks]
					wantNS, _ := security.ProjectNamespace(want.ResourceID)
					if req.Action.Name != want.Permission.Action || req.Resource.Type != want.Permission.Resource ||
						req.Resource.ID != want.ResourceID || req.NamespacePath != wantNS || ns != wantNS {
						t.Fatalf("check %d request=%+v namespace=%q want=%+v namespace=%q", checks, req, ns, want, wantNS)
					}
					if checks == 0 {
						clear(input) // The API has already captured the entire set.
					}
					checks++
				}
				f.probe.after = func(_ context.Context, result *warden.CheckResult) {
					if !result.Allowed {
						t.Fatal("unexpected engine denial")
					}
					completed++
				}
				err := f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
					// An inherited namespace cannot redirect a root check.
					checkCtx := warden.WithNamespace(ctx, "inherited")
					d, evalErr := a.EvaluateOrdinaryAll(checkCtx, input)
					if evalErr != nil {
						return evalErr
					}
					if d.ordinary == nil || d.ordinary.count != len(tc.requirements) {
						t.Fatalf("decision proof=%+v", d.ordinary)
					}
					for i, want := range tc.requirements {
						ns, _ := security.ProjectNamespace(want.ResourceID)
						if got := d.ordinary.requirements[i]; got != (ordinaryRequirementProof{permission: want.Permission, resource: want.ResourceID, namespace: ns}) {
							t.Fatalf("proof %d=%+v", i, got)
						}
					}
					if reverse && previous != *d.ordinary {
						t.Fatal("permuted input changed the proof")
					}
					previous = *d.ordinary
					return stageOrdinaryPGCandidate(ctx, a, d, b)
				})
				if err != nil {
					t.Fatal(err)
				}
				if checks != len(tc.requirements) || completed != checks || clockCalls != 2 {
					t.Fatalf("started=%d completed=%d clock reads=%d", checks, completed, clockCalls)
				}
				if !slices.Equal(counts.slugs, []string{security.RoleOwner, security.RoleAdmin, security.RoleDeveloper, security.RoleViewer}) ||
					counts.assignmentPages != 1 {
					t.Fatalf("catalog=%v assignment pages=%d", counts.slugs, counts.assignmentPages)
				}
				facts := make(map[string]uint64)
				for _, series := range queries.Series(observability.OperationDurationMetric) {
					facts[series.Labels["db.collection.name"]] += series.Count
				}
				if facts["authsome_users"] != 1 || facts["loomplane_team_members"] != 1 {
					t.Fatalf("person/team reads=%v", facts)
				}
			}
		})
	}
	t.Run("singleton exact root resources", func(t *testing.T) {
		f.probe.after = nil
		f.c.reader, f.c.now = baseReader, baseClock
		for _, resource := range []string{"", " project "} {
			checks := 0
			f.probe.before = func(_ context.Context, _, ns string, req *warden.CheckRequest) {
				checks++
				if req.Resource.ID != resource || req.NamespacePath != "" || ns != "" {
					t.Fatalf("singleton resource=%q resolved namespace=%q request=%+v", resource, ns, req)
				}
			}
			err := f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
				d, err := a.EvaluateOrdinary(warden.WithNamespace(ctx, "inherited"), security.ProjectRead, resource)
				if err != nil {
					return err
				}
				want := ordinaryRequirementProof{permission: security.ProjectRead, resource: resource}
				if d.ordinary == nil || d.ordinary.count != 1 || d.ordinary.requirements[0] != want {
					t.Fatalf("singleton proof=%+v", d.ordinary)
				}
				return stageOrdinaryPGCandidate(ctx, a, d, b)
			})
			if err != nil || checks != 1 {
				t.Fatalf("singleton resource=%q checks=%d err=%v", resource, checks, err)
			}
		}
	})
}

func testOrdinaryConjunctionDenials(t *testing.T, f *ordinaryPGFixture) {
	ctx := t.Context()
	org := f.binding.Actor.OrgID
	admin, err := f.store.GetRoleBySlug(ctx, org, "", security.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	requirements := make([]OrdinaryRequirement, 3)
	grants := make([]assignment.Assignment, len(requirements))
	for i := range requirements {
		resource := fmt.Sprintf("proj_%026d", i+1)
		requirements[i] = OrdinaryRequirement{Permission: security.ProjectDelete, ResourceID: resource}
		ns, _ := security.ProjectNamespace(resource)
		grants[i] = assignment.Assignment{
			ID: wardenid.NewAssignmentID(), TenantID: org, SubjectKind: "user", SubjectID: f.binding.Actor.UserID,
			RoleID: admin.ID, NamespacePath: ns, ResourceType: "project", ResourceID: resource, GrantedBy: "manual", CreatedAt: time.Now(),
		}
		if err := f.store.CreateAssignment(ctx, &grants[i]); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := f.store.DeleteAssignment(context.Background(), org, grants[i].ID); err != nil {
				t.Error(err)
			}
		})
	}
	if err := f.runAll(t, requirements); err != nil {
		t.Fatal(err)
	}
	for i, req := range requirements {
		t.Run(fmt.Sprintf("denial at %d", i+1), func(t *testing.T) {
			// The ordinary check populates the cache. Removing the row directly
			// preserves the stale cached answer while the conjunction reads fresh.
			if err := f.authz.Require(ctx, f.binding.Actor, req.Permission, req.ResourceID); err != nil {
				t.Fatal(err)
			}
			if err := f.store.DeleteAssignment(ctx, org, grants[i].ID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := f.store.CreateAssignment(context.Background(), &grants[i]); err != nil {
					t.Error(err)
				}
			})
			if err := f.authz.Require(ctx, f.binding.Actor, req.Permission, req.ResourceID); err != nil {
				t.Fatalf("ordinary permission was not cached: %v", err)
			}
			var allowed []bool
			f.probe.after = func(_ context.Context, result *warden.CheckResult) {
				allowed = append(allowed, result.Allowed)
			}
			defer func() { f.probe.after = nil }()
			if err := f.runAll(t, requirements); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("denied requirement=%v", err)
			}
			if len(allowed) != i+1 {
				t.Fatalf("checks continued after denial: %v", allowed)
			}
			for j, ok := range allowed {
				if ok != (j < i) {
					t.Fatalf("conjunction outcomes=%v", allowed)
				}
			}
		})
	}
	t.Run("owner-only requirement after an allow", func(t *testing.T) {
		err := f.runAll(t, []OrdinaryRequirement{
			{Permission: security.BackupRetire, ResourceID: f.project},
			{Permission: security.AnalysisRun, ResourceID: f.project},
		})
		if !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("non-owner backup retirement=%v", err)
		}
	})
	if err := f.runAll(t, requirements); err != nil {
		t.Fatalf("later clean attempt: %v", err)
	}
}

func testOrdinaryConjunctionReadError(t *testing.T, f *ordinaryPGFixture) {
	original := f.c.reader
	failure := errors.New("second requirement backend read failed")
	f.c.reader = &ordinaryCheckReader{Store: original, failResource: f.project, readFailure: failure}
	t.Cleanup(func() {
		f.c.reader = original
		f.probe.after = nil
	})
	allowed := 0
	f.probe.after = func(_ context.Context, result *warden.CheckResult) {
		if result.Allowed {
			allowed++
		}
	}
	err := f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
		d, evalErr := a.EvaluateOrdinaryAll(ctx, []OrdinaryRequirement{
			{Permission: security.ProjectRead, ResourceID: f.project},
			{Permission: security.ProjectRead},
		})
		if d != nil || !errors.Is(evalErr, failure) {
			t.Fatalf("backend failure decision=%v err=%v", d, evalErr)
		}
		// Even a caller that ignores the evaluation error cannot stage a write.
		return a.Stage(ctx, d, b, func(context.Context, driver.Tx, time.Time) error {
			t.Fatal("stage ran after a failed conjunction")
			return nil
		})
	})
	if allowed != 1 || !errors.Is(err, failure) || !errors.Is(err, ErrPoisoned) {
		t.Fatalf("earlier allows=%d run=%v", allowed, err)
	}
}

func testOrdinaryConjunctionInterval(t *testing.T, f *ordinaryPGFixture) {
	requirements := []OrdinaryRequirement{
		{Permission: security.DeploymentRead, ResourceID: f.project},
		{Permission: security.ProjectRead, ResourceID: f.project},
	}
	originalClock := f.c.now
	originalBinding := f.binding
	for _, tc := range []struct {
		name string
		want error
	}{
		{"persisted boundary between checks", ErrTemporal},
		{"clock reversal between checks", ErrTemporal},
		{"key expires at the decision instant", apikeys.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				f.c.now, f.binding = originalClock, originalBinding
				f.probe.after = nil
			})
			start := time.Now().UTC().Truncate(time.Microsecond)
			boundary, end := start.Add(time.Second), start.Add(2*time.Second)
			switch tc.name {
			case "persisted boundary between checks":
				org := f.binding.Actor.OrgID
				p := &policy.Policy{
					ID: wardenid.NewPolicyID(), TenantID: org, Name: "unrelated temporal deny",
					Effect: policy.EffectDeny, IsActive: true, Actions: []string{"unrelated"}, Resources: []string{"project"},
					NotAfter: &boundary,
				}
				if err := f.store.CreatePolicy(t.Context(), p); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := f.store.DeletePolicy(context.Background(), org, p.ID); err != nil {
						t.Error(err)
					}
				})
			case "clock reversal between checks":
				end = start.Add(-time.Second)
			case "key expires at the decision instant":
				id := keyid.NewKeyID().String()
				if _, err := f.main.Exec(t.Context(), `INSERT INTO keysmith_keys(id,tenant_id,app_id,name,prefix,hint,key_hash,created_by,metadata,expires_at) VALUES($1,$2,$3,'Temporal','lp','hint',$1,$4,'{"access":"write"}',$5)`,
					id, f.binding.Actor.OrgID, f.binding.AppID, f.binding.Actor.UserID, end); err != nil {
					t.Fatal(err)
				}
				f.binding.Actor.KeyID = id
				t.Cleanup(func() {
					if _, err := f.main.Exec(context.Background(), `DELETE FROM keysmith_keys WHERE id=$1`, id); err != nil {
						t.Error(err)
					}
				})
			}
			clockReads, allowed := 0, 0
			now := start
			f.c.now = func() time.Time {
				clockReads++
				return now
			}
			f.probe.after = func(_ context.Context, result *warden.CheckResult) {
				if !result.Allowed {
					t.Fatal("permission check denied before interval validation")
				}
				allowed++
				now = end
			}
			if err := f.runAll(t, requirements); !errors.Is(err, tc.want) || allowed != 2 || clockReads != 2 {
				t.Fatalf("interval result=%v allows=%d clock reads=%d", err, allowed, clockReads)
			}
		})
	}
	if err := f.runAll(t, requirements); err != nil {
		t.Fatalf("stable interval: %v", err)
	}
}

func testOrdinaryConjunctionInterruption(t *testing.T, f *ordinaryPGFixture) {
	requirements := []OrdinaryRequirement{
		{Permission: security.DeploymentRead, ResourceID: f.project},
		{Permission: security.ProjectRead, ResourceID: f.project},
	}
	for _, duringSecond := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel during second=%v", duringSecond), func(t *testing.T) {
			defer func() { f.probe.before, f.probe.after = nil, nil }()
			checks := 0
			err := f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
				checkCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				f.probe.before = func(context.Context, string, string, *warden.CheckRequest) {
					checks++
					if checks == 2 && duringSecond {
						cancel()
					}
				}
				f.probe.after = func(_ context.Context, result *warden.CheckResult) {
					if result.Allowed && checks == 1 && !duringSecond {
						cancel()
					}
				}
				d, err := a.EvaluateOrdinaryAll(checkCtx, requirements)
				if err != nil {
					return err
				}
				return stageOrdinaryPGCandidate(ctx, a, d, b)
			})
			wantChecks := 1
			if duringSecond {
				wantChecks = 2
			}
			if !errors.Is(err, context.Canceled) || checks != wantChecks {
				t.Fatalf("canceled conjunction=%v engine checks=%d", err, checks)
			}
		})
	}
	t.Run("engine profile changes during second check", func(t *testing.T) {
		config := f.engine.Config()
		rbac := *config.EnableRBAC
		defer func() {
			*config.EnableRBAC = rbac
			f.probe.before = nil
		}()
		checks := 0
		f.probe.before = func(context.Context, string, string, *warden.CheckRequest) {
			checks++
			if checks == 2 {
				*config.EnableRBAC = false
			}
		}
		if err := f.runAll(t, requirements); !errors.Is(err, ErrUnavailable) || checks != 2 {
			t.Fatalf("profile mutation result=%v checks=%d", err, checks)
		}
	})
	t.Run("pending reader during final validation", func(t *testing.T) {
		router, ok := f.engine.Store().(*storeRouter)
		if !ok {
			t.Fatal("missing store router")
		}
		entered := make(chan struct{})
		done := make(chan error, 1)
		original := f.c.reader
		f.c.reader = pendingReadStore{Store: original, entered: entered}
		defer func() {
			f.c.reader = original
			f.probe.after = nil
		}()
		checks := 0
		f.probe.after = func(ctx context.Context, _ *warden.CheckResult) {
			checks++
			if checks == len(requirements) {
				go func() { done <- router.Ping(ctx) }()
				select {
				case <-entered:
				case err := <-done:
					t.Fatalf("reader did not enter: %v", err)
				case <-ctx.Done():
					t.Fatal("reader did not enter before attempt deadline")
				}
			}
		}
		if err := f.runAll(t, requirements); !errors.Is(err, ErrPending) || checks != 2 {
			t.Fatalf("pending reader result=%v checks=%d", err, checks)
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("pending reader was not canceled: %v", err)
		}
	})
	if err := f.runAll(t, requirements); err != nil {
		t.Fatalf("later clean attempt: %v", err)
	}
}

func testOrdinaryConjunctionDecisionLifecycle(t *testing.T, f *ordinaryPGFixture) {
	requirements := []OrdinaryRequirement{
		{Permission: security.DeploymentRead, ResourceID: f.project},
		{Permission: security.ProjectRead, ResourceID: f.project},
	}
	for _, tc := range []struct {
		name string
		want error
	}{
		{"second conjunction", ErrDecision},
		{"singleton then conjunction", ErrDecision},
		{"conjunction then singleton", ErrDecision},
		{"wrong durable binding", ErrBinding},
		{"repeat stage rolls back first stage", ErrDecision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
				if tc.name == "singleton then conjunction" {
					if _, err := a.EvaluateOrdinary(ctx, requirements[0].Permission, requirements[0].ResourceID); err != nil {
						return err
					}
					_, err := a.EvaluateOrdinaryAll(ctx, requirements)
					return err
				}
				d, err := a.EvaluateOrdinaryAll(ctx, requirements)
				if err != nil {
					return err
				}
				switch tc.name {
				case "second conjunction":
					_, err = a.EvaluateOrdinaryAll(ctx, requirements)
					return err
				case "conjunction then singleton":
					_, err = a.EvaluateOrdinary(ctx, requirements[0].Permission, requirements[0].ResourceID)
					return err
				case "wrong durable binding":
					b.InputDigest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
				case "repeat stage rolls back first stage":
					if err := stageOrdinaryPGCandidate(ctx, a, d, b); err != nil {
						return err
					}
				}
				return a.Stage(ctx, d, b, func(context.Context, driver.Tx, time.Time) error {
					t.Fatal("invalid second stage or binding reached the write callback")
					return nil
				})
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("decision lifecycle=%v", err)
			}
		})
	}
	var savedContext context.Context
	var savedAttempt *Attempt
	var savedDecision *Decision
	if err := f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
		d, err := a.EvaluateOrdinaryAll(ctx, requirements)
		if err != nil {
			return err
		}
		savedContext, savedAttempt, savedDecision = ctx, a, d
		return stageOrdinaryPGCandidate(ctx, a, d, b)
	}); err != nil {
		t.Fatal(err)
	}
	if d, err := savedAttempt.EvaluateOrdinaryAll(savedContext, requirements); d != nil || !errors.Is(err, ErrExpired) {
		t.Fatalf("retained attempt decision=%v err=%v", d, err)
	}
	if err := f.runCandidate(t, func(ctx context.Context, a *Attempt, b Binding) error {
		if _, err := a.EvaluateOrdinaryAll(ctx, requirements); err != nil {
			return err
		}
		return a.Stage(ctx, savedDecision, b, func(context.Context, driver.Tx, time.Time) error {
			t.Fatal("foreign attempt staged a decision")
			return nil
		})
	}); !errors.Is(err, ErrDecision) {
		t.Fatalf("foreign decision: %v", err)
	}
	if err := f.runAll(t, requirements); err != nil {
		t.Fatalf("later clean attempt: %v", err)
	}
}
