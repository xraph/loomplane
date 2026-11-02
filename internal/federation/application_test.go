package federation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type applicationJournalFixture struct {
	record          *workloadidentity.WorkloadRecord
	issued, revoked int
	afterIssue      func()
}

func (j *applicationJournalFixture) Reserve(ctx context.Context, in workloadidentity.WorkloadIntent, guard func(context.Context) error) (*workloadidentity.WorkloadRecord, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	j.record = &workloadidentity.WorkloadRecord{ID: "record", Intent: in, Phase: "pending", Settled: true}
	j.record.Intent.Workload.Binding.EntryID = "entry"
	return j.record, nil
}
func (j *applicationJournalFixture) Get(_ context.Context, org, id string) (*workloadidentity.WorkloadRecord, error) {
	if j.record == nil || j.record.ID != id || j.record.Intent.Controller.OrgID != org {
		return nil, ErrNotFound
	}
	raw, _ := json.Marshal(j.record)
	var restored workloadidentity.WorkloadRecord
	if err := json.Unmarshal(raw, &restored); err != nil {
		return nil, err
	}
	restored.Phase, restored.Settled = j.record.Phase, j.record.Settled
	return &restored, nil
}
func (j *applicationJournalFixture) Issue(ctx context.Context, _, _ string, _ workloadidentity.WorkloadIssuer, guard func(context.Context) error) error {
	if err := guard(ctx); err != nil {
		return err
	}
	j.issued++
	j.record.Phase = "ready"
	if j.afterIssue != nil {
		j.afterIssue()
	}
	return nil
}
func (j *applicationJournalFixture) Revoke(_ context.Context, _, _ string, _ workloadidentity.WorkloadRevoker) error {
	j.revoked++
	j.record.Phase = "revoking"
	return nil
}

type applicationAuthorityFixture struct{}

func (applicationAuthorityFixture) RegisterWorkload(context.Context, workloadidentity.WorkloadBinding) (*spiretypes.Entry, error) {
	return nil, nil
}
func (applicationAuthorityFixture) Revoke(context.Context, string) error { return nil }

func TestApplicationIssuanceReloadsAuthorityAndEvidence(t *testing.T) {
	for _, scenario := range []string{"valid", "unqualified", "anonymous", "readonly", "permission revoked", "pod replaced", "binding changed", "revoked after issue"} {
		t.Run(scenario, func(t *testing.T) {
			destinations, _, store, p, in := fixture(t)
			d, err := destinations.CreateDestination(t.Context(), p, in)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			d.Capabilities = domain.CapabilitySet{Revision: "tested", IdentityRevision: d.Identity.Revision(), CheckedAt: now, ExpiresAt: now.Add(time.Minute), Results: map[domain.Capability]domain.CapabilityResult{domain.WorkloadAttestation: {State: domain.Supported}}}
			if scenario == "unqualified" {
				d.Capabilities = domain.CapabilitySet{}
			}
			store.rows[d.ID] = *d
			journal := &applicationJournalFixture{}
			auth := &authorizer{}
			destinations.authz = auth
			target := ApplicationTarget{BindingID: "binding", PodName: "api-pod", PodUID: "pod-uid"}
			intent := workloadidentity.WorkloadIntent{BindingID: target.BindingID, PodName: target.PodName, Controller: kube.ControllerReceipt{Scope: d.Scope, ConnectionID: d.ConnectionID, ServiceID: "api"}, Workload: workloadidentity.WorkloadBinding{Binding: workloadidentity.Binding{Destination: *d, PodUID: target.PodUID, Method: "kubernetes", CheckedAt: now, ParentURI: "spiffe://example.test/parent"}, Workload: domain.WorkloadSelector{Scope: d.Scope, DestinationID: d.ID, ServiceID: "api"}, ContainerImage: "image"}}
			calls := 0
			resolver := func(_ context.Context, current domain.Destination, got ApplicationTarget) (workloadidentity.WorkloadIntent, error) {
				calls++
				if current.ID != d.ID || got != target {
					t.Fatal("resolver input not pinned")
				}
				intent.ImageIndexes = [][]byte{}
				return intent, nil
			}
			service := NewApplicationService(destinations, journal, resolver, applicationAuthorityFixture{})
			if scenario == "anonymous" {
				p.UserID = ""
			}
			if scenario == "readonly" {
				p.ReadOnly = true
			}
			record, err := service.Reserve(t.Context(), p, d.ID, target)
			if scenario == "unqualified" || scenario == "anonymous" || scenario == "readonly" {
				if err == nil || calls != 0 || journal.record != nil {
					t.Fatal("denied request reached resolver/journal")
				}
				return
			}
			if err != nil || record.Phase != "pending" || journal.issued != 0 || record.Intent.Workload.PruneAfter.IsZero() {
				t.Fatal("reservation bypassed journal", err)
			}
			switch scenario {
			case "permission revoked":
				auth.deny = true
			case "pod replaced":
				intent.Workload.Binding.PodUID = "replacement"
			case "binding changed":
				intent.Controller.RuntimeRevision = "replacement"
			case "revoked after issue":
				journal.afterIssue = func() { auth.deny = true }
			}
			got, err := service.Issue(t.Context(), p, record.ID)
			if scenario == "valid" {
				if err != nil || got.Phase != "ready" || journal.issued != 1 || calls < 3 {
					t.Fatal("issuance not reverified", err, calls)
				}
				if err = service.Revoke(t.Context(), p, record.ID); err != nil || journal.revoked != 1 {
					t.Fatal("revoke failed", err)
				}
			} else {
				if err == nil || got != nil {
					t.Fatal("changed authority/evidence accepted")
				}
				if scenario == "revoked after issue" && journal.revoked != 1 {
					t.Fatal("post-issue denial did not revoke")
				}
			}
		})
	}
}
