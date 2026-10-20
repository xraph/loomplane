package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type accessBackend struct {
	AccessBackend
	export      *access.ExportRecord
	grant       *access.GrantRecord
	writes      int
	beforeGuard func()
}

func (b *accessBackend) GetExport(_ context.Context, org, id string) (*access.ExportRecord, error) {
	if b.export == nil || b.export.OrgID != org || b.export.ID != id {
		return nil, access.ErrPolicyNotFound
	}
	record := *b.export
	return &record, nil
}
func (b *accessBackend) GetGrant(_ context.Context, org, id string) (*access.GrantRecord, error) {
	if b.grant == nil || b.grant.OrgID != org || b.grant.ID != id {
		return nil, access.ErrPolicyNotFound
	}
	record := *b.grant
	return &record, nil
}
func (b *accessBackend) check(ctx context.Context, guard access.DecisionGuard) error {
	if b.beforeGuard != nil {
		b.beforeGuard()
	}
	return guard(ctx)
}
func (b *accessBackend) PutExport(ctx context.Context, in domain.ServiceExport, expected int64, _ string, guard access.DecisionGuard) (*access.ExportRecord, error) {
	if err := b.check(ctx, guard); err != nil {
		return nil, err
	}
	b.writes++
	in.Generation = expected + 1
	rev, err := access.ExportRevision(in)
	if err != nil {
		return nil, err
	}
	b.export = &access.ExportRecord{ServiceExport: in, Revision: rev}
	return b.export, nil
}
func (b *accessBackend) ProposeGrant(ctx context.Context, in domain.AccessGrant, expected int64, actor string, guard access.DecisionGuard) (*access.GrantRecord, error) {
	if err := b.check(ctx, guard); err != nil {
		return nil, err
	}
	b.writes++
	in.Generation = expected + 1
	in.OwnerID = actor
	rev, err := access.GrantRevision(in)
	if err != nil {
		return nil, err
	}
	b.grant = &access.GrantRecord{AccessGrant: in, Revision: rev, ProposedBy: actor}
	return b.grant, nil
}
func (b *accessBackend) ApproveGrant(ctx context.Context, _, _ string, _ int64, _, actor string, guard access.DecisionGuard) (*access.GrantRecord, error) {
	if err := b.check(ctx, guard); err != nil {
		return nil, err
	}
	b.writes++
	record := *b.grant
	record.ApprovedBy = actor
	return &record, nil
}
func (b *accessBackend) RevokeGrant(ctx context.Context, _, _ string, _ int64, actor string, guard access.DecisionGuard) (*access.GrantRecord, error) {
	if err := b.check(ctx, guard); err != nil {
		return nil, err
	}
	b.writes++
	record := *b.grant
	record.RevokedBy = actor
	return &record, nil
}
func (b *accessBackend) RetireExport(ctx context.Context, _, _ string, _ int64, _ string, guard access.DecisionGuard) (*access.ExportRecord, error) {
	if err := b.check(ctx, guard); err != nil {
		return nil, err
	}
	b.writes++
	record := *b.export
	return &record, nil
}

type policyAudit struct{ events []projects.AuditEvent }

