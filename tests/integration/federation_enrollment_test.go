//go:build integration

package integration

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type enrollmentIssuer struct {
	fail      bool
	failEntry string
}

func (i *enrollmentIssuer) Register(_ context.Context, b workloadidentity.Binding) (*spiretypes.Entry, error) {
	if i.fail {
		return nil, errors.New("issuer unavailable")
	}
	return &spiretypes.Entry{Id: b.EntryID}, nil
}
func (i *enrollmentIssuer) Revoke(_ context.Context, entry string) error {
	if i.fail || entry == i.failEntry {
		return errors.New("issuer unavailable")
	}
	return nil
}

func TestEnrollmentCannotBeReplayed(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','p','p','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','u',now(),now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	physical := domain.PhysicalIdentity{Provider: "server", Account: "daemon", ResourceID: "daemon"}
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_federation_destinations(id,org_id,project_id,environment_id,name,connection_id,identity,identity_revision,generation,created_at,updated_at) VALUES('d','o','p','e','primary','c','{}',$1,1,now(),now())`, physical.Revision()); err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}
	uid := uint32(1000)
	binding := workloadidentity.Binding{Destination: domain.Destination{Scope: scope, ID: "d", Identity: physical, Generation: 1}, ParentURI: "spiffe://example.test/node/test", Method: "unix", UnixUID: &uid, Executable: "/app/agent", SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", CheckedAt: time.Now(), TTL: time.Minute}
	store := workloadidentity.NewEnrollmentStore(pg, "example.test")
	issuer := &enrollmentIssuer{}
	if _, err = store.Reserve(ctx, binding, map[string]runtimecatalog.ExportBounds{"invalid": {}}); err == nil {
		t.Fatal("invalid export bounds reached issuance")
	}

	first, err := store.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CheckPending(ctx, first.ID, first.Binding.Agent); err == nil {
		t.Fatal("unissued enrollment authenticated")
	}
	if err = store.Issue(ctx, "o", first.ID, issuer); err != nil {
		t.Fatal(err)
	}
	if err = store.CheckPending(ctx, first.ID, first.Binding.Agent); err != nil {
		t.Fatal(err)
	}
	foreign := first.Binding.Agent
	foreign.EnvironmentID = "other"
	if err = store.Consume(ctx, first.ID, foreign); err == nil {
		t.Fatal("wrong identity consumed enrollment")
	}
	// Independent processes race the same enrollment. Exactly one may activate it.
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			results <- workloadidentity.NewEnrollmentStore(pg, "example.test").Consume(ctx, first.ID, first.Binding.Agent)
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("consumed %d times", successes)
	}
	catalog := runtimecatalog.NewPgStore(pg)
	if err = catalog.CheckAgent(ctx, first.Binding.Agent); err != nil {
		t.Fatal(err)
	}
	second, err := store.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Binding.Agent.Epoch <= first.Binding.Agent.Epoch {
		t.Fatal("epoch did not advance")
	}
	if err = store.Issue(ctx, "o", second.ID, issuer); err != nil {
		t.Fatal(err)
	}

	// Failure after retiring the old observer must roll back the entire activation.
	if _, err = conn.Exec(ctx, `CREATE FUNCTION reject_observer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$; CREATE TRIGGER reject_observer BEFORE INSERT ON loomplane_federation_observers FOR EACH ROW EXECUTE FUNCTION reject_observer()`); err != nil {
		t.Fatal(err)
	}
	if err = store.Consume(ctx, second.ID, second.Binding.Agent); err == nil {
		t.Fatal("activation failure hidden")
	}
	if err = catalog.CheckAgent(ctx, first.Binding.Agent); err != nil {
		t.Fatalf("partial activation retired old agent: %v", err)
	}
	if err = store.CheckPending(ctx, second.ID, second.Binding.Agent); err != nil {
		t.Fatalf("partial activation consumed handle: %v", err)
	}
	if _, err = conn.Exec(ctx, `DROP TRIGGER reject_observer ON loomplane_federation_observers; DROP FUNCTION reject_observer()`); err != nil {
		t.Fatal(err)
	}
	if err = store.Consume(ctx, second.ID, second.Binding.Agent); err != nil {
		t.Fatal(err)
	}
	if err = catalog.CheckAgent(ctx, first.Binding.Agent); err == nil {
		t.Fatal("old certificate retained authority")
	}
	if err = catalog.CheckAgent(ctx, second.Binding.Agent); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.CleanupCandidates(ctx, "o", 100)
	if err != nil || !slices.Contains(candidates, first.ID) || slices.Contains(candidates, second.ID) {
		t.Fatalf("incorrect cleanup candidates: %v %v", candidates, err)
	}
	// Cleanup must leave the current active observer intact.
	result, cleanupErr := store.Cleanup(ctx, "o", 100, issuer)
	if cleanupErr != nil || result.Deleted != 1 || result.Failed != 0 {
		t.Fatalf("superseded cleanup: %+v %v", result, cleanupErr)
	}
	if err = catalog.CheckAgent(ctx, second.Binding.Agent); err != nil {
		t.Fatalf("cleanup revoked active observer: %v", err)
	}
	issuer.fail = true
	if err = store.Revoke(ctx, "o", second.ID, issuer); err == nil {
		t.Fatal("issuer outage hidden")
	}
	if err = catalog.CheckAgent(ctx, second.Binding.Agent); err == nil {
		t.Fatal("revocation outage retained local authority")
	}
	var refs int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_destination_refs WHERE kind='trust' AND owner_id=$1`, second.ID).Scan(&refs); err != nil || refs != 1 {
		t.Fatalf("lost cleanup reference: %d %v", refs, err)
	}
	if err = store.Issue(ctx, "o", second.ID, issuer); err == nil {
		t.Fatal("revoked enrollment reissued")
	}
	result, cleanupErr = store.Cleanup(ctx, "o", 100, issuer)
	if cleanupErr == nil || result.Failed != 1 || result.Deleted != 0 {
		t.Fatalf("cleanup hid failed revocation: %+v %v", result, cleanupErr)
	}
	issuer.fail = false
	result, cleanupErr = store.Cleanup(ctx, "o", 100, issuer)
	if cleanupErr != nil || result.Deleted != 1 {
		t.Fatalf("retry cleanup: %+v %v", result, cleanupErr)
	}
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_destination_refs WHERE kind='trust' AND owner_id=$1`, second.ID).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("cleanup reference stuck: %d %v", refs, err)
	}

	expired, err := store.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Issue(ctx, "o", expired.ID, issuer); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_federation_enrollments SET created_at=now()-interval '6 minutes',expires_at=now()-interval '1 minute' WHERE id=$1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Consume(ctx, expired.ID, expired.Binding.Agent); err == nil {
		t.Fatal("expired enrollment consumed")
	}
	if err = store.Revoke(ctx, "foreign", expired.ID, issuer); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_enrollments WHERE id=$1`, expired.ID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("foreign revoke changed enrollment: %d %v", retained, err)
	}

	superseded, err := store.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Issue(ctx, "o", superseded.ID, issuer); err != nil {
		t.Fatal(err)
	}
	third, err := store.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}

	candidates, err = store.CleanupCandidates(ctx, "o", 100)
	if err != nil || !slices.Contains(candidates, superseded.ID) || slices.Contains(candidates, third.ID) {
		t.Fatalf("abandoned pending enrollment: %v %v", candidates, err)
	}
	result, cleanupErr = store.Cleanup(ctx, "foreign", 100, issuer)
	if cleanupErr != nil || result.Attempted != 0 {
		t.Fatalf("foreign cleanup reached enrollment: %+v %v", result, cleanupErr)
	}
	if err = store.Consume(ctx, superseded.ID, superseded.Binding.Agent); err == nil {
		t.Fatal("older pending epoch activated")
	}
	if err = store.Issue(ctx, "o", third.ID, issuer); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=true,generation=generation+1 WHERE id='d'`); err != nil {
		t.Fatal(err)
	}
	if err = store.Consume(ctx, third.ID, third.Binding.Agent); err == nil {
		t.Fatal("quarantined destination activated")
	}
	// A failed remote deletion must not stop the rest of the bounded sweep.
	issuer.failEntry = expired.Binding.EntryID
	result, cleanupErr = store.Cleanup(ctx, "o", 100, issuer)
	if cleanupErr == nil || result.Failed != 1 || result.Deleted != 2 {
		t.Fatalf("quarantine cleanup stopped on one failed entry: %+v %v", result, cleanupErr)
	}
	issuer.failEntry = ""
	result, cleanupErr = store.Cleanup(ctx, "o", 100, issuer)
	if cleanupErr != nil || result.Deleted != 1 {
		t.Fatalf("expired enrollment retry: %+v %v", result, cleanupErr)
	}
}
