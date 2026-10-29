//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type retirementRecoveryProvider struct {
	maintenanceRecoveryProvider
	gone, lost   bool
	deferAbsence bool
	deletes      int
}

func (p *retirementRecoveryProvider) AssociatedResources(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.AssociatedResourceInventory, error) {
	return provider.AssociatedResourceInventory{Ref: p.observation.Ref, Complete: true, ChildrenQualified: true, ObservedAt: time.Now(), Resources: []provider.AssociatedResource{{ID: "retained-volume", Kind: "volume", Ownership: "external", SurvivesClusterDeletion: true}}}, nil
}
func (p *retirementRecoveryProvider) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	if p.gone {
		return provider.ClusterObservation{}, provider.ErrInfrastructureNotFound
	}
	return p.observation, nil
}
func (p *retirementRecoveryProvider) DeleteCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, expected string) (provider.ClusterMutation, error) {
	if expected != ref.Incarnation {
		return provider.ClusterMutation{}, provider.ErrInfrastructureIdentity
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.ClusterMutation{}, err
	}
	p.deletes++
	p.gone = !p.deferAbsence
	if p.lost {
		return provider.ClusterMutation{}, provider.ErrInfrastructureAmbiguous
	}
	return provider.ClusterMutation{Kind: "retire-cluster", Ref: ref}, nil
}
func TestManagedClusterRetirementPersistedRecovery(t *testing.T) {
	for _, lost := range []bool{true, false} {
		t.Run(map[bool]string{true: "response_loss", false: "confirmed"}[lost], func(t *testing.T) {
			db := managedClusterDB(t)
			store, clouds := managedClusterStore(t, db)
			pg := openPg(t, db.Config().ConnString())
			if _, err := db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET cloud='digitalocean',generation=1,details='{"account_uuid":"account"}' WHERE id='conn'`); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			ref := provider.ClusterRef{ID: "physical", AccountID: "account", Region: "nyc1", Incarnation: "birth"}
			observed := provider.ClusterObservation{Ref: ref, State: "running", Endpoint: "https://cluster.test", CAHash: "ca", Pools: []provider.NodePoolSpec{{ID: "pool", Name: "workers", Size: "s-2", Count: 2, Labels: map[string]string{}, Taints: []provider.NodePoolTaint{}}}, ObservedAt: now}
			original := &cloudinfra.ClusterOperation{ID: "original", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "original", Input: cloudinfra.ClusterInput{Kind: "create", Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", CloudRevision: 1, ConnectionGeneration: 1, Spec: provider.ClusterSpec{OperationID: "original"}}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
			if err := store.CreateClusterPreview(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			receipt := provider.ClusterMutation{OperationID: original.ID, Kind: "create", Ref: ref, RequestDigest: provider.ClusterRequestDigest(original.Input.Spec)}
			original.ResourceID = "cluster"
			original.Receipts = []provider.ClusterMutation{receipt}
			original.Status = "succeeded"
			original.Revision++
			r := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", ConnectionID: "conn", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "managed", Status: "running", Revision: 1, ObservedRevision: 1, SchemaVersion: 1, Observation: &observed, CreationReceipt: &receipt}
			if _, err := store.ClaimManagedResource(t.Context(), r, 0); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveClusterCheckpoint(t.Context(), original, 1); err != nil {
				t.Fatal(err)
			}

			adapter := &retirementRecoveryProvider{maintenanceRecoveryProvider: maintenanceRecoveryProvider{enrollmentKindProvider: enrollmentKindProvider{observation: observed}}, lost: lost, deferAbsence: !lost}
			makeService := func() *cloudinfra.Service {
				adapters := cloudinfra.NewAdapters()
				if err := adapters.RegisterCluster("digitalocean", adapter, provider.ClusterDeletion, provider.ClusterMutationObservation); err != nil {
					t.Fatal(err)
				}
				return cloudinfra.NewService(cloudinfra.Deps{Clusters: cloudinfra.NewPgStore(pg), Clouds: clouds, Connections: maintenanceRecoveryCredentials{connections.NewPgStore(pg)}, Authz: enrollmentServiceAuth{}, Adapters: adapters, ManagedClusters: config.ManagedClustersConfig{Enabled: true}})
			}
			svc := makeService()
			principal := security.Principal{OrgID: "org", UserID: "manager"}
			review, err := svc.PreviewClusterRetirement(t.Context(), principal, "cloud", "cluster")
			if err != nil {
				t.Fatal(err)
			}
			in := cloudinfra.ClusterInput{Kind: "retire-cluster", Provider: "digitalocean", ConnectionID: "conn", ConnectionGeneration: 1, AccountID: "account", CloudRevision: 1, ResourceID: "cluster", ResourceRevision: 1, Ref: ref, Retirement: &cloudinfra.ClusterRetirementInput{ReviewDigest: review.Digest}}
			op, err := svc.PreviewCluster(t.Context(), principal, "cloud", in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.ApplyCluster(t.Context(), principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
				t.Fatal(err)
			}
			if err = svc.ReconcileClusterOperation(t.Context(), "org", op.ID, nil); err != nil {
				t.Fatal(err)
			}
			if !lost {
				adapter.gone = true
				drift, driftErr := svc.ObserveClusterDrift(t.Context(), principal, "cloud", "cluster")
				if driftErr != nil {
					t.Fatal(driftErr)
				}
				if drift.Status != "missing" || drift.Revision <= in.ResourceRevision {
					t.Fatal("missing observation was not persisted")
				}
			}
			{
				if _, err = db.Exec(t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(jsonb_set(body,'{lease_expires_at}',to_jsonb($2::text)),'{next_attempt_at}',to_jsonb($2::text)) WHERE id=$1`, op.ID, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
				if err = makeService().ReconcileClusterOperation(t.Context(), "org", op.ID, nil); err != nil {
					t.Fatal(err)
				}
			}
			saved, err := store.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
			if err != nil {
				t.Fatal(err)
			}
			current, err := store.GetManagedResource(t.Context(), "org", "cluster")
			if err != nil {
				t.Fatal(err)
			}
			if adapter.deletes != 1 {
				t.Fatal("blind provider repeat", adapter.deletes)
			}
			if lost {
				if saved.Status != "needs_attention" || current.DeletedAt != nil {
					t.Fatal(saved.Status, current)
				}
			} else {
				if saved.Status != "succeeded" || current.DeletedAt == nil || len(current.RetainedResources) != 1 || current.RetainedResources[0].ID != "retained-volume" {
					t.Fatal(saved.Status, current)
				}
			}
			var retained string
			if err = db.QueryRow(t.Context(), `SELECT body->'input'->'retirement'->'inventory'->'resources'->0->>'id' FROM loomplane_cluster_retirement_receipts WHERE operation_id=$1`, op.ID).Scan(&retained); err != nil || retained != "retained-volume" {
				t.Fatal("retained inventory receipt", retained, err)
			}
		})
	}
}