func (a *policyAudit) Record(_ context.Context, event projects.AuditEvent) {
	a.events = append(a.events, event)
}
func accessFixture(t *testing.T) (*AccessService, *accessBackend, *resources, *memoryStore, *security.Authorizer, security.Principal, ExportInput) {
	t.Helper()
	destinations, r, store, p, input := fixture(t)
	roles := memory.New()
	engine, err := warden.NewEngine(warden.WithStore(roles))
	if err != nil {
		t.Fatal(err)
	}
	auth := security.NewAuthorizer(engine, roles, nil)
	if assignErr := auth.Assign(t.Context(), p.OrgID, p.UserID, security.RoleAdmin, "test"); assignErr != nil {
		t.Fatal(assignErr)
	}
	destinations.authz = auth
	destination, err := destinations.CreateDestination(t.Context(), p, input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	destination.Capabilities = domain.CapabilitySet{Revision: "qualified-fixture", IdentityRevision: destination.Identity.Revision(), CheckedAt: now, ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{}}
	for _, capability := range []domain.Capability{domain.WorkloadAttestation, domain.GatewayAttestation, domain.PolicyEnforcement, domain.BackendIsolation, domain.HTTPProtocol, domain.GRPCProtocol, domain.TCPProtocol} {
		destination.Capabilities.Results[capability] = domain.CapabilityResult{State: domain.Supported}
	}
	store.rows[destination.ID] = *destination
	destinations.audit = &policyAudit{}
	backend := &accessBackend{}
	return NewAccessService(destinations, backend), backend, r, store, auth, p, ExportInput{ProjectID: "project", EnvironmentID: "env", ServiceID: "orders", DestinationIDs: []string{destination.ID}, Port: 8443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}
}
func proposeFixture(t *testing.T, service *AccessService, p security.Principal, input ExportInput) *access.GrantRecord {
	t.Helper()
	export, err := service.CreateExport(t.Context(), p, input)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.ProposeGrant(t.Context(), p, GrantInput{Source: domain.WorkloadSelector{Scope: domain.Scope{OrgID: "forged", ProjectID: input.ProjectID, EnvironmentID: input.EnvironmentID}, DestinationID: input.DestinationIDs[0], ServiceID: "checkout"}, ExportID: export.ID, ExportRevision: export.Revision, Port: export.Port, Protocol: export.Protocol, HTTPMethods: []string{"GET"}, HTTPPaths: []string{"/orders"}})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}
func TestAccessManagementUsesPrincipalAndServerIDs(t *testing.T) {
	service, backend, _, _, _, p, input := accessFixture(t)
	grant := proposeFixture(t, service, p, input)
	if grant.OrgID != p.OrgID || grant.Source.OrgID != p.OrgID || grant.OwnerID != p.UserID || grant.ID == "" || backend.export.ID == "" || grant.ApprovedRevision != "" {
		t.Fatal("caller metadata became authority")
	}
	approved, err := service.ApproveGrant(t.Context(), p, grant.ID, grant.Generation, grant.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if approved.ApprovedBy != p.UserID {
		t.Fatal("approver not derived from principal")
	}
	audit := service.destinations.audit.(*policyAudit)
	if len(audit.events) != 3 || audit.events[2].Action != "federation.grant.approve" || audit.events[2].Principal != p {
		t.Fatal("missing management audit")
	}
}
func TestAccessManagementRolesAndScope(t *testing.T) {
	service, backend, _, _, auth, p, input := accessFixture(t)
	grant := proposeFixture(t, service, p, input)
	developer := security.Principal{OrgID: p.OrgID, UserID: "developer"}
	if assignErr := auth.Assign(t.Context(), developer.OrgID, developer.UserID, security.RoleDeveloper, "test"); assignErr != nil {
		t.Fatal(assignErr)
	}
	if _, err := service.GetExport(t.Context(), developer, backend.export.ID); err != nil {
		t.Fatal("developer cannot review export")
	}
	if _, err := service.ApproveGrant(t.Context(), developer, grant.ID, grant.Generation, grant.Revision); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("developer approved grant")
	}
	if _, err := service.RevokeGrant(t.Context(), developer, grant.ID, grant.Generation); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("developer revoked grant")
	}
	if _, err := service.CreateExport(t.Context(), developer, input); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("developer published export")
	}
	writes := backend.writes
	for _, principal := range []security.Principal{{OrgID: p.OrgID}, {OrgID: p.OrgID, UserID: p.UserID, ReadOnly: true}, {OrgID: "foreign", UserID: p.UserID}} {
		if _, err := service.ApproveGrant(t.Context(), principal, grant.ID, grant.Generation, grant.Revision); err == nil {
			t.Fatal("invalid principal approved grant")
		}
	}
	if backend.writes != writes {
		t.Fatal("denied request wrote policy")
	}
	if err := auth.Assign(t.Context(), "foreign", p.UserID, security.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetGrant(t.Context(), security.Principal{OrgID: "foreign", UserID: p.UserID}, grant.ID); !errors.Is(err, access.ErrPolicyNotFound) {
		t.Fatal("organization switch exposed grant")
	}
}
func TestAccessApprovalRechecksPermissionAndIdentity(t *testing.T) {
	for _, change := range []string{"permission", "identity", "capability"} {
		t.Run(change, func(t *testing.T) {
			service, backend, r, store, auth, p, input := accessFixture(t)
			grant := proposeFixture(t, service, p, input)
			writes := backend.writes
			backend.beforeGuard = func() {
				switch change {
				case "permission":
					if err := auth.Revoke(t.Context(), p.OrgID, p.UserID); err != nil {
						t.Fatal(err)
					}
				case "identity":
					r.conn.Details["namespace_uid"] = "replacement"
				case "capability":
					d := store.rows[input.DestinationIDs[0]]
					d.Capabilities.Results[domain.PolicyEnforcement] = domain.CapabilityResult{State: domain.Unsupported}
					store.rows[d.ID] = d
				}
			}
			if _, err := service.ApproveGrant(t.Context(), p, grant.ID, grant.Generation, grant.Revision); err == nil {
				t.Fatal("stale authorization committed")
			}
			if backend.writes != writes {
				t.Fatal("stale authorization wrote policy")
			}
		})
	}
}
func TestAccessRevocationSurvivesUnavailableProvider(t *testing.T) {
	service, backend, r, _, _, p, input := accessFixture(t)
	grant := proposeFixture(t, service, p, input)
	stale := time.Now().Add(-time.Hour)
	r.conn.CheckedAt = &stale
	if _, err := service.ApproveGrant(t.Context(), p, grant.ID, grant.Generation, grant.Revision); !errors.Is(err, ErrUnverified) {
		t.Fatal("stale provider approved")
	}
	if _, err := service.RevokeGrant(t.Context(), p, grant.ID, grant.Generation); err != nil {
		t.Fatal("provider outage blocked revocation", err)
	}
	if _, err := service.RetireExport(t.Context(), p, backend.export.ID, backend.export.Generation); err != nil {
		t.Fatal("provider outage blocked retirement", err)
	}
}
func TestAccessApprovalRejectsUnqualifiedIdentityProfile(t *testing.T) {
	service, _, _, store, _, p, input := accessFixture(t)
	grant := proposeFixture(t, service, p, input)
	d := store.rows[input.DestinationIDs[0]]
	d.Capabilities.Results[domain.WorkloadAttestation] = domain.CapabilityResult{State: domain.Unsupported}
	store.rows[d.ID] = d
	if _, err := service.ApproveGrant(t.Context(), p, grant.ID, grant.Generation, grant.Revision); !errors.Is(err, ErrUnverified) {
		t.Fatal("gateway proof satisfied workload grant")
	}
}
func TestAccessManagementDisabledAndCanceled(t *testing.T) {
	service, backend, _, _, _, p, input := accessFixture(t)
	service.destinations.enabled = false
	if _, err := service.CreateExport(t.Context(), p, input); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled management wrote export")
	}
	service.destinations.enabled = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.CreateExport(ctx, p, input); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled management wrote export")
	}
	if backend.writes != 0 {
		t.Fatal("blocked request reached storage")
	}
}

