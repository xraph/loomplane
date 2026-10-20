package deployments

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func kubernetesDatabaseConfig(t *testing.T) *planning.ResourceConfig {
	t.Helper()
	recipe, err := recipes.KubernetesPostgres("17", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	return &planning.ResourceConfig{Provider: "kubernetes", Kind: "postgres", Version: "17", Target: "selected", Catalog: recipe, TargetSettings: map[string]string{"architecture": "arm64", "namespace": "databases", "namespace_uid": "checked-namespace", "storage_class": "retained", "storage_class_uid": "checked-class", "storage_reclaim": "Retain", "persistent_storage": "ready"}}
}
func TestKubernetesDatabaseBuildsOneRetainedStatefulProfile(t *testing.T) {
	cfg, err := databaseService(kubernetesDatabaseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Connection: &connections.Connection{ID: "selected", Cloud: "kubernetes"}, Action: planning.Action{Name: "database", Service: cfg}, Env: map[string]string{"POSTGRES_DB": "database", "POSTGRES_PASSWORD": "vault-only-password"}, Secrets: map[string]bool{"POSTGRES_PASSWORD": true}}
	spec, labels := ownedSpec(job, cfg.Catalog.Image, ownedState{RuntimeRef: "runtime"})
	if len(spec.Volumes) != 1 || spec.Volumes[0].SizeMB != 10240 || labels[owned.LabelStateful] != "true" || labels[nativepg.LabelDatabase] != "database" || labels[labelImage] != cfg.Catalog.Image || spec.Env["POSTGRES_PASSWORD"] != "" || cfg.Public {
		t.Fatalf("unsafe cluster database profile: %+v %+v", spec, labels)
	}
}
func TestKubernetesDatabaseRefusesModifiedCapacityOrDestination(t *testing.T) {
	for _, change := range []string{"capacity", "namespace", "class", "engine"} {
		t.Run(change, func(t *testing.T) {
			cfg := kubernetesDatabaseConfig(t)
			switch change {
			case "capacity":
				cfg.Catalog.VolumeSizeMB = 20480
			case "namespace":
				cfg.TargetSettings["namespace"] = "foreign.namespace"
			case "class":
				delete(cfg.TargetSettings, "storage_class_uid")
			case "engine":
				cfg.Kind = "mysql"
			}
			if _, err := databaseService(cfg); err == nil {
				t.Fatalf("accepted unreviewed %s", change)
			}
		})
	}
}
func TestKubernetesDatabaseUsesReviewedPrivateServiceAddress(t *testing.T) {
	cfg := kubernetesDatabaseConfig(t)
	iid := id.New(id.PrefixInstance)
	address, err := databaseAddress(cfg, iid, "database", "secret-with+reserved@characters:1234")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if parsed.Host != kubernetes.WorkloadName(iid)+".databases.svc:5432" || parsed.Scheme != "postgresql" || parsed.Path != "/database" || password != "secret-with+reserved@characters:1234" || strings.Contains(address, "vault-only") {
		t.Fatalf("wrong database address: %s", parsed.Redacted())
	}
}
func TestDatabaseApplyRefusesWrongAdapterBeforeCredentials(t *testing.T) {
	cfg := kubernetesDatabaseConfig(t)
	cfg.Provider = "server"
	cfg.Catalog, _ = recipes.Postgres("17", "arm64")
	target := &OwnedTarget{Cloud: connections.Kubernetes}
	job := &Job{Deployment: &Deployment{OrgID: "org"}, Connection: &connections.Connection{ID: "selected", Cloud: connections.Kubernetes}, Action: planning.Action{Name: "database", Resource: cfg}, Recall: func(context.Context, string) ([]byte, error) {
		t.Fatal("resolved credentials on the wrong adapter")
		return nil, nil
	}, Keep: func(context.Context, string, []byte) error {
		t.Fatal("persisted credentials on the wrong adapter")
		return nil
	}}
	if _, err := target.Apply(t.Context(), job); err == nil {
		t.Fatal("cross-provider database accepted")
	}
}
