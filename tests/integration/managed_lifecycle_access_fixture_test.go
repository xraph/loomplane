//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/forge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	apiclient "github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

type lifecycleBrowserProvider struct {
	*maintenanceRecoveryProvider
	kind enrollmentKindProvider
}

func (p *lifecycleBrowserProvider) GetCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef) (provider.ClusterObservation, error) {
	if ref.ID == p.kind.observation.Ref.ID {
		return p.kind.GetCluster(ctx, a, ref)
	}
	return p.maintenanceRecoveryProvider.GetCluster(ctx, a, ref)
}
func (p *lifecycleBrowserProvider) BootstrapCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, ttl time.Duration) (provider.BootstrapAccess, error) {
	return p.kind.BootstrapCluster(ctx, a, ref, ttl)
}

// The optional kind fixture enrolls through the same routes used by the browser.
func lifecycleAccessFixture(t *testing.T, a *capacityAPI) string {
	t.Helper()
	path := os.Getenv("LOOMPLANE_TEST_MANAGED_KUBECONFIG")
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := kube.ParseConfig(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := kube.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	selected := embedded.Contexts[embedded.CurrentContext]
	cluster, auth := embedded.Clusters[selected.Cluster], embedded.AuthInfos[selected.AuthInfo]
	ca := sha256.Sum256(cluster.CertificateAuthorityData)
	observed := provider.ClusterObservation{Ref: provider.ClusterRef{ID: "fixture-kind", AccountID: "account", Region: "local", Incarnation: "fixture-kind-incarnation"}, Endpoint: cluster.Server, CAHash: hex.EncodeToString(ca[:]), State: "running", ObservedAt: time.Now().UTC()}
	svc, err := forge.InjectType[*cloudinfra.Service](a.r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	manual, err := a.store.GetManagedResource(t.Context(), a.org, "fixture-manual-cluster")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &lifecycleBrowserProvider{maintenanceRecoveryProvider: &maintenanceRecoveryProvider{enrollmentKindProvider: enrollmentKindProvider{observation: *manual.Observation}}, kind: enrollmentKindProvider{observation: observed, access: provider.BootstrapAccess{Endpoint: cluster.Server, CAData: cluster.CertificateAuthorityData, Token: auth.Token, ClientCertificate: auth.ClientCertificateData, ClientKey: auth.ClientKeyData, ExpiresAt: time.Now().Add(time.Hour)}}}
	adapters := cloudinfra.NewAdapters()
	if err = adapters.RegisterCluster("digitalocean", adapter, provider.ClusterBootstrapAccess, provider.NodePoolUpdate, provider.NodePoolAutoscaling, provider.ClusterMutationObservation); err != nil {
		t.Fatal(err)
	}
	svc.Adapters = adapters
	r := &cloudinfra.ManagedResource{ID: "fixture-access-cluster", OrgID: a.org, CloudID: a.cloud, ConnectionID: "capacity-do", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: observed.Ref.ID, Incarnation: observed.Ref.Incarnation, Ownership: "external", Status: "running", Revision: 1, SchemaVersion: 1, Observation: &observed}
	if _, err = a.store.ClaimManagedResource(t.Context(), r, 0); err != nil {
		t.Fatal(err)
	}
	namespace := "lifecycle-api-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_ = bootstrap.Kube.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
	})
	in := apiclient.ClusterInput{Kind: "adopt-external", Provider: "digitalocean", ConnectionID: "capacity-do", AccountID: "account", ConnectionGeneration: 1, CloudRevision: 1, ResourceID: r.ID, ResourceRevision: 1, Ref: observed.Ref, Attachment: &apiclient.AttachmentInput{ProjectID: a.project, Namespace: namespace, ServiceAccountName: "runtime", TargetSlug: "kind-runtime", CloudRevision: 1, ResourceRevision: 1, GrantRevision: 1}}
	var operation apiclient.CloudClusterOperation
	a.c.must(http.StatusOK, http.MethodPost, a.path("/clusters/preview"), in, &operation)
	a.c.must(http.StatusAccepted, http.MethodPost, a.path("/cluster-operations/"+operation.ID+"/apply"), apiclient.InfrastructureApplyInput{Review: operation.Digest}, nil)
	if err = svc.ReconcileClusterOperation(t.Context(), a.org, operation.ID, nil); err != nil {
		t.Fatal(err)
	}
	a.c.must(http.StatusOK, http.MethodGet, a.path("/cluster-operations/"+operation.ID), nil, &operation)
	if operation.Status != "succeeded" || operation.Enrollment == nil {
		t.Fatalf("kind enrollment status: %s", operation.Status)
	}
	var current apiclient.CloudCluster
	a.c.must(http.StatusOK, http.MethodGet, a.path("/clusters/"+r.ID), nil, &current)
	upgrade := apiclient.ClusterInput{Kind: cloudinfra.RuntimeAccessUpgrade, Provider: "digitalocean", ConnectionID: "capacity-do", AccountID: "account", ConnectionGeneration: 1, CloudRevision: 1, ResourceID: r.ID, ResourceRevision: current.Revision, Ref: observed.Ref, RuntimeAccess: &apiclient.RuntimeAccessReview{AttachmentID: operation.Enrollment.AttachmentID, Profile: provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true, HPA: true}}}
	var review apiclient.CloudClusterOperation
	a.c.must(http.StatusOK, http.MethodPost, a.path("/clusters/preview"), upgrade, &review)
	a.c.must(http.StatusConflict, http.MethodPost, a.path("/cluster-operations/"+review.ID+"/apply"), apiclient.InfrastructureApplyInput{Review: "stale"}, nil)
	return operation.Enrollment.AttachmentID
}
