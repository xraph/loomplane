package cloudinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/grove"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"
	"github.com/xraph/grove/observability"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/warden"
	wardenpg "github.com/xraph/warden/store/postgres"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
	"github.com/xraph/loomplane/pkg/provider"
)

type namespaceFixtureAuth struct{}

func (namespaceFixtureAuth) Require(_ context.Context, p security.Principal, _ security.Permission, _ string) error {
	if p.OrgID != "org" || p.UserID != "usr" || p.KeyID != "" || p.ReadOnly {
		return security.ErrForbidden
	}
	return nil
}
func (a namespaceFixtureAuth) RequireCurrentMembership(ctx context.Context, p security.Principal) error {
	return a.Require(ctx, p, security.Permission{}, "")
}

type namespacePGFixture struct {
	proxy        *namespaceCommitProxy
	db           *pgdriver.PgDB
	c            *currentauthority.Coordinator
	transactions *InstanceNamespaceTransactions
	action       namespaceAction
	dsn, schema  string
}

func newNamespacePGFixture(t *testing.T, fault ...string) *namespacePGFixture {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for isolated PostgreSQL 55471")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Port != 55471 || (cfg.Host != "localhost" && cfg.Host != "127.0.0.1") {
		t.Fatal("requires isolated PostgreSQL 55471")
	}
	schema := fmt.Sprintf("fleet_namespace_%d", time.Now().UnixNano())
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("fixture requires PostgreSQL URL")
	}
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("pool_max_conns", "1")
	query.Set("pool_min_conns", "0")
	query.Set("pool_min_idle_conns", "0")
	u.RawQuery = query.Encode()
	dsn = u.String()
	installDSN, mainDSN := dsn, dsn
	var proxy *namespaceCommitProxy
	if len(fault) > 0 && fault[0] == "receipt-commit" {
		proxy, mainDSN = newNamespaceCommitProxy(t, dsn)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	db := pgdriver.New()
	if err = db.Open(ctx, mainDSN, driver.WithPoolSize(1)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if dbCloseErr := db.Close(); dbCloseErr != nil {
			t.Error(dbCloseErr)
		}
	})
	if _, err = db.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		schemaCleanupCtx, schemaCleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer schemaCleanupCancel()
		if _, schemaDropErr := db.Exec(schemaCleanupCtx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); schemaDropErr != nil {
			t.Error(schemaDropErr)
		}
	})
	if _, err = migrate.NewOrchestrator(pgmigrate.New(db), migrations.All()...).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, execErr := db.Exec(ctx, q, args...); execErr != nil {
			t.Fatal(execErr)
		}
	}
	exec(`INSERT INTO authsome_apps(id,name,slug) VALUES('app','Fleet','fleet')`)
	exec(`INSERT INTO authsome_environments(id,app_id,name,slug,type) VALUES('authenv','app','Prod','prod','production')`)
	exec(`INSERT INTO authsome_users(id,app_id,env_id,email,email_verified) VALUES('usr','app','authenv','fleet@example.invalid',true)`)
	exec(`INSERT INTO authsome_organizations(id,app_id,env_id,name,slug,created_by) VALUES('org','app','authenv','Workspace','workspace','usr')`)
	exec(`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj','org','Project','project','ready','usr',now(),now())`)
	now := time.Now().UTC()
	clouds := cloudprofiles.NewPgStore(db)
	if err = clouds.CreateProfile(ctx, &cloudprofiles.Profile{ID: "cloud", OrgID: "org", ProjectID: "proj", Name: "Cloud", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	fstore := fleets.NewPgStore(db)
	service := fleets.NewService(fleets.Deps{Store: fstore, Projects: projects.NewPgStore(db), Authz: namespaceFixtureAuth{}})
	owner := security.Principal{OrgID: "org", UserID: "usr"}
	f, err := service.CreateFleet(ctx, owner, "proj")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := service.CreateStage(ctx, owner, f.ID, fleet.CreateStageInput{Name: "production", EnvironmentKind: "production", AllowedCloudIDs: []string{"cloud"}, MaxInstances: 10})
	if err != nil {
		t.Fatal(err)
	}
	release, err := service.PublishRelease(ctx, owner, f.ID, []byte(`schema_version: 1
manifest:
  version: 1
  services:
    api:
      kind: http-service
      build: {image: registry.example.test/api@sha256:`+strings.Repeat("a", 64)+`}
profiles:
  small:
    isolation: namespace
    services:
      api: {cpu_millis: 500, memory_mib: 256, min_replicas: 1, max_replicas: 2}
`))
	if err != nil {
		t.Fatal(err)
	}
	scope := fleets.FleetScope{OrgID: "org", ProjectID: "proj", FleetID: f.ID}
	create := fleet.CreateInstanceInput{StageID: stage.ID, ReleaseID: release.ID, ProfileID: "small", ExternalRef: "instance-a", IdempotencyKey: "create-a"}
	instance, err := fstore.ReserveInstance(ctx, scope, create)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fleets.RenderInstance(*release, instance.Instance, create)
	if err != nil {
		t.Fatal(err)
	}
	key := keyid.NewKeyID().String()
	actor := security.Principal{OrgID: "org", KeyID: key, UserID: security.FleetSubject(key), Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "proj", FleetID: f.ID}}
	metadata, err := json.Marshal(map[string]string{"access": "write", "kind": "fleet", "project_id": "proj", "fleet_id": f.ID})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO keysmith_keys(id,tenant_id,app_id,name,prefix,hint,key_hash,created_by,metadata,expires_at) VALUES($1,'org','app','Fleet','lp','hint','hash','usr',$2,$3)`, key, metadata, now.Add(time.Hour))
	grant, err := service.CreateGrant(ctx, owner, f.ID, fleet.CreateGrantInput{CredentialID: key, StageID: stage.ID, ReleaseIDs: []string{release.ID}, ProfileIDs: []string{"small"}, CloudIDs: []string{"cloud"}, Regions: []string{"region"}, Actions: []string{"create"}, ExpiresAt: now.Add(time.Hour), Limits: fleet.GrantLimits{MaxInstances: 10, CPUMillis: 2000, MemoryMiB: 1024, StorageGiB: 10, HourlyCostMicros: 10000, Currency: "USD"}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := fstore.ReserveAuthority(ctx, &fleets.OperationAuthority{ID: "authority", Scope: scope, Principal: actor, GrantRevision: grant.Revision, StageRevision: stage.PolicyRevision, Request: fleets.OperationRequest{GrantID: grant.ID, Kind: "create", StageID: stage.ID, ReleaseID: release.ID, ProfileID: "small", CloudID: "cloud", Region: "region", ExternalRef: create.ExternalRef, IdempotencyKey: create.IdempotencyKey, SnapshotDigest: snapshot.Digest, Demand: fleets.OperationDemand{CPUMillis: 1000, MemoryMiB: 512, StorageGiB: 1, HourlyCostMicros: 1000, Currency: "USD", PriceKnown: true}}, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	conn := &connections.Connection{ID: "infra", OrgID: "org", Cloud: "digitalocean", Name: "Account", Account: "account", Status: connections.StatusReady, Generation: 1, Details: map[string]string{}, CreatedAt: now, UpdatedAt: now}
	if err = connections.NewPgStore(db).CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	resource := ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ConnectionID: "infra", ExternalID: "remote", Incarnation: "physical", Ownership: "external", Revision: 1, Status: "running", Observation: &provider.ClusterObservation{Ref: provider.ClusterRef{ID: "remote", AccountID: "account", Region: "region", Incarnation: "physical"}, State: "running", CAHash: "ca", Endpoint: "https://cluster.example.invalid"}}
	raw, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO loomplane_managed_resources(id,org_id,cloud_id,kind,provider,account_id,connection_id,external_id,incarnation,ownership,status,revision,body) VALUES('cluster','org','cloud','cluster','digitalocean','account','infra','remote','physical','external','running',1,$1)`, raw)
	var projectGrant int64
	if err = db.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_project_grants WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`).Scan(&projectGrant); err != nil {
		t.Fatal(err)
	}
	pins := InstanceNamespacePins{FleetID: f.ID, InstanceID: snapshot.InstanceID, EnvironmentID: snapshot.EnvironmentID, Generation: snapshot.Generation, OwnershipEpoch: snapshot.OwnershipEpoch, SnapshotDigest: snapshot.Digest, QualificationID: "fixture-qualification"}
	id, namespace := instanceNamespaceIdentity("org", snapshot.InstanceID, snapshot.OwnershipEpoch)
	record := InstanceNamespaceRecord{OrgID: "org", ProjectID: "proj", CloudID: "cloud", ClusterID: "cluster", OperationID: id, Namespace: namespace, Pins: pins, CreationAuthority: *authority, CurrentAuthority: *authority, CurrentGeneration: snapshot.Generation, CurrentSnapshotDigest: snapshot.Digest, Snapshot: *snapshot, QualificationExpiresAt: now.Add(time.Hour)}
	input := ClusterInput{Kind: InstanceNamespaceKind, Provider: "digitalocean", ConnectionID: "infra", AccountID: "account", ResourceID: "cluster", ResourceRevision: 1, ConnectionGeneration: 1, CloudRevision: 1, Ref: resource.Observation.Ref, Attachment: &AttachmentInput{Instance: &pins, InstancePolicy: &provider.InstanceIsolationPolicy{Revision: provider.InstanceIsolationRevision, PodSecurityVersion: "v1.35", CPUMillis: 1000, MemoryMiB: 512, EphemeralMiB: 128, MaxPods: 2}, TargetSlug: namespace, CloudRevision: 1, ResourceRevision: 1, GrantRevision: projectGrant, TrustFingerprint: "ca", EndpointFingerprint: endpointFingerprint(resource.Observation.Endpoint), ProjectID: "proj", Namespace: namespace, ServiceAccountName: "runtime"}}
	operation := ClusterOperation{ID: id, OrgID: "org", CloudID: "cloud", ResourceID: "cluster", Kind: InstanceNamespaceKind, SchemaVersion: 1, Input: input, Digest: clusterDigest(input), Revision: 1, Status: "accepted", Principal: actor, Attempt: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: authority.ExpiresAt}
	operation.Steps = []ClusterStep{{Name: "resolve-cluster", State: "completed", CompletedAt: &now, RequestDigest: operation.Digest}, {Name: "enroll-runtime", State: "pending", RequestDigest: operation.Digest}}
	groveDB, err := grove.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	c := &currentauthority.Coordinator{}
	router := currentauthority.NewStoreRouter(c, wardenpg.New(groveDB))
	engine, err := warden.NewEngine(warden.WithStore(router), warden.WithCache(currentauthority.NewFreshCache(c, warden.NewMemoryCache())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if shutdownErr := c.Shutdown(shutdownCtx); shutdownErr != nil {
			t.Error(shutdownErr)
			return
		}
		if stopErr := engine.Stop(shutdownCtx); stopErr != nil {
			t.Error(stopErr)
		}
	})
	if len(fault) > 0 && fault[0] == "namespace-commit" {
		proxy, installDSN = newNamespaceCommitProxy(t, dsn)
	}
	if err = currentauthority.InstallBuiltInFleetProfile(ctx, c, db, engine, installDSN, "app", observability.NewTelemetry("postgresql"), nil); err != nil {
		t.Fatal(err)
	}
	transactions := NewInstanceNamespaceTransactions(c.FleetAuthority(), service, NewPgStore(db), "app")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := transactions.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return &namespacePGFixture{proxy: proxy, db: db, c: c, transactions: transactions, action: namespaceAction{Kind: "reserve", AttemptID: "reserve-attempt", AppID: "app", Record: record, Snapshot: *snapshot, Operation: operation, Principal: actor}, dsn: dsn, schema: schema}
}

func TestNamespacePostgresReservationAndExactReconciliation(t *testing.T) {
	f := newNamespacePGFixture(t)
	result, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.Revision != 1 || result.Operation.Revision != 1 || result.Record.CreationAttemptID != f.action.AttemptID {
		t.Fatal("lost creation evidence")
	}
	frozen, digest, err := freezeNamespaceAction(f.action)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.transactions.run(t.Context(), f.action)
	if err != nil || !namespaceEqual(retry, result) {
		t.Fatalf("same attempt replay: %v", err)
	}
	replay, err := f.transactions.store.reconcileNamespaceAttempt(t.Context(), f.schema, frozen, digest)
	if err != nil || !namespaceEqual(replay, result) {
		t.Fatalf("historical reconciliation: %v", err)
	}
	frozen.ExpectedOperationRevision++
	if _, err = f.transactions.store.reconcileNamespaceAttempt(t.Context(), f.schema, frozen, digest); err == nil {
		t.Fatal("reconciled wrong revision")
	}
	current, err := f.transactions.store.GetInstanceNamespaceRecord(t.Context(), "org", result.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := result.Operation
	next.Revision++
	if err = f.transactions.Checkpoint(t.Context(), current, &next, result.Operation.Revision); err != nil {
		t.Fatal(err)
	}
	current, err = f.transactions.store.GetInstanceNamespaceRecord(t.Context(), "org", result.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != 2 || current.CreationAttemptID != f.action.AttemptID || current.CurrentAttemptID == current.CreationAttemptID {
		t.Fatal("checkpoint replaced creation or lost current attempt")
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_instances SET ownership_epoch=ownership_epoch+1 WHERE id=$1`, current.Pins.InstanceID); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	if err = f.transactions.Checkpoint(t.Context(), current, &next, next.Revision-1); err == nil {
		t.Fatal("stale epoch checkpoint accepted")
	}
}

