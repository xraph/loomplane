//go:build integration

package integration

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
)

func managedClusterDB(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := freshDatabase(t)
	if err := ops.Migrate(t.Context(), dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	db, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(t.Context()) })
	return db
}
func insertManagedResource(t *testing.T, db *pgx.Conn, id, org, external string) {
	t.Helper()
	_, err := db.Exec(t.Context(), `INSERT INTO loomplane_managed_resources(id,org_id,kind,provider,account_id,external_id,incarnation,ownership,status,body) VALUES($1,$2,'cluster','digitalocean','account',$3,'incarnation','discovered','ready','{}')`, id, org, external)
	if err != nil {
		t.Fatal(err)
	}
}
func TestManagedClusterDuplicateAccountAliases(t *testing.T) {
	db := managedClusterDB(t)
	insertManagedResource(t, db, "a", "org", "external")
	_, err := db.Exec(t.Context(), `INSERT INTO loomplane_managed_resources(id,org_id,kind,provider,account_id,external_id,incarnation,ownership,status,body) SELECT 'b',org_id,kind,provider,account_id,external_id,incarnation,ownership,status,body FROM loomplane_managed_resources WHERE id='a'`)
	if err == nil {
		t.Fatal("account aliases duplicated an external identity")
	}
}
func TestManagedClusterCrossOrgEdges(t *testing.T) {
	db := managedClusterDB(t)
	insertManagedResource(t, db, "a", "one", "one")
	insertManagedResource(t, db, "b", "two", "two")
	_, err := db.Exec(t.Context(), `INSERT INTO loomplane_resource_relations(org_id,source_id,destination_id,kind) VALUES('one','a','b','owns')`)
	if err == nil {
		t.Fatal("cross-organization edge accepted")
	}
}
func TestManagedClusterRelationCycle(t *testing.T) {
	db := managedClusterDB(t)
	insertManagedResource(t, db, "a", "org", "one")
	_, err := db.Exec(t.Context(), `INSERT INTO loomplane_resource_relations(org_id,source_id,destination_id,kind) VALUES('org','a','a','owns')`)
	if err == nil {
		t.Fatal("cycle accepted")
	}
}

