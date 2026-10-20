//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/connections"
	management "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestFederationAccessManagement(t *testing.T) {
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
	for _, query := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','target','target','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,details,checked_at,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','{"docker_id":"daemon"}',now(),'u',now(),now())`,
	} {
		if _, seedErr := conn.Exec(ctx, query); seedErr != nil {
			t.Fatal(seedErr)
		}
	}
	db := pgdriver.New()
	if openErr := db.Open(ctx, dsn); openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	roles := memory.New()
	engine, err := warden.NewEngine(warden.WithStore(roles))
	if err != nil {
		t.Fatal(err)
	}
	auth := security.NewAuthorizer(engine, roles, nil)
	admin := security.Principal{OrgID: "o", UserID: "admin"}
	developer := security.Principal{OrgID: "o", UserID: "developer"}
	for principal, role := range map[security.Principal]string{admin: security.RoleAdmin, developer: security.RoleDeveloper} {
		if assignErr := auth.Assign(ctx, principal.OrgID, principal.UserID, role, "test"); assignErr != nil {
			t.Fatal(assignErr)
		}
	}
	destinations := management.NewPgStore(db)
	service := management.NewService(destinations, connections.NewPgStore(db), projects.NewPgStore(db), auth, nil, true)
	destination, err := service.CreateDestination(ctx, admin, domain.Destination{Scope: domain.Scope{ProjectID: "p", EnvironmentID: "e"}, Name: "primary", ConnectionID: "c", Ownership: domain.Imported})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	destination.Capabilities = domain.CapabilitySet{Revision: "qualified-fixture", IdentityRevision: destination.Identity.Revision(), CheckedAt: now, ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{}}
	for _, capability := range []domain.Capability{domain.WorkloadAttestation, domain.PolicyEnforcement, domain.BackendIsolation, domain.HTTPProtocol} {
		destination.Capabilities.Results[capability] = domain.CapabilityResult{State: domain.Supported}
	}
	destination.Generation++
	if updateErr := destinations.Update(ctx, destination, 1); updateErr != nil {
		t.Fatal(updateErr)
	}
	backend := access.NewPolicyStore(db)
	policies := management.NewAccessService(service, backend)
	export, err := policies.CreateExport(ctx, admin, management.ExportInput{ProjectID: "p", EnvironmentID: "e", ServiceID: "orders", DestinationIDs: []string{destination.ID}, Port: 8443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := policies.ProposeGrant(ctx, developer, management.GrantInput{Source: domain.WorkloadSelector{Scope: domain.Scope{OrgID: "forged", ProjectID: "p", EnvironmentID: "e"}, DestinationID: destination.ID, ServiceID: "checkout"}, ExportID: export.ID, ExportRevision: export.Revision, Port: export.Port, Protocol: export.Protocol})
	if err != nil {
		t.Fatal(err)
	}
	if _, denyErr := policies.ApproveGrant(ctx, developer, proposal.ID, proposal.Generation, proposal.Revision); !errors.Is(denyErr, security.ErrForbidden) {
		t.Fatalf("developer approval: %v", denyErr)
	}
	approved, err := policies.ApproveGrant(ctx, admin, proposal.ID, proposal.Generation, proposal.Revision)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := management.NewAccessService(service, access.NewPolicyStore(db)).GetGrant(ctx, admin, approved.ID)
	if err != nil || restored.ApprovedBy != admin.UserID || restored.ProposedBy != developer.UserID || restored.Source.OrgID != admin.OrgID {
		t.Fatalf("provenance: %+v %v", restored, err)
	}
	input, err := backend.Input(ctx, destination.Scope)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := access.Compile(input)
	if err != nil || len(compiled.Grants) != 1 {
		t.Fatalf("approved policy: %+v %v", compiled, err)
	}
	if revokeErr := auth.Revoke(ctx, admin.OrgID, admin.UserID); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if _, denyErr := policies.RevokeGrant(ctx, admin, approved.ID, approved.Generation); !errors.Is(denyErr, security.ErrForbidden) {
		t.Fatalf("revoked role: %v", denyErr)
	}
	if assignErr := auth.Assign(ctx, admin.OrgID, admin.UserID, security.RoleAdmin, "test"); assignErr != nil {
		t.Fatal(assignErr)
	}
	if _, outageErr := conn.Exec(ctx, `UPDATE loomplane_cloud_connections SET checked_at=now()-interval '1 hour' WHERE id='c'`); outageErr != nil {
		t.Fatal(outageErr)
	}
	if _, revokeErr := policies.RevokeGrant(ctx, admin, approved.ID, approved.Generation); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if _, retireErr := policies.RetireExport(ctx, admin, export.ID, export.Generation); retireErr != nil {
		t.Fatal(retireErr)
	}
	input, err = backend.Input(ctx, destination.Scope)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err = access.Compile(input)
	if err != nil || len(compiled.Grants) != 0 {
		t.Fatalf("revoked policy: %+v %v", compiled, err)
	}
	var count int
	if auditErr := conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_policy_history WHERE org_id='o'`).Scan(&count); auditErr != nil {
		t.Fatal(auditErr)
	}
	if count != 5 {
		t.Fatalf("policy audit records=%d", count)
	}
}