func namespaceFixtureConnection(t *testing.T, f *namespacePGFixture) *pgx.Conn {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(t.Context(), cfg.ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := conn.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return conn
}

func namespaceAwaitBlocked(t *testing.T, observer *pgx.Conn, pid uint32) {
	t.Helper()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := observer.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected writer did not reach the held lock")
}

func TestNamespacePostgresRevocationBeforeAdmission(t *testing.T) {
	for _, kind := range []string{"grant", "key", "stage"} {
		t.Run(kind, func(t *testing.T) {
			f := newNamespacePGFixture(t)
			writer := namespaceFixtureConnection(t, f)
			observer := namespaceFixtureConnection(t, f)
			tx, err := writer.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			a := f.action.Record.CurrentAuthority
			switch kind {
			case "grant":
				_, err = tx.Exec(t.Context(), `UPDATE loomplane_fleet_grants SET status='revoked',revision=revision+1 WHERE id=$1`, a.Request.GrantID)
			case "key":
				_, err = tx.Exec(t.Context(), `UPDATE keysmith_keys SET revoked_at=now() WHERE id=$1`, a.Principal.KeyID)
			case "stage":
				_, err = tx.Exec(t.Context(), `UPDATE loomplane_fleet_stages SET policy_revision=policy_revision+1 WHERE id=$1`, a.Request.StageID)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, runErr := f.transactions.run(t.Context(), f.action); done <- runErr }()
			namespaceAwaitBlocked(t, observer, writer.PgConn().PID())
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("revoked authority admitted")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("admission did not finish")
			}
			var rows int
			if err = f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_fleet_namespaces)+(SELECT count(*) FROM loomplane_fleet_namespace_attempts)+(SELECT count(*) FROM loomplane_cluster_operations)`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("revoked attempt left writes: %d %v", rows, err)
			}
		})
	}
}

func TestNamespacePostgresAdmissionBeforeRevocation(t *testing.T) {
	for _, kind := range []string{"grant", "key", "stage"} {
		t.Run(kind, func(t *testing.T) {
			f := newNamespacePGFixture(t)
			blocker := namespaceFixtureConnection(t, f)
			observer := namespaceFixtureConnection(t, f)
			writer := namespaceFixtureConnection(t, f)
			hold, err := blocker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = hold.Rollback(context.Background()) }()
			if kind == "key" {
				_, err = hold.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('cloud-organization/org',0))`)
			} else {
				_, err = hold.Exec(t.Context(), `SELECT id FROM loomplane_environments WHERE id=$1 FOR UPDATE`, f.action.Record.Pins.EnvironmentID)
			}
			if err != nil {
				t.Fatal(err)
			}
			admitted := make(chan error, 1)
			go func() { _, admissionErr := f.transactions.run(t.Context(), f.action); admitted <- admissionErr }()
			namespaceAwaitBlocked(t, observer, blocker.PgConn().PID())
			revoked := make(chan error, 1)
			a := f.action.Record.CurrentAuthority
			go func() {
				var revokeErr error
				switch kind {
				case "grant":
					_, revokeErr = writer.Exec(t.Context(), `UPDATE loomplane_fleet_grants SET status='revoked',revision=revision+1 WHERE id=$1`, a.Request.GrantID)
				case "key":
					_, revokeErr = writer.Exec(t.Context(), `UPDATE keysmith_keys SET revoked_at=now() WHERE id=$1`, a.Principal.KeyID)
				case "stage":
					_, revokeErr = writer.Exec(t.Context(), `UPDATE loomplane_fleet_stages SET policy_revision=policy_revision+1 WHERE id=$1`, a.Request.StageID)
				}
				revoked <- revokeErr
			}()
			// Observe the revoker itself waiting before releasing the namespace attempt.
			deadline := time.Now().Add(time.Second)
			waiting := false
			for time.Now().Before(deadline) {
				if err = observer.QueryRow(t.Context(), `SELECT cardinality(pg_blocking_pids($1))>0`, writer.PgConn().PID()).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("revocation bypassed the acquired authority locks")
			}
			if err = hold.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-admitted:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("admission did not finish")
			}
			select {
			case err = <-revoked:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("revocation did not drain")
			}
			var rows int
			if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("missing admitted effect evidence: %d %v", rows, err)
			}
		})
	}
}

