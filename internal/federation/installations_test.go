package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type installationMemory struct {
	InstallationStore
	destinations  *memoryStore
	record        *KubernetesInstallation
	puts, retires int
}

func (s *installationMemory) GetInstallation(_ context.Context, org, id string) (*KubernetesInstallation, error) {
	if s.record == nil || s.record.Profile.OrgID != org || s.record.Profile.DestinationID != id {
		return nil, ErrNotFound
	}
	snapshot := *s.record
	return &snapshot, nil
}
func (s *installationMemory) PutInstallation(ctx context.Context, d domain.Destination, p KubernetesAttestationProfile, _ int64, actor string, checked time.Time, guard func(context.Context) error) (*KubernetesInstallation, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	s.puts++
	d.Generation++
	s.destinations.rows[d.ID] = d
	s.record = &KubernetesInstallation{Profile: p, Generation: d.Generation, RecordedBy: actor, CheckedAt: checked}
	return s.record, nil
}
func (s *installationMemory) RetireInstallation(ctx context.Context, _, _ string, _ int64, _ string, guard func(context.Context) error) error {
	if err := guard(ctx); err != nil {
		return err
	}
	s.retires++
	s.record = nil
	return nil
}
func TestInstallationServiceAuthorizesBeforeVerification(t *testing.T) {
	destinations, _, rows, principal, input := fixture(t)
	d, err := destinations.CreateDestination(t.Context(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	store := &installationMemory{destinations: rows}
	calls := 0
	verify := func(_ context.Context, actual domain.Destination, p KubernetesAttestationProfile) (workloadidentity.Binding, error) {
		calls++
		if p.Scope != d.Scope || p.DestinationID != d.ID || p.ConnectionID != d.ConnectionID || p.IdentityRevision != d.Identity.Revision() {
			t.Fatal("server scope not derived")
		}
		return workloadidentity.Binding{Destination: actual, CheckedAt: time.Now(), Method: "kubernetes", PodUID: p.PodUID, ServiceAccount: p.ServiceAccount, Container: p.Container}, nil
	}
	svc := NewInstallationService(destinations, store, verify)
	request := InstallationInput{DestinationID: d.ID, DestinationGeneration: d.Generation, PodName: "agent", PodUID: "pod-uid", ServiceAccount: "agent", Container: "agent", SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	for _, change := range []func(*security.Principal){func(p *security.Principal) { p.UserID = "" }, func(p *security.Principal) { p.ReadOnly = true }, func(p *security.Principal) { p.OrgID = "foreign" }} {
		bad := principal
		change(&bad)
		if _, recordErr := svc.Record(t.Context(), bad, request); recordErr == nil {
			t.Fatal("unauthorized installation recorded")
		}
	}
	destinations.authz = &authorizer{deny: true}
	if _, recordErr := svc.Record(t.Context(), principal, request); recordErr == nil {
		t.Fatal("permission bypassed")
	}
	if calls != 0 || store.puts != 0 {
		t.Fatal("denied caller reached provider")
	}
	auth := &authorizer{}
	destinations.authz = auth
	r, err := svc.Record(t.Context(), principal, request)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordedBy != principal.UserID || calls != 1 || store.puts != 1 {
		t.Fatal("installation was not recorded")
	}
	if auth.calls[0] != security.DestinationManage || auth.calls[1] != security.TrustManage {
		t.Fatal("missing trust permission", auth.calls)
	}
	current := rows.rows[d.ID]
	if _, _, resolveErr := svc.Resolve(t.Context(), current); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if calls != 2 {
		t.Fatal("enrollment reused stale attestation")
	}
	current.Generation++
	if _, _, resolveErr := svc.Resolve(t.Context(), current); resolveErr == nil {
		t.Fatal("stale installation resolved")
	}
}
func TestInstallationServiceRechecksPermissionAfterProviderRead(t *testing.T) {
	destinations, resources, rows, principal, input := fixture(t)
	d, err := destinations.CreateDestination(t.Context(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	auth := &authorizer{}
	destinations.authz = auth
	store := &installationMemory{destinations: rows}
	svc := NewInstallationService(destinations, store, func(_ context.Context, d domain.Destination, p KubernetesAttestationProfile) (workloadidentity.Binding, error) {
		auth.deny = true
		return workloadidentity.Binding{Destination: d, Method: "kubernetes", CheckedAt: time.Now(), PodUID: p.PodUID, ServiceAccount: p.ServiceAccount, Container: p.Container}, nil
	})
	request := InstallationInput{DestinationID: d.ID, DestinationGeneration: d.Generation, PodName: "agent", PodUID: "pod-uid", ServiceAccount: "agent", Container: "agent", SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	if _, recordErr := svc.Record(t.Context(), principal, request); !errors.Is(recordErr, security.ErrForbidden) || store.puts != 0 {
		t.Fatal("revoked permission wrote installation", recordErr)
	}
	auth.deny = false
	resources.conn.CheckedAt = nil
	if retireErr := svc.Retire(t.Context(), principal, d.ID, 1); retireErr != nil || store.retires != 1 {
		t.Fatal("stale provider blocked retirement", retireErr)
	}
}