func TestAccessApprovalRequiresEveryDestinationCapability(t *testing.T) {
	for _, side := range []string{"source", "target"} {
		for _, capability := range []domain.Capability{domain.WorkloadAttestation, domain.PolicyEnforcement, domain.HTTPProtocol, domain.BackendIsolation} {
			if side == "source" && capability == domain.BackendIsolation {
				continue
			}
			t.Run(side+"/"+string(capability), func(t *testing.T) {
				service, backend, _, store, _, p, input := accessFixture(t)
				grant := proposeFixture(t, service, p, input)
				source := store.rows[input.DestinationIDs[0]]
				source.ID = "source"
				source.Capabilities.Results = make(map[domain.Capability]domain.CapabilityResult)
				for cap, result := range store.rows[input.DestinationIDs[0]].Capabilities.Results {
					source.Capabilities.Results[cap] = result
				}
				store.rows[source.ID] = source
				grant.Source.DestinationID = source.ID
				var err error
				grant.Revision, err = access.GrantRevision(grant.AccessGrant)
				if err != nil {
					t.Fatal(err)
				}
				id := source.ID
				if side == "target" {
					id = input.DestinationIDs[0]
				}
				d := store.rows[id]
				delete(d.Capabilities.Results, capability)
				store.rows[id] = d
				writes := backend.writes
				if _, err := service.ApproveGrant(t.Context(), p, grant.ID, grant.Generation, grant.Revision); !errors.Is(err, ErrUnverified) {
					t.Fatalf("missing capability allowed: %v", err)
				}
				if backend.writes != writes {
					t.Fatal("unqualified approval reached storage")
				}
			})
		}
	}
}

