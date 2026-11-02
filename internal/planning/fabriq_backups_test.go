package planning

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestFabriqReviewsDailyBackupForNewDockerPostgres(t *testing.T) {
	for _, kind := range []string{"fabriq", "fabriq-library"} {
		t.Run(kind, func(t *testing.T) {
			doc := "version: 1\nresources:\n  data: {kind: " + kind + ", provider: server, target: " + targetA + "}\n"
			r := composedPlan(t, recipes.Default(), doc)
			if !r.Executable {
				t.Fatalf("blocked: %+v", r.Problems)
			}
			for _, a := range r.Actions {
				want := 0
				if a.Name == "data--postgres" {
					want = 24
					if !strings.Contains(a.Creates, "backup every 24 hours") {
						t.Fatalf("review omits schedule: %+v", a)
					}
				}
				if a.BackupEveryHours != want {
					t.Fatalf("unexpected backup schedule on %s: %d", a.Name, a.BackupEveryHours)
				}
			}
			before, err := Digest(r)
			if err != nil {
				t.Fatal(err)
			}
			findAction(r, "resource:data--postgres").BackupEveryHours = 12
			after, err := Digest(r)
			if err != nil || before == after {
				t.Fatalf("schedule is outside plan revision: %v", err)
			}
			for _, a := range r.Actions {
				raw, err := json.Marshal(a.Resource)
				if err != nil || strings.Contains(string(raw), "backup_every_hours") {
					t.Fatalf("schedule changed runtime contract: %s %v", raw, err)
				}
			}
		})
	}
}

func TestFabriqDoesNotResetSchedulesOnExistingDependencies(t *testing.T) {
	doc := "version: 1\nresources:\n  data: {kind: fabriq, provider: server, target: " + targetA + "}\n"
	first := composedPlan(t, recipes.Default(), doc)
	r := composedPlan(t, recipes.Default(), doc, boundFrom(t, first)...)
	if !r.Executable {
		t.Fatalf("blocked: %+v", r.Problems)
	}
	for _, a := range r.Actions {
		if a.BackupEveryHours != 0 {
			t.Fatalf("existing dependency gets a new default: %+v", a)
		}
	}
}

func TestFabriqKubernetesDoesNotPromiseUnqualifiedBackups(t *testing.T) {
	connection := clusterCatalogConnection(t)
	connection.Details["jobs"] = "ready"
	connection.Details["service_proxy"] = "ready"
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, "version: 1\nresources:\n  data: {kind: fabriq, provider: kubernetes, target: "+targetA+"}\n"), Providers: providers.Registry(), Connections: []ConnectionOption{connection}, Kinds: recipes.Default()})
	if !r.Executable {
		t.Fatalf("blocked: %+v", r.Problems)
	}
	for _, a := range r.Actions {
		if a.BackupEveryHours != 0 {
			t.Fatalf("Kubernetes backup claim: %+v", a)
		}
	}
}
