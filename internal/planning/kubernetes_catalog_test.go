package planning

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
)

func clusterCatalogConnection(t *testing.T) ConnectionOption {
	return infrastructureConnection(t, targetA, "kubernetes", map[string]string{"endpoint": "https://cluster.example", "cluster_ca": "checked-ca", "namespace": "databases", "namespace_uid": "namespace-identity", "architecture": "arm64", "storage_class": "retained", "storage_class_uid": "class-identity", "storage_provisioner": "fixture.csi", "storage_binding": "WaitForFirstConsumer", "storage_reclaim": "Retain", "persistent_storage": "ready", "postgres_recovery_permissions": "ready"})
}
func clusterCatalogPlan(t *testing.T, c ConnectionOption, bindings ...*Binding) *Result {
	t.Helper()
	manifest := parse(t, "version: 1\nresources:\n  database: {kind: postgres, version: '17', provider: kubernetes, target: "+targetA+"}\n")
	return Compute(Input{Environment: "production", Graph: &graph.Graph{}, Manifest: manifest, Providers: providers.Registry(), Connections: []ConnectionOption{c}, Bindings: bindings})
}
func TestKubernetesCatalogPinsCapacityAndPlacement(t *testing.T) {
	result := clusterCatalogPlan(t, clusterCatalogConnection(t))
	if !result.Executable || len(result.Actions) != 1 {
		t.Fatalf("cluster database unavailable: %+v", result.Problems)
	}
	action := result.Actions[0]
	cfg := action.Resource
	if cfg == nil || cfg.Catalog == nil || cfg.Catalog.VolumeSizeMB != 10240 || cfg.Catalog.ID != "postgresql-17-kubernetes" || cfg.TargetSettings["storage_class_uid"] != "class-identity" || cfg.TargetSettings["namespace_uid"] != "namespace-identity" || cfg.TargetSettings["architecture"] != "arm64" || action.ConnectionID != targetA {
		t.Fatalf("unreviewed persistent deployment: %+v", cfg)
	}
	if cfg.TargetSettings["postgres_recovery_permissions"] != "" {
		t.Fatal("volatile permission readiness became destination identity")
	}
	if !slices.ContainsFunc(result.Warnings, func(f Finding) bool { return f.Code == "retained-cluster-storage" }) {
		t.Fatal("cluster storage and network requirements are not visible for review")
	}
}
func TestKubernetesCatalogRequiresCheckedStorageAndRecoveryAccess(t *testing.T) {
	for _, key := range []string{"architecture", "namespace_uid", "storage_class_uid", "persistent_storage", "postgres_recovery_permissions"} {
		t.Run(key, func(t *testing.T) {
			connection := clusterCatalogConnection(t)
			delete(connection.Details, key)
			result := clusterCatalogPlan(t, connection)
			if result.Executable {
				t.Fatalf("database accepted unchecked %s", key)
			}
		})
	}
}
func TestKubernetesCatalogRefusesStorageRebinding(t *testing.T) {
	original := clusterCatalogConnection(t)
	first := clusterCatalogPlan(t, original)
	if !first.Executable {
		t.Fatal(first.Problems)
	}
	data, err := json.Marshal(first.Actions[0].Resource)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"storage_class_uid", "namespace_uid", "architecture"} {
		t.Run(key, func(t *testing.T) {
			next := original
			next.Details = maps.Clone(original.Details)
			next.Details[key] = "replacement-identity"
			if key == "architecture" {
				next.Details[key] = "amd64"
			}
			binding := &Binding{ID: "binding", Subject: ResourceSubject, Name: "database", Provider: "kubernetes", ConnectionID: targetA, ExternalID: "existing", Config: data}
			result := clusterCatalogPlan(t, next, binding)
			if result.Executable || !slices.ContainsFunc(result.Problems, func(f Finding) bool { return f.Code == "database-migration-required" }) {
				t.Fatalf("persistent identity changed without migration: %+v", result)
			}
		})
	}
}
