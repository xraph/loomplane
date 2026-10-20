//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestFederationAccess(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if checkErr := ops.Migrate(ctx, dsn, migrations.All()); checkErr != nil {
		t.Fatal(checkErr)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, q := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','target','target','ready','u',now(),now()),('p2','o','source','source','ready','u',now(),now()),('pf','foreign','foreign','foreign','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now()),('e2','o','p2','prod','production',now(),now()),('ef','foreign','pf','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','u',now(),now())`,
		`INSERT INTO loomplane_federation_destinations(id,org_id,project_id,environment_id,name,connection_id,identity,identity_revision,generation,created_at,updated_at) VALUES('target','o','p','e','target','c','{}','target',1,now(),now()),('source','o','p2','e2','source','c','{}','source',1,now(),now())`,
	} {
		if _, seedErr := conn.Exec(ctx, q); seedErr != nil {
			t.Fatal(seedErr)
		}
	}
	db := pgdriver.New()
	if openErr := db.Open(ctx, dsn); openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	store := access.NewPolicyStore(db)
	allow := func(context.Context) error { return nil }
	target := domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}
	source := domain.Scope{OrgID: "o", ProjectID: "p2", EnvironmentID: "e2"}
	exp := domain.ServiceExport{Metadata: domain.Metadata{Scope: target, ID: "api"}, ServiceID: "orders", DestinationIDs: []string{"target"}, Port: 8443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}
	export, err := store.PutExport(ctx, exp, 0, "admin", allow)
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.AccessGrant{Metadata: domain.Metadata{Scope: target, ID: "grant"}, Source: domain.WorkloadSelector{Scope: source, DestinationID: "source", ServiceID: "checkout"}, ExportID: "api", ExportRevision: export.Revision, Port: 8443, Protocol: domain.HTTP, HTTPMethods: []string{"GET"}, HTTPPaths: []string{"/orders"}, ApprovedRevision: "caller-supplied", OwnerID: "forged-owner"}
	proposed, err := store.ProposeGrant(ctx, grant, 0, "developer", allow)
	if err != nil {
		t.Fatal(err)
	}
	if proposed.ApprovedRevision != "" || proposed.OwnerID != "developer" || proposed.Generation != 1 {
		t.Fatal("proposal accepted caller authority")
	}
	assertPolicy := func(want int) domain.PolicyInput {
		t.Helper()
		in, inputErr := store.Input(ctx, source)
		if inputErr != nil {
			t.Fatal(inputErr)
		}
		policy, compileErr := access.Compile(in)
		if compileErr != nil || len(policy.Grants) != want {
			t.Fatalf("policy: grants=%d err=%v", len(policy.Grants), compileErr)
		}
		return in
	}
	assertPolicy(0)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, approveErr := store.ApproveGrant(ctx, "o", "grant", proposed.Generation, proposed.Revision, "admin", allow)
			if approveErr == nil {
				wins.Add(1)
			} else if !errors.Is(approveErr, access.ErrPolicyConflict) {
				t.Error(approveErr)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("approval winners=%d", wins.Load())
	}
	first := assertPolicy(1)
	inbound, err := store.Input(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	inboundPolicy, err := access.Compile(inbound)
	if err != nil || len(inboundPolicy.Grants) != 1 {
		t.Fatal("target lost its inbound grant")
	}
	foreignInput, err := store.Input(ctx, domain.Scope{OrgID: "foreign", ProjectID: "pf", EnvironmentID: "ef"})
	if err != nil || len(foreignInput.Grants) != 0 || len(foreignInput.Exports) != 0 {
		t.Fatal("foreign scope received policy records")
	}

	approved, err := store.GetGrant(ctx, "o", "grant")
	if err != nil {
		t.Fatal(err)
	}
	if approved.ApprovedBy != "admin" || approved.ReviewedRevision != proposed.Revision || approved.ApprovedAt == nil {
		t.Fatal("approval provenance missing")
	}
	restarted := access.NewPolicyStore(db)
	if _, lookupErr := restarted.GetGrant(ctx, "foreign", "grant"); !errors.Is(lookupErr, access.ErrPolicyNotFound) {
		t.Fatal("foreign organization read grant")
	}
	if _, approveErr := store.ApproveGrant(ctx, "foreign", "grant", approved.Generation, approved.Revision, "admin", allow); !errors.Is(approveErr, access.ErrPolicyNotFound) {
		t.Fatal("foreign approval accepted")
	}
	denied := func(context.Context) error { return security.ErrForbidden }
	if _, revokeErr := store.RevokeGrant(ctx, "o", "grant", approved.Generation, "admin", denied); !errors.Is(revokeErr, security.ErrForbidden) {
		t.Fatal("revoked human permission ignored")
	}
	if in := assertPolicy(1); in.Generation != first.Generation {
		t.Fatal("failed authorization changed policy generation")
	}
	revoked, err := store.RevokeGrant(ctx, "o", "grant", approved.Generation, "admin", allow)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == nil || revoked.RevokedBy != "admin" {
		t.Fatal("revocation not persisted")
	}
	if in := assertPolicy(0); in.Generation <= first.Generation {
		t.Fatal("revocation did not advance generation")
	}
	if _, approveErr := store.ApproveGrant(ctx, "o", "grant", revoked.Generation, revoked.Revision, "admin", allow); !errors.Is(approveErr, access.ErrPolicyConflict) {
		t.Fatal("revoked revision reapproved")
	}
	// An edited export invalidates its reviewed grant without deleting history.
	proposed, err = store.ProposeGrant(ctx, grant, revoked.Generation, "developer", allow)
	if err != nil {
		t.Fatal(err)
	}
	exp.Port = 9443
	export, err = store.PutExport(ctx, exp, export.Generation, "admin", allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, approveErr := store.ApproveGrant(ctx, "o", "grant", proposed.Generation, proposed.Revision, "admin", allow); !errors.Is(approveErr, access.ErrPolicyConflict) {
		t.Fatal("stale export approved")
	}
	grant.ExportRevision = export.Revision
	grant.Port = export.Port
	proposed, err = store.ProposeGrant(ctx, grant, proposed.Generation, "developer", allow)
	if err != nil {
		t.Fatal(err)
	}
	approved, err = store.ApproveGrant(ctx, "o", "grant", proposed.Generation, proposed.Revision, "admin", allow)
	if err != nil {
		t.Fatal(err)
	}
	beforeTargetQuarantine := assertPolicy(1)
	if _, checkErr := conn.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=true WHERE id='target'`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if after := assertPolicy(0); after.Generation <= beforeTargetQuarantine.Generation {
		t.Fatal("target quarantine did not advance generation")
	}
	if _, checkErr := conn.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=false WHERE id='target'`); checkErr != nil {
		t.Fatal(checkErr)
	}
	beforeQuarantine := assertPolicy(1)
	if _, checkErr := conn.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=true WHERE id='source'`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if after := assertPolicy(0); after.Generation <= beforeQuarantine.Generation {
		t.Fatal("quarantine did not advance policy generation")
	}
	// Old destinations stay retained until explicit cleanup proves old leases ended.
	if _, checkErr := conn.Exec(ctx, `DELETE FROM loomplane_federation_destinations WHERE id='source'`); checkErr == nil {
		t.Fatal("grant source retention lost")
	}
	if _, checkErr := conn.Exec(ctx, `DELETE FROM loomplane_federation_destinations WHERE id='target'`); checkErr == nil {
		t.Fatal("export target retention lost")
	}
	var history int
	if checkErr := conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_policy_history WHERE org_id='o'`).Scan(&history); checkErr != nil || history < 8 {
		t.Fatalf("missing transactional history: %d %v", history, checkErr)
	}
	if checkErr := ops.Migrate(ctx, dsn, migrations.All()); checkErr != nil {
		t.Fatal(checkErr)
	}
	// Invalid scope and missing server authorization cannot change stored policy.
	foreignSource := grant
	foreignSource.ID = "wrong-source"
	foreignSource.Source.ProjectID = "p"
	if _, checkErr := store.ProposeGrant(ctx, foreignSource, 0, "developer", allow); !errors.Is(checkErr, access.ErrPolicyNotFound) {
		t.Fatal("source destination escaped its project")
	}
	if _, checkErr := store.PutExport(ctx, exp, export.Generation, "admin", nil); !errors.Is(checkErr, access.ErrPolicy) {
		t.Fatal("missing decision guard accepted")
	}
	// Serialized decisions recheck permission after waiting, not before the lock.
	if _, checkErr := conn.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=false WHERE id='source'`); checkErr != nil {
		t.Fatal(checkErr)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, checkErr := tx.Exec(ctx, `SELECT generation FROM loomplane_federation_policy_versions WHERE org_id='o' FOR UPDATE`); checkErr != nil {
		t.Fatal(checkErr)
	}
	var permitted atomic.Bool
	permitted.Store(true)
	result := make(chan error, 1)
	go func() {
		_, revokeErr := store.RevokeGrant(ctx, "o", "grant", approved.Generation, "admin", func(context.Context) error {
			if !permitted.Load() {
				return security.ErrForbidden
			}
			return nil
		})
		result <- revokeErr
	}()
	select {
	case resultErr := <-result:
		t.Fatalf("mutation did not serialize: %v", resultErr)
	case <-time.After(30 * time.Millisecond):
	}
	permitted.Store(false)
	if checkErr := tx.Commit(ctx); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := <-result; !errors.Is(checkErr, security.ErrForbidden) {
		t.Fatalf("stale permission applied: %v", checkErr)
	}
	var guardCalls int
	if _, checkErr := store.RevokeGrant(ctx, "o", "grant", approved.Generation, "admin", func(context.Context) error {
		guardCalls++
		if guardCalls > 1 {
			return security.ErrForbidden
		}
		return nil
	}); !errors.Is(checkErr, security.ErrForbidden) {
		t.Fatal("permission was not rechecked before commit")
	}
	beforeFailure := assertPolicy(1)
	if _, checkErr := conn.Exec(ctx, `CREATE FUNCTION reject_policy_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_policy_history BEFORE INSERT ON loomplane_federation_policy_history FOR EACH ROW EXECUTE FUNCTION reject_policy_history()`); checkErr != nil {
		t.Fatal(checkErr)
	}
	if _, checkErr := store.RevokeGrant(ctx, "o", "grant", approved.Generation, "admin", allow); checkErr == nil {
		t.Fatal("audit failure committed revocation")
	}
	if after := assertPolicy(1); after.Generation != beforeFailure.Generation {
		t.Fatal("audit failure advanced generation")
	}
	unchanged, err := store.GetGrant(ctx, "o", "grant")
	if err != nil || unchanged.Generation != approved.Generation || unchanged.RevokedAt != nil {
		t.Fatal("audit failure partially changed grant")
	}
	if _, checkErr := conn.Exec(ctx, `DROP TRIGGER reject_policy_history ON loomplane_federation_policy_history; DROP FUNCTION reject_policy_history()`); checkErr != nil {
		t.Fatal(checkErr)
	}
	// Expiry advances the durable generation before the next policy is read.
	current, err := store.GetGrant(ctx, "o", "grant")
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(2 * time.Second)
	grant.ExpiresAt = &expiry
	expiring, err := store.ProposeGrant(ctx, grant, current.Generation, "developer", allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, checkErr := store.ApproveGrant(ctx, "o", "grant", expiring.Generation, expiring.Revision, "admin", allow); checkErr != nil {
		t.Fatal(checkErr)
	}
	beforeExpiry := assertPolicy(1)
	timer := time.NewTimer(time.Until(expiry.Add(time.Millisecond)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if after := assertPolicy(0); after.Generation <= beforeExpiry.Generation {
		t.Fatal("expiry changed policy without advancing generation")
	}
	retired, err := store.RetireExport(ctx, "o", "api", export.Generation, "admin", allow)
	if err != nil || retired.RetiredAt == nil {
		t.Fatalf("retire export: %v", err)
	}
	if after := assertPolicy(0); after.Generation <= beforeFailure.Generation {
		t.Fatal("retirement did not advance generation")
	}
	if _, checkErr := conn.Exec(ctx, `DELETE FROM loomplane_federation_destinations WHERE id='target'`); checkErr == nil {
		t.Fatal("retirement released destination before lease cleanup")
	}
	if _, checkErr := store.PutExport(ctx, exp, retired.Generation, "admin", allow); !errors.Is(checkErr, access.ErrPolicyConflict) {
		t.Fatal("retired export resurrected")
	}
}
