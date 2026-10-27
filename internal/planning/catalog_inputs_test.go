package planning

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func inputsPlan(t *testing.T, inputs map[string]map[string]string, bindings ...*Binding) *Result {
	t.Helper()
	return Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, sampleApp), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: recipestest.Registry(t), Inputs: inputs, Bindings: bindings,
	})
}

func TestInputsLandOnComponentConfigs(t *testing.T) {
	r := inputsPlan(t, map[string]map[string]string{"app": {"api": "sha256:a"}})
	if !r.Executable {
		t.Fatalf("problems: %+v", r.Problems)
	}
	want := map[string]string{"resource:app--api": "sha256:a", "resource:app--migrate": "", "resource:app--worker": ""}
	for id, inputs := range want {
		a := findAction(r, id)
		if a == nil || a.Resource.Component == nil || a.Resource.Component.Inputs != inputs {
			t.Fatalf("%s: %+v, want inputs %q", id, a, inputs)
		}
	}
}

func TestOutsideInputChangePlansAnUpdate(t *testing.T) {
	bound := boundFrom(t, inputsPlan(t, map[string]map[string]string{"app": {"api": "sha256:a"}}))

	r := inputsPlan(t, map[string]map[string]string{"app": {"api": "sha256:b"}}, bound...)
	api := findAction(r, "resource:app--api")
	if api.Op != Update || api.Reason != "app--api's configuration changed." {
		t.Fatalf("api: op %s, reason %q", api.Op, api.Reason)
	}
	for _, id := range []string{"resource:app--migrate", "resource:app--worker", "resource:app"} {
		if a := findAction(r, id); a.Op != Keep {
			t.Fatalf("%s: op %s, reason %q", id, a.Op, a.Reason)
		}
	}

	r = inputsPlan(t, map[string]map[string]string{"app": {"api": "sha256:a"}}, bound...)
	for _, a := range r.Actions {
		if a.Op != Keep {
			t.Fatalf("same digest: %s op %s, reason %q", a.ID, a.Op, a.Reason)
		}
	}
}

// digests is a CatalogInputs that answers from a map, records what it was
// asked, and fails with err when that's set.
type digests struct {
	out   map[string]string
	err   error
	asked []string
}

func (d *digests) Digest(_ context.Context, orgID, projectID, environmentID, resource string) (map[string]string, error) {
	d.asked = append(d.asked, orgID+"/"+projectID+"/"+environmentID+"/"+resource)
	return d.out, d.err
}

// planningStore holds one plan in planning and records why it failed.
type planningStore struct {
	catalogStore
	plan   *Plan
	failed string
}

func (s *planningStore) GetPlan(context.Context, string, string) (*Plan, error) {
	p := *s.plan
	return &p, nil
}

func (s *planningStore) FailPlanning(_ context.Context, _, _, reason string, _ time.Time) error {
	s.failed = reason
	return nil
}

func inputsService(t *testing.T, store Store, doc string, inputs CatalogInputs, features map[string]bool) *Service {
	t.Helper()
	return NewService(Deps{
		Store: store, Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}, Providers: providers.Registry(),
		Connections: serverClouds{}, Kinds: featuredKinds(t), Features: features,
		CatalogInputs: CatalogInputMap{"sample": inputs, "featured": inputs},
	})
}

func TestServiceAsksEachEnabledResourceForItsInputs(t *testing.T) {
	doc := "version: 1\nresources:\n" +
		"  app: {kind: sample, provider: server, target: " + targetA + "}\n" +
		"  off: {kind: sample, provider: server, target: " + targetA + ", environments: {production: {enabled: false}}}\n" +
		"  helper: {kind: featured, provider: server, target: " + targetB + "}\n" +
		"  db: {kind: postgres, provider: server, target: " + targetA + "}\n"
	inputs := &digests{out: map[string]string{"api": "sha256:a"}}
	plan := &Plan{Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}

	// The feature is off, so helper isn't asked; off isn't in production.
	result, _, err := inputsService(t, catalogStore{}, doc, inputs, nil).compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inputs.asked, []string{"org/project/env/app"}) {
		t.Fatalf("asked %v", inputs.asked)
	}
	if a := findAction(result, "resource:app--api"); a == nil || a.Resource.Component.Inputs != "sha256:a" {
		t.Fatalf("app--api: %+v", a)
	}

	inputs.asked = nil
	result, _, err = inputsService(t, catalogStore{}, doc, inputs, map[string]bool{"ai": true}).compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inputs.asked, []string{"org/project/env/app", "org/project/env/helper"}) {
		t.Fatalf("asked %v", inputs.asked)
	}
	if a := findAction(result, "resource:helper--api"); a == nil || a.Resource.Component.Inputs != "sha256:a" {
		t.Fatalf("helper--api: %+v", a)
	}
}

func TestInputsErrorFailsThePlanClearly(t *testing.T) {
	store := &planningStore{plan: &Plan{ID: "plan_1", Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Status: StatusPlanning}}
	svc := inputsService(t, store, sampleApp, &digests{err: errors.New("the settings store is down")}, nil)
	status, err := svc.Plan(t.Context(), "org", "plan_1")
	if err != nil || status != StatusFailed {
		t.Fatalf("plan: %q, %v", status, err)
	}
	if want := "couldn't read what app is configured with: the settings store is down"; store.failed != want {
		t.Fatalf("failed with %q, want %q", store.failed, want)
	}
}
