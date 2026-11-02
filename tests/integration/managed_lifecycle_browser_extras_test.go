//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	apiclient "github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

// Extra read-only inventory belongs to another Cloud/account so the primary
// fixture's real admission regression retains its original inventory scope.
func lifecycleBrowserExtras(t *testing.T, a *capacityAPI, directory string) (map[string]string, func() error) {
	t.Helper()
	if os.Getenv("LOOMPLANE_LIFECYCLE_BROWSER_EXTRAS") != "1" {
		return nil, nil
	}
	var cloud cloudprofiles.Profile
	a.c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+a.project+"/clouds", cloudprofiles.CreateInput{Name: "Lifecycle list fixture"}, &cloud)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("fixture-list-%03d", i)
		ref := provider.ClusterRef{ID: id, AccountID: "fixture-list-only", Region: "fixture", Incarnation: id}
		observation := provider.ClusterObservation{Name: fmt.Sprintf("%03d long customer workload name for readable resource identity across narrow screens and workspace pages with an intentionally-long-unbroken-resource-identifier-for-overflow-checks", i), Ref: ref, State: "running", ObservedAt: time.Now().UTC()}
		r := &cloudinfra.ManagedResource{ID: id, OrgID: a.org, CloudID: cloud.ID, Kind: "cluster", Provider: "fixture-list-only", AccountID: ref.AccountID, ExternalID: id, Incarnation: id, Ownership: "external", Status: "running", SchemaVersion: 1, Revision: 1, Observation: &observation}
		if _, err := a.store.ClaimManagedResource(t.Context(), r, 0); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := forge.InjectType[*cloudinfra.Service](a.r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := svc.Adapters.Cluster("digitalocean", provider.ClusterDiscovery)
	if err != nil {
		t.Fatal(err)
	}
	base, ok := reader.(*lifecycleBrowserProvider)
	if !ok {
		t.Fatal("browser extras require the isolated kind fixture")
	}
	adapter := &lifecycleRetirementBrowserProvider{lifecycleBrowserProvider: base}
	resource, err := a.store.GetManagedResource(t.Context(), a.org, "fixture-manual-cluster")
	if err != nil {
		t.Fatal(err)
	}
	inventory := provider.AssociatedResourceInventory{Ref: resource.Observation.Ref, Complete: true, ChildrenQualified: true, ObservedAt: time.Now().UTC()}
	adapter.retirement.Store(&inventory)
	adapters := cloudinfra.NewAdapters()
	if err = adapters.RegisterCluster("digitalocean", adapter, provider.ClusterBootstrapAccess, provider.NodePoolUpdate, provider.NodePoolAutoscaling, provider.ClusterMutationObservation, provider.ClusterDeletion); err != nil {
		t.Fatal(err)
	}
	svc.Adapters = adapters
	var retirement apiclient.ClusterRetirementReview
	a.c.must(http.StatusOK, http.MethodPost, a.path("/clusters/"+resource.ID+"/retirement/preview"), map[string]string{}, &retirement)
	var currentCloud cloudprofiles.Profile
	a.c.must(http.StatusOK, http.MethodGet, a.path(""), nil, &currentCloud)
	in := apiclient.ClusterInput{Kind: "retire-cluster", Provider: resource.Provider, ConnectionID: resource.ConnectionID, ConnectionGeneration: 1, AccountID: resource.AccountID, CloudRevision: currentCloud.Revision, ResourceID: resource.ID, ResourceRevision: resource.Revision, Ref: resource.Observation.Ref, Retirement: &apiclient.ClusterRetirementInput{ReviewDigest: retirement.Digest}, AcknowledgeUnknownCosts: true, AcknowledgeNodeCosts: true}
	var saved apiclient.CloudClusterOperation
	a.c.must(http.StatusOK, http.MethodPost, a.path("/clusters/preview"), in, &saved)
	tick := func() error {
		for _, name := range []string{"update-persisted-capacity", "change-retirement-inventory"} {
			marker := filepath.Join(directory, name)
			if _, err := os.Stat(marker); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			if name == "update-persisted-capacity" {
				snapshot, err := a.store.ReadClusterCapacity(t.Context(), a.org, a.cloud, resource.ID)
				if err != nil {
					return err
				}
				revision, err := a.store.BeginClusterCapacityObservation(t.Context(), a.org, a.cloud, resource.ID)
				if err != nil {
					return err
				}
				snapshot.ObservationRevision = revision
				snapshot.CheckedAt = time.Now().UTC()
				snapshot.Conditions = append(snapshot.Conditions, provider.ClusterCondition{Type: "fixture", Status: "observed", Reason: "Browser persisted observation updated"})
				if err = a.store.SaveClusterCapacity(t.Context(), resource, *snapshot); err != nil {
					return err
				}
			} else {
				changed := inventory
				changed.ObservedAt = time.Now().UTC()
				changed.Resources = []provider.AssociatedResource{{ID: "new-retained-volume-from-changed-inventory", Kind: "volume", Ownership: "external", SurvivesClusterDeletion: true}}
				adapter.retirement.Store(&changed)
			}
			if err := os.Remove(marker); err != nil {
				return err
			}
		}
		return nil
	}
	return map[string]string{"list_cloud_id": cloud.ID, "list_cluster_count": "100", "retirement_operation_id": saved.ID}, tick
}

type lifecycleRetirementBrowserProvider struct {
	*lifecycleBrowserProvider
	retirement atomic.Pointer[provider.AssociatedResourceInventory]
}

func (p *lifecycleRetirementBrowserProvider) AssociatedResources(_ context.Context, _ provider.AccountAccess, ref provider.ClusterRef) (provider.AssociatedResourceInventory, error) {
	inventory := p.retirement.Load()
	if inventory == nil || inventory.Ref != ref {
		return provider.AssociatedResourceInventory{}, provider.ErrInfrastructureUnsupported
	}
	return *inventory, nil
}
func (p *lifecycleRetirementBrowserProvider) DeleteCluster(context.Context, provider.AccountAccess, provider.ClusterRef, string) (provider.ClusterMutation, error) {
	return provider.ClusterMutation{}, provider.ErrInfrastructureUnsupported
}
