package cloudinfra

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

type attachmentListMemory struct {
	clusterMemory
	calls int
}

func (m *attachmentListMemory) ListClusterAttachments(_ context.Context, org, cloud, cluster string) ([]RuntimeAttachmentSummary, error) {
	m.calls++
	if org != "org" || cloud != "cloud" || cluster != "cluster" {
		return nil, ErrNotFound
	}
	return []RuntimeAttachmentSummary{{ID: "attachment", Revision: 7, Namespace: "workers", State: "active"}}, nil
}
func TestClusterAttachmentsCurrentScopedRead(t *testing.T) {
	store := &attachmentListMemory{clusterMemory: clusterMemory{resource: &ManagedResource{ID: "cluster", Kind: "cluster", OrgID: "org", CloudID: "cloud"}}}
	creds := &doksCredentials{}
	auth := overviewAuth{"manager": {{security.OrganizationCloudManage, "org"}}}
	svc := NewService(Deps{Connections: creds, Authz: auth, Clouds: capacityAPIClouds{}})
	svc.Clusters = store
	p := security.Principal{UserID: "manager", OrgID: "org"}
	page, err := svc.ListClusterAttachments(t.Context(), p, "cloud", "cluster")
	if err != nil || len(page.Items) != 1 || page.Items[0].Revision != 7 {
		t.Fatalf("current selection: %+v %v", page, err)
	}
	store.resource.CloudID = "foreign"
	if _, err = svc.ListClusterAttachments(t.Context(), p, "cloud", "cluster"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	store.resource.CloudID = "cloud"
	delete(auth, "manager")
	if _, err = svc.ListClusterAttachments(t.Context(), p, "cloud", "cluster"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	if store.calls != 1 || creds.reads != 0 {
		t.Fatalf("unexpected read or credential issuance: %d %d", store.calls, creds.reads)
	}
}