func TestNamespacePostgresCompetingRevisionAndRollback(t *testing.T) {
	f := newNamespacePGFixture(t)
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	action := f.action
	action.Kind = "checkpoint"
	action.Record = first.Record
	action.ExpectedNamespaceRevision = 1
	action.ExpectedOperationRevision = 1
	action.Operation = first.Operation
	action.Operation.Revision = 2
	results := make(chan error, 2)
	for _, id := range []string{"checkpoint-a", "checkpoint-b"} {
		next := action
		next.AttemptID = id
		go func() { _, competingRunErr := f.transactions.run(t.Context(), next); results <- competingRunErr }()
	}
	successes := 0
	for range 2 {
		select {
		case resultErr := <-results:
			if resultErr == nil {
				successes++
			}
		case <-time.After(7 * time.Second):
			t.Fatal("competing revision did not finish")
		}
	}
	if successes != 1 {
		t.Fatalf("expected one checked write, got %d", successes)
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_fleet_namespace_attempts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("wrong journal count: %d %v", count, err)
	}
}

func TestNamespacePostgresFailureRollsBackEveryWrite(t *testing.T) {
	f := newNamespacePGFixture(t)
	action := f.action
	action.Operation.Steps[1].RequestDigest = "different"
	if _, err := f.transactions.run(t.Context(), action); err == nil {
		t.Fatal("invalid persistence input accepted")
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM loomplane_cluster_operations)+(SELECT count(*) FROM loomplane_cluster_operation_steps)+(SELECT count(*) FROM loomplane_fleet_namespaces)+(SELECT count(*) FROM loomplane_fleet_namespace_attempts)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial writes escaped rollback: %d %v", count, err)
	}
}

