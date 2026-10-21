package federation

import (
	"context"
	"testing"
	"time"

	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type enrollmentBackend struct {
	reserved int
	issued   bool
	binding  workloadidentity.Binding
}

func (b *enrollmentBackend) Reserve(_ context.Context, binding workloadidentity.Binding, _ map[string]runtimecatalog.ExportBounds) (*workloadidentity.EnrollmentRecord, error) {
	b.reserved++
	b.binding = binding
	return &workloadidentity.EnrollmentRecord{ID: "enrollment", Binding: binding, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (b *enrollmentBackend) Issue(ctx context.Context, _, _ string, issuer workloadidentity.EnrollmentIssuer) error {
	if issuer != nil {
		if _, err := issuer.Register(ctx, b.binding); err != nil {
			return err
		}
	}
	b.issued = true
	return nil
}
func (b *enrollmentBackend) Revoke(context.Context, string, string, workloadidentity.EnrollmentIssuer) error {
	return nil
}

func TestEnrollmentRequiresQualifiedDestination(t *testing.T) {
	svc, resources, store, p, input := fixture(t)
	d, err := svc.CreateDestination(t.Context(), p, input)
	if err != nil {
		t.Fatal(err)
	}
	backend := &enrollmentBackend{}
	calls := 0
	resolve := func(_ context.Context, d domain.Destination) (workloadidentity.Binding, map[string]runtimecatalog.ExportBounds, error) {
		calls++
		return workloadidentity.Binding{Destination: d, Method: "kubernetes"}, nil, nil
	}
	controller := NewEnrollmentService(svc, backend, resolve, &enrollmentIssuerFixture{})
	request := domain.EnrollmentRequest{DestinationID: d.ID}
	if _, err = controller.Enroll(t.Context(), p, request); err == nil || calls != 0 {
		t.Fatal("unqualified destination reached enrollment")
	}
	now := time.Now()
	d.Capabilities = domain.CapabilitySet{Revision: "tested", IdentityRevision: d.Identity.Revision(), CheckedAt: now, ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{domain.WorkloadAttestation: {State: domain.Supported}}}
	store.rows[d.ID] = *d
	anonymous := p
	anonymous.UserID = ""
	if _, err = controller.Enroll(t.Context(), anonymous, request); err == nil {
		t.Fatal("runtime principal reached management enrollment")
	}
	foreign := p
	foreign.OrgID = "other"
	if _, err = controller.Enroll(t.Context(), foreign, request); err == nil {
		t.Fatal("cross tenant enrollment")
	}
	svc.authz = &authorizer{deny: true}
	if _, err = controller.Enroll(t.Context(), p, request); err == nil {
		t.Fatal("permission denial bypassed")
	}
	auth := &authorizer{}
	svc.authz = auth
	if _, err = controller.Enroll(t.Context(), p, request); err != nil {
		t.Fatal(err)
	}
	if backend.reserved != 1 || !backend.issued || backend.binding.Destination.ID != d.ID {
		t.Fatal("server destination was not used")
	}
	if len(auth.calls) < 6 || auth.calls[0] != security.DestinationManage || auth.calls[1] != security.TrustManage {
		t.Fatalf("missing management permission: %+v", auth.calls)
	}
	resources.conn.Details["namespace_uid"] = "replacement"
	if _, err = controller.Enroll(t.Context(), p, request); err == nil {
		t.Fatal("changed physical identity enrolled")
	}
}

func TestEnrollmentRechecksAuthorityAfterAttestationAndIssuance(t *testing.T) {
	for _, stage := range []string{"attestation", "issuance"} {
		t.Run(stage, func(t *testing.T) {
			svc, _, store, p, input := fixture(t)
			d, err := svc.CreateDestination(t.Context(), p, input)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			d.Capabilities = domain.CapabilitySet{Revision: "tested", IdentityRevision: d.Identity.Revision(), CheckedAt: now, ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{domain.WorkloadAttestation: {State: domain.Supported}}}
			store.rows[d.ID] = *d
			auth := &authorizer{}
			svc.authz = auth
			backend := &enrollmentBackend{}
			issuer := &enrollmentIssuerFixture{onRegister: func() {
				if stage == "issuance" {
					auth.deny = true
				}
			}}
			resolver := func(_ context.Context, d domain.Destination) (workloadidentity.Binding, map[string]runtimecatalog.ExportBounds, error) {
				if stage == "attestation" {
					auth.deny = true
				}
				return workloadidentity.Binding{Destination: d, Method: "kubernetes"}, nil, nil
			}
			controller := NewEnrollmentService(svc, backend, resolver, issuer)
			if _, enrollErr := controller.Enroll(t.Context(), p, domain.EnrollmentRequest{DestinationID: d.ID}); enrollErr == nil {
				t.Fatal("revoked authority issued enrollment")
			}
			if backend.issued {
				t.Fatal("revoked authority marked enrollment ready")
			}
		})
	}
}

type enrollmentIssuerFixture struct{ onRegister func() }

func (i *enrollmentIssuerFixture) Register(_ context.Context, b workloadidentity.Binding) (*spiretypes.Entry, error) {
	if i.onRegister != nil {
		i.onRegister()
	}
	return &spiretypes.Entry{Id: b.EntryID}, nil
}
func (*enrollmentIssuerFixture) Revoke(context.Context, string) error { return nil }