func TestAccessApprovalRejectsStaleReview(t *testing.T) {
	for _, change := range []string{"generation", "review", "export", "expiry", "retired", "source-scope", "quarantine"} {
		t.Run(change, func(t *testing.T) {
			service, backend, _, store, _, p, input := accessFixture(t)
			grant := proposeFixture(t, service, p, input)
			expected, reviewed := grant.Generation, grant.Revision
			past := time.Now().Add(-time.Minute)
			switch change {
			case "generation":
				expected++
			case "review":
				reviewed = "stale"
			case "export":
				backend.export.Revision = "replacement"
			case "expiry":
				grant.ExpiresAt = &past
			case "retired":
				backend.export.RetiredAt = &past
			case "source-scope":
				grant.Source.EnvironmentID = "foreign"
			case "quarantine":
				d := store.rows[input.DestinationIDs[0]]
				d.Quarantined = true
				store.rows[d.ID] = d
			}
			writes := backend.writes
			if _, err := service.ApproveGrant(t.Context(), p, grant.ID, expected, reviewed); err == nil {
				t.Fatal("invalid approval accepted")
			}
			if backend.writes != writes {
				t.Fatal("invalid approval reached storage")
			}
		})
	}
}

func TestAccessDeveloperCanProposeUnqualifiedGrant(t *testing.T) {
	service, backend, _, store, auth, p, input := accessFixture(t)
	export, err := service.CreateExport(t.Context(), p, input)
	if err != nil {
		t.Fatal(err)
	}
	d := store.rows[input.DestinationIDs[0]]
	d.Capabilities = domain.CapabilitySet{}
	store.rows[d.ID] = d
	developer := security.Principal{OrgID: p.OrgID, UserID: "developer"}
	if assignErr := auth.Assign(t.Context(), developer.OrgID, developer.UserID, security.RoleDeveloper, "test"); assignErr != nil {
		t.Fatal(assignErr)
	}
	request := GrantInput{Source: domain.WorkloadSelector{Scope: export.Scope, DestinationID: d.ID, ServiceID: "checkout"}, ExportID: export.ID, ExportRevision: export.Revision, Port: export.Port, Protocol: export.Protocol}
	grant, err := service.ProposeGrant(t.Context(), developer, request)
	if err != nil || grant.ProposedBy != developer.UserID {
		t.Fatalf("proposal: %v", err)
	}
	if _, err := service.ApproveGrant(t.Context(), p, grant.ID, grant.Generation, grant.Revision); !errors.Is(err, ErrUnverified) {
		t.Fatalf("unqualified grant approved: %v", err)
	}
	request.Source.EnvironmentID = "foreign"
	writes := backend.writes
	if _, err := service.ProposeGrant(t.Context(), developer, request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign source proposed: %v", err)
	}
	if backend.writes != writes {
		t.Fatal("foreign proposal reached storage")
	}
}