func namespaceCloneOperation(t *testing.T, in ClusterOperation) ClusterOperation {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ClusterOperation
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	out.Principal = in.Principal
	return out
}

func TestNamespacePostgresDelayedReceiptKeepsOriginalAttempt(t *testing.T) {
	f := newNamespacePGFixture(t)
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	o := namespaceCloneOperation(t, first.Operation)
	at := time.Now().UTC()
	o.Enrollment = &ClusterEnrollment{AttachmentID: "attachment", RuntimeConnectionID: "runtime-connection", TargetID: "runtime-target", ResourceRevision: 1, State: kubernetes.EnrollmentState{Attempted: map[string]bool{"Namespace": true}, Objects: map[string]string{}}}
	o.Steps[1].State = "attempted"
	o.Steps[1].AttemptedAt = &at
	o.Revision++
	if err = f.transactions.Checkpoint(t.Context(), first.Record, &o, 1); err != nil {
		t.Fatal(err)
	}
	observedA := namespaceCloneOperation(t, o)
	observedA.Enrollment.State.NamespaceUID = "namespace-uid"
	if err = f.transactions.preserveOutcome(t.Context(), &observedA); err != nil {
		t.Fatal(err)
	}
	recorded, err := f.transactions.store.getNamespaceAttemptOutcome(t.Context(), o.OrgID, o.ID, o.NamespaceAttemptID)
	if err != nil || recorded.AttemptID != o.NamespaceAttemptID {
		t.Fatalf("initial outcome: %v", err)
	}
	current, err := f.transactions.store.GetInstanceNamespaceRecord(t.Context(), o.OrgID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := namespaceCloneOperation(t, observedA)
	next.Revision++
	if err = f.transactions.Checkpoint(t.Context(), current, &next, o.Revision); err != nil {
		t.Fatal(err)
	}
	current, err = f.transactions.store.GetInstanceNamespaceRecord(t.Context(), o.OrgID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.transactions.store.getNamespaceAttemptOutcome(t.Context(), o.OrgID, o.ID, o.NamespaceAttemptID); err != nil {
		t.Fatalf("checkpoint erased receipt: %v", err)
	}
	// Publish fixture identities, then renew the current authority. The delayed
	// callback still carries the original attempted checkpoint's immutable ID.
	next.Enrollment.State.ServiceAccountUID = "account-uid"
	policyDigest, err := next.Input.Attachment.InstancePolicy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	next.Enrollment.State.Isolation = &kubernetes.InstanceIsolationState{Digest: policyDigest, Objects: map[string]string{"ResourceQuota": "quota", "LimitRange": "limits", "NetworkPolicy": "policy"}}
	next.Steps[1].State = "completed"
	next.Steps[1].CompletedAt = &at
	next.Status = "succeeded"
	attachment := &RuntimeAttachment{ID: "attachment", OrgID: o.OrgID, CloudID: o.CloudID, ClusterID: o.ResourceID, ProjectID: current.ProjectID, OperationID: o.ID, Revision: 1, SchemaVersion: 1, Namespace: current.Namespace, NamespaceUID: "namespace-uid", ServiceAccountName: "runtime", ServiceAccountUID: "account-uid", RuntimeConnectionID: "runtime-connection", EndpointFingerprint: o.Input.Attachment.EndpointFingerprint, TrustFingerprint: o.Input.Attachment.TrustFingerprint, CredentialExpiry: at.Add(time.Hour), CredentialVersion: 1, TrustRevision: 1, Ownership: "managed", CreatedAt: at, UpdatedAt: at, VaultKey: "fixture-reference"}
	connection := &connections.Connection{ID: attachment.RuntimeConnectionID, OrgID: o.OrgID, Cloud: connections.Kubernetes, Method: connections.MethodManagedKubernetes, Generation: 1, Name: "Runtime", Account: "namespace", Status: connections.StatusReady, Details: map[string]string{"managed_instance_id": current.Pins.InstanceID, "managed_environment_id": current.Pins.EnvironmentID}, CreatedBy: o.Principal.UserID, CreatedAt: at, UpdatedAt: at}
	if err = f.transactions.Publish(t.Context(), current, &next, attachment, connection); err != nil {
		t.Fatal(err)
	}
	current, err = f.transactions.store.GetInstanceNamespaceRecord(t.Context(), o.OrgID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.transactions.Renew(t.Context(), current, current, &next); err != nil {
		t.Fatal(err)
	}
	renewed, err := f.transactions.store.GetInstanceNamespaceRecord(t.Context(), o.OrgID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.CurrentAttemptID == o.NamespaceAttemptID {
		t.Fatal("renewal did not advance current attempt")
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE keysmith_keys SET revoked_at=now() WHERE id=$1`, f.action.Principal.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE loomplane_fleet_grants SET status='revoked',revision=revision+1 WHERE id=$1`, f.action.Record.CurrentAuthority.Request.GrantID); err != nil {
		t.Fatal(err)
	}
	observedA.Status = "needs_attention"
	if err = f.transactions.preserveOutcome(t.Context(), &observedA); err != nil {
		t.Fatalf("revocation lost known outcome: %v", err)
	}
	delayed, err := f.transactions.store.getNamespaceAttemptOutcome(t.Context(), o.OrgID, o.ID, o.NamespaceAttemptID)
	if err != nil || delayed.AttemptID != o.NamespaceAttemptID || delayed.InputDigest != o.NamespaceInputDigest || delayed.NamespaceRevision != recorded.NamespaceRevision || delayed.Reason != "operator_recovery_required" {
		t.Fatalf("relabelled delayed outcome: %+v %v", delayed, err)
	}
	after, err := f.transactions.store.GetInstanceNamespaceRecord(t.Context(), o.OrgID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !namespaceEqual(after, renewed) {
		t.Fatal("receipt recovery changed namespace authority")
	}
}

func TestNamespacePostgresCheckpointPreservesStoredLease(t *testing.T) {
	f := newNamespacePGFixture(t)
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := f.transactions.store.LeaseClusterOperation(t.Context(), first.Operation.OrgID, first.Operation.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	next := namespaceCloneOperation(t, *leased)
	expected := next.Revision
	next.Revision++
	extended := next.LeaseExpiresAt.Add(time.Hour)
	next.LeaseExpiresAt = &extended
	if err = f.transactions.Checkpoint(t.Context(), first.Record, &next, expected); err == nil {
		t.Fatal("checkpoint extended its own worker lease")
	}
	next.LeaseExpiresAt = leased.LeaseExpiresAt
	if err = f.transactions.Checkpoint(t.Context(), first.Record, &next, expected); err != nil {
		t.Fatalf("original leased checkpoint refused: %v", err)
	}
}
