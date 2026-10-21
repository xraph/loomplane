package federation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type readyFixture struct {
	record *workloadidentity.EnrollmentRecord
	fail   bool
}

func (r *readyFixture) Ready(_ context.Context, org, id string) (*workloadidentity.EnrollmentRecord, error) {
	if r.fail || r.record == nil || r.record.Binding.Agent.OrgID != org || r.record.ID != id {
		return nil, workloadidentity.ErrIdentity
	}
	copyRecord := *r.record
	return &copyRecord, nil
}
func launchServiceFixture(t *testing.T) (*LaunchService, *readyFixture, *installationMemory, security.Principal, LaunchDeliveryInput, *int) {
	t.Helper()
	destinations, _, rows, principal, input := fixture(t)
	d, err := destinations.CreateDestination(t.Context(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d.Capabilities = domain.CapabilitySet{Revision: "qualified", IdentityRevision: d.Identity.Revision(), CheckedAt: now, ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{domain.WorkloadAttestation: {State: domain.Supported}}}
	rows.rows[d.ID] = *d
	attempt := uuid.NewString()
	cfg := config.FederationLaunchConfig{Enabled: true, ControlEndpoint: "https://control.example:9443", AgentImage: "registry.example/agent@sha256:" + strings.Repeat("a", 64), TrustGeneration: 1}
	plan, err := agentinstall.NewKubernetesPlan("workloads", attempt, cfg.AgentImage, false)
	if err != nil {
		t.Fatal(err)
	}
	pod := plan.Pod()
	profile := KubernetesAttestationProfile{Scope: d.Scope, DestinationID: d.ID, ConnectionID: d.ConnectionID, IdentityRevision: d.Identity.Revision(), PodName: pod.Name, PodUID: "pod-uid", ServiceAccount: pod.Spec.ServiceAccountName, Container: agentinstall.ContainerName, SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	store := &installationMemory{destinations: rows, record: &KubernetesInstallation{Profile: profile, Generation: d.Generation}}
	b := workloadidentity.Binding{Destination: *d, CheckedAt: now, Method: "kubernetes", Namespace: "workloads", NamespaceUID: d.Identity.NamespaceUID, PodUID: profile.PodUID, ServiceAccount: profile.ServiceAccount, Container: profile.Container, ParentURI: "spiffe://loomplane.test/spire/agent/k8s_psat/cluster/node"}
	installations := NewInstallationService(destinations, store, func(context.Context, domain.Destination, KubernetesAttestationProfile) (workloadidentity.Binding, error) {
		return b, nil
	})
	identity := domain.AgentIdentity{Scope: d.Scope, DestinationID: d.ID, AgentID: uuid.NewString(), Epoch: 1}
	identity.URI, err = workloadidentity.AgentURI("loomplane.test", identity)
	if err != nil {
		t.Fatal(err)
	}
	enrolled := b
	enrolled.Agent = identity
	ready := &readyFixture{record: &workloadidentity.EnrollmentRecord{ID: uuid.NewString(), Binding: enrolled, Registration: runtimecatalog.Registration{Agent: identity, DestinationGeneration: d.Generation, Active: true}, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := func(context.Context, domain.Scope) (access.SnapshotKey, error) {
		return access.SnapshotKey{PublicKey: public, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}, nil
	}
	svc := NewLaunchService(installations, ready, nil, keys, cfg, principal.OrgID)
	writes := new(int)
	svc.deliver = func(ctx context.Context, _ *agentinstall.KubernetesPlan, _ domain.Destination, r workloadidentity.EnrollmentRecord, s agentinstall.LaunchSettings, guard func(context.Context) error) (*corev1.ConfigMap, error) {
		if err := guard(ctx); err != nil {
			return nil, err
		}
		if r.ID != ready.record.ID || s.ControlEndpoint != cfg.ControlEndpoint || len(s.SigningKeys) != 1 {
			t.Fatal("delivery lost server-owned launch inputs")
		}
		*writes++
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, UID: "config-uid"}}, nil
	}
	return svc, ready, store, principal, LaunchDeliveryInput{DestinationID: d.ID, EnrollmentID: ready.record.ID, AttemptID: attempt}, writes
}
func TestLaunchServiceAuthorizesAndUsesReadyEnrollment(t *testing.T) {
	svc, _, _, p, in, writes := launchServiceFixture(t)
	for _, change := range []func(*security.Principal){func(p *security.Principal) { p.UserID = "" }, func(p *security.Principal) { p.OrgID = "foreign" }, func(p *security.Principal) { p.ReadOnly = true }} {
		caller := p
		change(&caller)
		if _, err := svc.Deliver(t.Context(), caller, in); err == nil {
			t.Fatal("unauthorized launch delivered")
		}
	}
	if *writes != 0 {
		t.Fatal("denied caller reached writer")
	}
	result, err := svc.Deliver(t.Context(), p, in)
	if err != nil || result.ConfigMapUID != "config-uid" || *writes != 1 {
		t.Fatal("authorized launch did not use trusted inputs", err)
	}
}
func TestLaunchServiceRechecksAuthorityBeforeDelivery(t *testing.T) {
	for _, change := range []string{"permission", "installation", "enrollment", "capability", "listener", "parent"} {
		t.Run(change, func(t *testing.T) {
			svc, ready, store, p, in, writes := launchServiceFixture(t)
			original := svc.installations.verify
			svc.installations.verify = func(ctx context.Context, d domain.Destination, profile KubernetesAttestationProfile) (workloadidentity.Binding, error) {
				b, err := original(ctx, d, profile)
				switch change {
				case "permission":
					svc.installations.destinations.authz = &authorizer{deny: true}
				case "installation":
					store.record.Generation++
				case "enrollment":
					ready.fail = true
				case "capability":
					d.Capabilities = domain.CapabilitySet{}
					store.destinations.rows[d.ID] = d
				case "listener":
					svc.keys = func(context.Context, domain.Scope) (access.SnapshotKey, error) {
						return access.SnapshotKey{}, errors.New("listener stopped")
					}
				case "parent":
					b.ParentURI = "spiffe://loomplane.test/spire/agent/k8s_psat/cluster/other"
				}
				return b, err
			}
			if _, err := svc.Deliver(t.Context(), p, in); err == nil || *writes != 0 {
				t.Fatal("changed authority reached delivery", err)
			}
		})
	}
}
func TestLaunchServiceRejectsUnboundRequests(t *testing.T) {
	for _, change := range []string{"attempt", "enrollment destination", "export", "registration", "disabled"} {
		t.Run(change, func(t *testing.T) {
			svc, ready, _, p, in, writes := launchServiceFixture(t)
			switch change {
			case "attempt":
				in.AttemptID = uuid.NewString()
			case "enrollment destination":
				ready.record.Binding.Destination.ID = "other"
			case "export":
				ready.record.Registration.Exports = map[string]runtimecatalog.ExportBounds{"unexpected": {}}
			case "registration":
				ready.record.Registration.Agent.Epoch++
			case "disabled":
				svc.cfg.Enabled = false
			}
			if _, err := svc.Deliver(t.Context(), p, in); err == nil || *writes != 0 {
				t.Fatal("unbound launch delivered", err)
			}
		})
	}
}
