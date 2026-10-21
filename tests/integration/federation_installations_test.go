//go:build integration

package integration

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"

	"github.com/xraph/loomplane/database/migrations"
	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func installationDatabase(t *testing.T) (*pgdriver.PgDB, *fed.PgStore, *domain.Destination, fed.KubernetesAttestationProfile) {
	t.Helper()
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	db := pgdriver.New()
	if err := db.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, q := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','p','p','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','kubernetes','c','cluster','ready','u',now(),now())`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	d := &domain.Destination{ID: "d", Scope: domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}, Name: "primary", ConnectionID: "c", Ownership: domain.Imported, Identity: domain.PhysicalIdentity{Provider: "kubernetes", Account: "cluster", ResourceID: "ns-uid", NamespaceUID: "ns-uid"}, Generation: 1, CreatedAt: now, UpdatedAt: now}
	store := fed.NewPgStore(db)
	if err := store.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	profile := fed.KubernetesAttestationProfile{Scope: d.Scope, DestinationID: d.ID, ConnectionID: d.ConnectionID, IdentityRevision: d.Identity.Revision(), PodName: "agent", PodUID: "pod-uid", ServiceAccount: "loomplane-agent", Container: "agent", SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	return db, store, d, profile
}
func TestFederationInstallationPersistenceAndFencing(t *testing.T) {
	db, store, d, profile := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	record, err := store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow)
	if err != nil {
		t.Fatal(err)
	}
	if record.Generation != 2 || record.RecordedBy != "operator" {
		t.Fatal("installation did not advance destination", record)
	}
	if _, readErr := store.GetInstallation(ctx, "foreign", d.ID); !errors.Is(readErr, fed.ErrNotFound) {
		t.Fatal("foreign installation visible", readErr)
	}
	restarted := fed.NewPgStore(db)
	loaded, err := restarted.GetInstallation(ctx, d.OrgID, d.ID)
	if err != nil || loaded.Profile != profile || loaded.Generation != record.Generation {
		t.Fatal("installation not persisted", err)
	}
	if deleteErr := store.Delete(ctx, d.OrgID, d.ID, record.Generation); !errors.Is(deleteErr, fed.ErrInUse) {
		t.Fatal("active installation lost its destination", deleteErr)
	}
	d, err = store.Get(ctx, d.OrgID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	enrollments := workloadidentity.NewEnrollmentStore(db, "example.test")
	binding := workloadidentity.Binding{Destination: *d, Method: "kubernetes", Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, ServiceAccount: profile.ServiceAccount, PodUID: profile.PodUID, Container: profile.Container, ParentURI: "spiffe://example.test/spire/agent/k8s_psat/cluster/node", TTL: time.Minute, CheckedAt: time.Now()}
	pending, err := enrollments.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &enrollmentIssuer{}
	if issueErr := enrollments.Issue(ctx, d.OrgID, pending.ID, issuer); issueErr != nil {
		t.Fatal(issueErr)
	}
	if consumeErr := enrollments.Consume(ctx, pending.ID, pending.Binding.Agent); consumeErr != nil {
		t.Fatal(consumeErr)
	}
	registry := runtimecatalog.NewPgStore(db)
	if checkErr := registry.CheckAgent(ctx, pending.Binding.Agent); checkErr != nil {
		t.Fatal(checkErr)
	}
	waiting, waitErr := enrollments.Reserve(ctx, binding, nil)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	if issueErr := enrollments.Issue(ctx, d.OrgID, waiting.ID, issuer); issueErr != nil {
		t.Fatal(issueErr)
	}
	replacement := profile
	replacement.PodUID = "replacement-pod"
	next, err := store.PutInstallation(ctx, *d, replacement, record.Generation, "operator", time.Now(), allow)
	if err != nil {
		t.Fatal(err)
	}
	if checkErr := registry.CheckAgent(ctx, pending.Binding.Agent); checkErr == nil {
		t.Fatal("old installation retained agent authority")
	}
	if checkErr := enrollments.CheckPending(ctx, waiting.ID, waiting.Binding.Agent); checkErr == nil {
		t.Fatal("old installation retained pending authority")
	}
	if consumeErr := enrollments.Consume(ctx, waiting.ID, waiting.Binding.Agent); consumeErr == nil {
		t.Fatal("old installation handle consumed")
	}
	if issueErr := enrollments.Issue(ctx, d.OrgID, waiting.ID, issuer); issueErr == nil {
		t.Fatal("old installation reissued")
	}

	if _, reserveErr := enrollments.Reserve(ctx, binding, nil); reserveErr == nil {
		t.Fatal("old binding reserved another enrollment")
	}
	candidates, err := enrollments.CleanupCandidates(ctx, d.OrgID, 20)
	if err != nil || len(candidates) != 2 || !slices.Contains(candidates, pending.ID) || !slices.Contains(candidates, waiting.ID) {
		t.Fatal("replaced installation not cleanup eligible", candidates, err)
	}
	if retireErr := store.RetireInstallation(ctx, d.OrgID, d.ID, record.Generation, "operator", allow); !errors.Is(retireErr, fed.ErrConflict) {
		t.Fatal("stale retirement accepted", retireErr)
	}
	if retireErr := store.RetireInstallation(ctx, d.OrgID, d.ID, next.Generation, "operator", allow); retireErr != nil {
		t.Fatal(retireErr)
	}
	if _, readErr := store.GetInstallation(ctx, d.OrgID, d.ID); !errors.Is(readErr, fed.ErrNotFound) {
		t.Fatal("retired installation still available", readErr)
	}
	d, err = store.Get(ctx, d.OrgID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleteErr := store.Delete(ctx, d.OrgID, d.ID, d.Generation); !errors.Is(deleteErr, fed.ErrInUse) {
		t.Fatal("retirement discarded outstanding trust references", deleteErr)
	}
	if cleanup, cleanupErr := enrollments.Cleanup(ctx, d.OrgID, 20, issuer); cleanupErr != nil || cleanup.Deleted != 2 {
		t.Fatal("cleanup failed", cleanup, cleanupErr)
	}
	if deleteErr := store.Delete(ctx, d.OrgID, d.ID, d.Generation); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	var events int
	if queryErr := db.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE org_id='o' AND type IN ('federation.installation.recorded','federation.installation.retired')`).Scan(&events); queryErr != nil || events != 3 {
		t.Fatal("durable lifecycle events missing", events, queryErr)
	}
}
func TestFederationInstallationCASAndAuthorizationRollback(t *testing.T) {
	_, store, d, profile := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	denied := errors.New("permission revoked")
	calls := 0
	_, err := store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), func(context.Context) error {
		calls++
		if calls > 1 {
			return denied
		}
		return nil
	})
	if !errors.Is(err, denied) {
		t.Fatal("permission revocation ignored", err)
	}
	unchanged, err := store.Get(ctx, d.OrgID, d.ID)
	if err != nil || unchanged.Generation != d.Generation {
		t.Fatal("failed write changed destination", err)
	}
	if _, readErr := store.GetInstallation(ctx, d.OrgID, d.ID); !errors.Is(readErr, fed.ErrNotFound) {
		t.Fatal("failed write retained installation", readErr)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, putErr := store.PutInstallation(ctx, *d, profile, 0, "operator", time.Now(), allow); putErr == nil {
				wins.Add(1)
			} else if !errors.Is(putErr, fed.ErrConflict) {
				t.Error(putErr)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("concurrent writers both won", wins.Load())
	}
	recorded, readErr := store.GetInstallation(ctx, d.OrgID, d.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	calls = 0
	if retireErr := store.RetireInstallation(ctx, d.OrgID, d.ID, recorded.Generation, "operator", func(context.Context) error {
		calls++
		if calls > 1 {
			return denied
		}
		return nil
	}); !errors.Is(retireErr, denied) {
		t.Fatal("retirement permission revocation ignored", retireErr)
	}
	if retained, readErr := store.GetInstallation(ctx, d.OrgID, d.ID); readErr != nil || retained.Generation != recorded.Generation {
		t.Fatal("failed retirement lost installation", readErr)
	}

	for _, change := range []func(*fed.KubernetesAttestationProfile){func(p *fed.KubernetesAttestationProfile) { p.OrgID = "foreign" }, func(p *fed.KubernetesAttestationProfile) { p.ConnectionID = "foreign" }, func(p *fed.KubernetesAttestationProfile) { p.PodUID = "" }} {
		bad := profile
		change(&bad)
		if _, putErr := store.PutInstallation(ctx, *d, bad, 0, "operator", time.Now(), allow); putErr == nil {
			t.Fatal("invalid profile stored")
		}
	}
}

func TestFederationInstallationWardenAuthorization(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `UPDATE loomplane_cloud_connections SET details='{"cluster_ca":"cluster","namespace_uid":"ns-uid"}',checked_at=now() WHERE org_id='o' AND id='c'`); err != nil {
		t.Fatal(err)
	}
	roles := memory.New()
	engine, err := warden.NewEngine(warden.WithStore(roles))
	if err != nil {
		t.Fatal(err)
	}
	auth := security.NewAuthorizer(engine, roles, nil)
	admin := security.Principal{OrgID: "o", UserID: "admin"}
	developer := security.Principal{OrgID: "o", UserID: "developer"}
	for principal, role := range map[security.Principal]string{admin: security.RoleAdmin, developer: security.RoleDeveloper} {
		if assignErr := auth.Assign(ctx, principal.OrgID, principal.UserID, role, "fixture"); assignErr != nil {
			t.Fatal(assignErr)
		}
	}
	destinations := fed.NewService(store, connections.NewPgStore(db), projects.NewPgStore(db), auth, nil, true)
	calls := 0
	revoke := false
	service := fed.NewInstallationService(destinations, store, func(ctx context.Context, d domain.Destination, p fed.KubernetesAttestationProfile) (workloadidentity.Binding, error) {
		calls++
		if revoke {
			if revokeErr := auth.Revoke(ctx, admin.OrgID, admin.UserID); revokeErr != nil {
				return workloadidentity.Binding{}, revokeErr
			}
		}
		return workloadidentity.Binding{Destination: d, Method: "kubernetes", CheckedAt: time.Now(), PodUID: p.PodUID, ServiceAccount: p.ServiceAccount, Container: p.Container}, nil
	})
	request := fed.InstallationInput{DestinationID: d.ID, DestinationGeneration: d.Generation, PodName: "agent", PodUID: "pod-uid", ServiceAccount: "agent", Container: "agent", SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	if _, recordErr := service.Record(ctx, developer, request); !errors.Is(recordErr, security.ErrForbidden) || calls != 0 {
		t.Fatal("developer reached installation verification", recordErr)
	}
	record, err := service.Record(ctx, admin, request)
	if err != nil {
		t.Fatal(err)
	}
	request.DestinationGeneration = record.Generation
	request.ExpectedGeneration = record.Generation
	request.PodUID = "replacement"
	revoke = true
	if _, recordErr := service.Record(ctx, admin, request); !errors.Is(recordErr, security.ErrForbidden) {
		t.Fatal("revoked Warden role committed installation", recordErr)
	}
	retained, err := store.GetInstallation(ctx, d.OrgID, d.ID)
	if err != nil || retained.Generation != record.Generation || retained.Profile.PodUID != "pod-uid" {
		t.Fatal("denied replacement changed installation", err)
	}
	foreign := admin
	foreign.OrgID = "other"
	if _, readErr := service.Get(ctx, foreign, d.ID); readErr == nil {
		t.Fatal("foreign tenant read installation")
	}
}
