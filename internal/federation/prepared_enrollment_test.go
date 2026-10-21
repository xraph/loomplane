package federation

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type preparedEnrollmentBackend struct {
	enrollmentBackend
	preparationID string
	actor         string
	afterIssue    func()
}

func (b *preparedEnrollmentBackend) ReservePrepared(ctx context.Context, binding workloadidentity.Binding, exports map[string]runtimecatalog.ExportBounds, id, actor string, guard func(context.Context) error) (*workloadidentity.EnrollmentRecord, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	b.preparationID = id
	b.actor = actor
	return b.Reserve(ctx, binding, exports)
}
func TestPreparedEnrollmentUsesSharedAuthorityChecks(t *testing.T) {
	svc, _, store, p, input := fixture(t)
	d, err := svc.CreateDestination(t.Context(), p, input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d.Capabilities = domain.CapabilitySet{Revision: "qualified", IdentityRevision: d.Identity.Revision(), CheckedAt: now, ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{domain.WorkloadAttestation: {State: domain.Supported}}}
	store.rows[d.ID] = *d
	backend := &preparedEnrollmentBackend{}
	issuer := &enrollmentIssuerFixture{}
	resolve := func(_ context.Context, d domain.Destination) (workloadidentity.Binding, map[string]runtimecatalog.ExportBounds, error) {
		return workloadidentity.Binding{Destination: d, Method: "kubernetes"}, nil, nil
	}
	service := NewEnrollmentService(svc, backend, resolve, issuer)
	result, err := service.EnrollPreparation(t.Context(), p, d.ID, "preparation")
	if err != nil || result.ID != "enrollment" || backend.preparationID != "preparation" || backend.actor != p.UserID || !backend.issued {
		t.Fatal("missing prepared enrollment", err)
	}
	backend.afterIssue = func() { svc.authz = &authorizer{deny: true} }
	if _, err = service.EnrollPreparation(t.Context(), p, d.ID, "preparation"); err == nil {
		t.Fatal("revoked replay returned enrollment")
	}
	backend.afterIssue = nil
	svc.authz = &authorizer{}
	backend.issued = false
	issuer.onRegister = func() { svc.authz = &authorizer{deny: true} }
	if _, err = service.EnrollPreparation(t.Context(), p, d.ID, "preparation"); err == nil || backend.issued {
		t.Fatal("revoked authority issued prepared enrollment")
	}
}

func (b *preparedEnrollmentBackend) Issue(ctx context.Context, org, id string, issuer workloadidentity.EnrollmentIssuer) error {
	if err := b.enrollmentBackend.Issue(ctx, org, id, issuer); err != nil {
		return err
	}
	if b.afterIssue != nil {
		b.afterIssue()
	}
	return nil
}