func managedClusterStore(t *testing.T, db *pgx.Conn) (*cloudinfra.PgStore, *cloudprofiles.PgStore) {
	t.Helper()
	pool := pgdriver.New()
	if err := pool.Open(t.Context(), db.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	_, err := db.Exec(t.Context(), `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj','org','One','one','ready','usr',now(),now())`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	clouds := cloudprofiles.NewPgStore(pool)
	if err = clouds.CreateProfile(t.Context(), &cloudprofiles.Profile{ID: "cloud", OrgID: "org", ProjectID: "proj", Name: "Cloud", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = connections.NewPgStore(pool).CreateConnection(t.Context(), &connections.Connection{ID: "conn", OrgID: "org", Cloud: connections.GCP, Name: "Account", Status: connections.StatusReady, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return cloudinfra.NewPgStore(pool), clouds
}

func TestManagedClusterNamespaceClaim(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: "external", Incarnation: "physical", Ownership: "external", Status: "ready", Revision: 1}
	if _, err := store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- store.SaveRuntimeAttachment(t.Context(), &cloudinfra.RuntimeAttachment{ID: fmt.Sprintf("attachment-%d", i), OrgID: "org", CloudID: "cloud", ClusterID: "cluster", ProjectID: "proj", Namespace: "app", NamespaceUID: "ns-uid", ServiceAccountUID: "sa-uid", RuntimeConnectionID: "conn", Revision: 1}, 0)
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, cloudinfra.ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d namespace winners", winners)
	}
}

func TestManagedClusterCheckpointCAS(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	o := &cloudinfra.ClusterOperation{ID: "op", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Digest: "review", Revision: 1, Status: "preview", Steps: []cloudinfra.ClusterStep{{Name: "create", State: "pending", RequestDigest: "request"}}}
	if err := store.CreateClusterPreview(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	next := *o
	next.Revision = 2
	next.Status = "accepted"
	if err := store.SaveClusterCheckpoint(t.Context(), &next, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveClusterCheckpoint(t.Context(), &next, 1); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("stale checkpoint: %v", err)
	}
	next.Revision = 3
	next.Steps = []cloudinfra.ClusterStep{{Name: "create", State: "attempted", RequestDigest: "changed"}}
	if err := store.SaveClusterCheckpoint(t.Context(), &next, 2); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("request digest changed: %v", err)
	}
	saved, err := store.GetClusterOperation(t.Context(), "org", "cloud", "op")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 2 || saved.Steps[0].RequestDigest != "request" {
		t.Fatal("failed checkpoint was not atomic")
	}
	pending, err := store.ListPendingClusterOperations(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %v %v", pending, err)
	}
}

func TestManagedClusterMigrationCompatibility(t *testing.T) {
	db := managedClusterDB(t)
	store, clouds := managedClusterStore(t, db)
	now := time.Now().UTC()
	target := &cloudprofiles.Target{ID: "target", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "legacy", DisplayName: "Legacy", Provider: "cloud-run", ConnectionID: "conn", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := clouds.CreateTarget(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	operation := &cloudinfra.ClusterOperation{ID: "historic", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Digest: "unchanged", Revision: 1, Status: "preview"}
	if err := store.CreateClusterPreview(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	legacy := &cloudinfra.Operation{ID: "legacy-op", OrgID: "org", CloudID: "cloud", Status: "preview", Review: "legacy-review"}
	if err := store.CreatePreview(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	var legacyBefore string
	if err := db.QueryRow(t.Context(), `SELECT body::text FROM loomplane_infrastructure_operations WHERE id='legacy-op'`).Scan(&legacyBefore); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(t.Context(), `SELECT body::text FROM loomplane_cluster_operations WHERE id='historic'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	pool := pgdriver.New()
	if err := pool.Open(t.Context(), db.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	found := false
	for _, migration := range migrations.Group().Migrations() {
		if migration.Name == "managed_clusters" {
			found = true
			if err := migration.Up(t.Context(), pgmigrate.New(pool)); err != nil {
				t.Fatal(err)
			}

			resource := &cloudinfra.ManagedResource{ID: "downgrade-resource", OrgID: "org", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: "downgrade-external", Incarnation: "physical", Ownership: "discovered", Status: "ready", Revision: 1}
			if _, err := store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
				t.Fatal(err)
			}
			if err := migration.Down(t.Context(), pgmigrate.New(pool)); err == nil {
				t.Fatal("downgrade accepted active resource")
			}
		}
	}
	if !found {
		t.Fatal("managed cluster migration missing")
	}
	var after string
	if err := db.QueryRow(t.Context(), `SELECT body::text FROM loomplane_cluster_operations WHERE id='historic'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration rewrote historic review")
	}
	var legacyAfter string
	if err := db.QueryRow(t.Context(), `SELECT body::text FROM loomplane_infrastructure_operations WHERE id='legacy-op'`).Scan(&legacyAfter); err != nil {
		t.Fatal(err)
	}
	if legacyBefore != legacyAfter {
		t.Fatal("migration rewrote legacy server review")
	}

	got, err := clouds.GetTarget(t.Context(), "org", "proj", "cloud", "target")
	if err != nil {
		t.Fatal(err)
	}
	if got.ManagedResourceID != "" || got.LocationID != "" {
		t.Fatal("legacy target acquired managed ownership")
	}
	got.DisplayName = "Renamed"
	got.Revision = 2
	if err = clouds.UpdateTarget(t.Context(), got, 1); err != nil {
		t.Fatal(err)
	}
}

func TestManagedClusterPaginationAndIndexes(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	_, err := db.Exec(t.Context(), `INSERT INTO loomplane_managed_resources(id,org_id,cloud_id,kind,provider,account_id,external_id,incarnation,ownership,status,body) SELECT 'r'||lpad(n::text,4,'0'),'org','cloud','cluster','digitalocean','account',n::text,'physical','external','ready','{}' FROM generate_series(1,120) n`)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListManagedResources(t.Context(), "org", "cloud", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 50 || page.NextCursor != "r0050" {
		t.Fatalf("first page: %d %s", len(page.Items), page.NextCursor)
	}
	page, err = store.ListManagedResources(t.Context(), "org", "cloud", page.NextCursor, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 70 || page.NextCursor != "" {
		t.Fatalf("last page: %d %s", len(page.Items), page.NextCursor)
	}
	if _, err = db.Exec(t.Context(), `SET enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ sql, index string }{
		{`EXPLAIN SELECT id FROM loomplane_managed_resources WHERE org_id='org' AND cloud_id='cloud' AND id>'r0001' ORDER BY id LIMIT 51`, "loomplane_managed_resources_cloud_page"},
		{`EXPLAIN SELECT body,principal FROM loomplane_cluster_operations WHERE status IN ('accepted','running','waiting_provider') ORDER BY updated_at,id LIMIT 100`, "loomplane_cluster_operations_pending"},
	} {
		rows, e := db.Query(t.Context(), test.sql)
		if e != nil {
			t.Fatal(e)
		}
		plan := ""
		for rows.Next() {
			var line string
			if e = rows.Scan(&line); e != nil {
				t.Fatal(e)
			}
			plan += line
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
		if !strings.Contains(plan, test.index) {
			t.Fatalf("missing %s: %s", test.index, plan)
		}
	}
}

func TestManagedClusterConcurrentOwnership(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: "external", Incarnation: "physical", Ownership: "discovered", Status: "ready", Revision: 1}
	if _, err := store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
		t.Fatal(err)
	}
	resource.CloudID = "cloud"
	resource.Ownership = "external"
	resource.Revision = 2
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := store.ClaimManagedResource(t.Context(), resource, 1); results <- err }()
	}
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, cloudinfra.ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d ownership winners", winners)
	}
}

func TestManagedClusterTargetAttachmentScope(t *testing.T) {
	db := managedClusterDB(t)
	store, clouds := managedClusterStore(t, db)
	resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: "external", Incarnation: "physical", Ownership: "external", Status: "ready", Revision: 1}
	if _, err := store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := &cloudprofiles.Target{ID: "target", OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: "app", DisplayName: "App", Provider: "kubernetes", ConnectionID: "conn", Revision: 1, ManagedResourceID: "cluster", CreatedAt: now, UpdatedAt: now}
	if err := clouds.CreateTarget(t.Context(), target); err == nil {
		t.Fatal("target published before attachment")
	}
	attachment := &cloudinfra.RuntimeAttachment{ID: "attachment", OrgID: "org", CloudID: "cloud", ClusterID: "cluster", ProjectID: "proj", Namespace: "app", NamespaceUID: "ns-uid", ServiceAccountUID: "sa-uid", RuntimeConnectionID: "conn", Revision: 1}
	if err := store.SaveRuntimeAttachment(t.Context(), attachment, 0); err != nil {
		t.Fatal(err)
	}
	if err := clouds.CreateTarget(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env','org','proj','production','production',now(),now())`,
		`INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES('org','proj','env','cloud',1)`,
	} {
		if _, err := db.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	assignment, err := clouds.Resolve(t.Context(), "org", "proj", "env")
	if err != nil || len(assignment.Targets) != 1 {
		t.Fatalf("active target missing: %v %v", assignment, err)
	}
	attachment.Revision = 2
	attachment.DeletedAt = &now
	if err = store.SaveRuntimeAttachment(t.Context(), attachment, 1); err != nil {
		t.Fatal(err)
	}
	assignment, err = clouds.Resolve(t.Context(), "org", "proj", "env")
	if err != nil || len(assignment.Targets) != 0 {
		t.Fatalf("deleted attachment remains eligible: %v %v", assignment, err)
	}
}

func TestManagedClusterAttachmentRejectsNamespaceRename(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: "external", Incarnation: "physical", Ownership: "external", Status: "ready", Revision: 1}
	if _, err := store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
		t.Fatal(err)
	}
	original := &cloudinfra.RuntimeAttachment{ID: "attachment", OrgID: "org", CloudID: "cloud", ClusterID: "cluster", ProjectID: "proj", Namespace: "app", NamespaceUID: "ns-uid", ServiceAccountUID: "sa-uid", RuntimeConnectionID: "conn", Revision: 1}
	if err := store.SaveRuntimeAttachment(t.Context(), original, 0); err != nil {
		t.Fatal(err)
	}
	changed, err := store.GetRuntimeAttachment(t.Context(), "org", "attachment")
	if err != nil {
		t.Fatal(err)
	}
	changed.Namespace = "other"
	changed.Revision++
	if err = store.SaveRuntimeAttachment(t.Context(), changed, 1); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("namespace rename accepted: %v", err)
	}
	saved, err := store.GetRuntimeAttachment(t.Context(), "org", "attachment")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Namespace != original.Namespace || saved.Revision != 1 {
		t.Fatalf("rejected rename changed stored attachment: %+v", saved)
	}
	var namespace, bodyNamespace string
	var revision int64
	if err = db.QueryRow(t.Context(), `SELECT namespace,body->>'namespace',revision FROM loomplane_runtime_attachments WHERE id='attachment'`).Scan(&namespace, &bodyNamespace, &revision); err != nil {
		t.Fatal(err)
	}
	if namespace != "app" || bodyNamespace != "app" || revision != 1 {
		t.Fatalf("attachment row changed: %s %s %d", namespace, bodyNamespace, revision)
	}
}

func TestManagedClusterCheckpointRejectsDuplicateSteps(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	attempted := time.Now().UTC()
	original := &cloudinfra.ClusterOperation{ID: "op", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Digest: "review", Revision: 1, Status: "preview", Steps: []cloudinfra.ClusterStep{
		{Name: "a", State: "pending", RequestDigest: "request-a"},
		{Name: "b", State: "attempted", RequestDigest: "request-b", AttemptedAt: &attempted, RemoteOperationID: "remote-b"},
	}}
	if err := store.CreateClusterPreview(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	changed := *original
	changed.Revision = 2
	changed.Status = "accepted"
	duplicate := original.Steps[0]
	duplicate.State = "completed"
	changed.Steps = []cloudinfra.ClusterStep{duplicate, duplicate}
	if err := store.SaveClusterCheckpoint(t.Context(), &changed, 1); !errors.Is(err, cloudinfra.ErrInvalid) {
		t.Fatalf("duplicate checkpoint accepted: %v", err)
	}
	saved, err := store.GetClusterOperation(t.Context(), "org", "cloud", "op")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.Status != "preview" || len(saved.Steps) != 2 || saved.Steps[0].State != "pending" || saved.Steps[1].Name != "b" || saved.Steps[1].AttemptedAt == nil || saved.Steps[1].RemoteOperationID != "remote-b" {
		t.Fatalf("rejected checkpoint changed operation: %+v", saved)
	}
	var state, receipt string
	if err = db.QueryRow(t.Context(), `SELECT a.state,b.body->>'remote_operation_id' FROM loomplane_cluster_operation_steps a JOIN loomplane_cluster_operation_steps b ON b.org_id=a.org_id AND b.operation_id=a.operation_id WHERE a.operation_id='op' AND a.name='a' AND b.name='b'`).Scan(&state, &receipt); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || receipt != "remote-b" {
		t.Fatalf("rejected checkpoint changed durable steps: %s %s", state, receipt)
	}
}
