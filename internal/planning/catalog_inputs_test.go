package planning

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/pkg/manifest"
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

func (d *digests) Digest(_ context.Context, orgID, projectID, environmentID, resource string, r manifest.Resource) (map[string]string, error) {
	d.asked = append(d.asked, orgID+"/"+projectID+"/"+environmentID+"/"+resource+" "+r.Kind)
	return d.out, d.err
}

// planningStore holds one plan and records why it failed or how it was
// settled.
type planningStore struct {
	catalogStore
	plan    *Plan
	failed  string
	settled string
	note    string
}

func (s *planningStore) Decide(_ context.Context, _, _, status, _, note string, _ time.Time) (bool, error) {
	s.settled, s.note = status, note
	return true, nil
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
	if !slices.Equal(inputs.asked, []string{"org/project/env/app sample"}) {
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
	if !slices.Equal(inputs.asked, []string{"org/project/env/app sample", "org/project/env/helper featured"}) {
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

func TestCancelledInputReadIsTriedAgain(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, fmt.Errorf("read settings: %w", context.DeadlineExceeded)} {
		if failsPlanning(&catalogInputsError{resource: "app", err: cause}) {
			t.Fatalf("%v fails the plan", cause)
		}
		store := &planningStore{plan: &Plan{ID: "plan_1", Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Status: StatusPlanning}}
		svc := inputsService(t, store, sampleApp, &digests{err: cause}, nil)
		status, err := svc.Plan(t.Context(), "org", "plan_1")
		if !errors.Is(err, cause) || status != "" || store.failed != "" {
			t.Fatalf("%v: plan %q, %v, failed with %q", cause, status, err, store.failed)
		}

		store.plan = &Plan{ID: "plan_1", Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Status: StatusReady, Revision: "r1"}
		status, err = svc.ApplyDecision(t.Context(), "org", "plan_1", orchestration.Decision{Approve: true, Revision: "r1", By: "usr_1"})
		if !errors.Is(err, cause) || status != "" || store.settled != "" {
			t.Fatalf("%v: approve %q, %v, settled %q", cause, status, err, store.settled)
		}
	}
}

func TestApprovalWithUnreadableInputsSaysWhy(t *testing.T) {
	store := &planningStore{plan: &Plan{ID: "plan_1", Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Status: StatusReady, Revision: "r1"}}
	svc := inputsService(t, store, sampleApp, &digests{err: errors.New("the settings store is down")}, nil)
	status, err := svc.ApplyDecision(t.Context(), "org", "plan_1", orchestration.Decision{Approve: true, Revision: "r1", By: "usr_1"})
	if err != nil || status != StatusStale || store.settled != StatusStale {
		t.Fatalf("approve: %q, %v, settled %q", status, err, store.settled)
	}
	if want := "Couldn't check what app is configured with. Plan again."; store.note != want {
		t.Fatalf("settled with %q, want %q", store.note, want)
	}
}

// revisions is a project's manifest history: Get finds a revision by ID,
// and Latest is the newest one.
type revisions struct {
	Manifests
	byID   map[string]*manifests.Revision
	latest string
}

func (r *revisions) Get(_ context.Context, _, id string) (*manifests.Revision, error) {
	if rev, ok := r.byID[id]; ok {
		return rev, nil
	}
	return nil, manifests.ErrNotFound
}

func (r *revisions) Latest(context.Context, string, string) (*manifests.Revision, error) {
	return r.byID[r.latest], nil
}

// settingDigest digests one setting of the resource it's given, as a kind
// that renders it into a component would.
type settingDigest struct{ setting string }

func (d settingDigest) Digest(_ context.Context, _, _, _, _ string, r manifest.Resource) (map[string]string, error) {
	return map[string]string{"engine": "sha256:" + r.Settings[d.setting]}, nil
}

// A teammate saving the manifest again after a plan was made doesn't make
// the plan stale: the approval digests the plan's own revision.
func TestApprovalDigestsThePlansOwnManifest(t *testing.T) {
	doc := func(maxLen string) *manifests.Revision {
		return &manifests.Revision{Manifest: parse(t, "version: 1\nresources:\n  llm: {kind: "+recipes.ModelTEIKind+", provider: server, target: "+targetA+
			", settings: {entry: BAAI/bge-small-en-v1.5, model: BAAI/bge-small-en-v1.5, revision: 5c38ec7c405ec4b44b94cc5a9bb96e735b38267a, quantization: native, served_name: bge-small, max_model_len: '"+maxLen+"'}, environments: {production: {enabled: true}}}\n")}
	}
	history := &revisions{byID: map[string]*manifests.Revision{"man_7": doc("512"), "man_8": doc("1024")}, latest: "man_7"}
	ai := map[string]bool{recipes.FeatureAI: true}
	plan := &Plan{ID: "plan_1", Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", ManifestID: "man_7", Status: StatusReady}
	store := &planningStore{plan: plan}
	svc := NewService(Deps{
		Store: store, Manifests: history, Providers: providers.RegistryFor(ai), Connections: serverClouds{}, Features: ai,
		CatalogInputs: CatalogInputMap{recipes.ModelTEIKind: settingDigest{setting: "max_model_len"}},
	})
	result, revision, err := svc.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if a := findAction(result, "resource:llm--engine"); a == nil || a.Resource.Component.Inputs != "sha256:512" {
		t.Fatalf("engine: %+v", a)
	}
	plan.Revision = revision

	// Revision 8 changes the setting the digest reads.
	history.latest = "man_8"
	status, err := svc.ApplyDecision(t.Context(), "org", "plan_1", orchestration.Decision{Approve: true, Revision: revision, By: "usr_1"})
	if err != nil || status != StatusApproved || store.settled != StatusApproved {
		t.Fatalf("approve: %q, %v, settled %q (%s)", status, err, store.settled, store.note)
	}
}

// fixable is an InputsProblem: it says what to change.
type fixable struct{}

func (fixable) Error() string {
	return "app asks for v1, which is gone. Set its version to v2, then plan again"
}
func (fixable) InputsProblem() {}

func TestInputsProblemsSayWhatToChange(t *testing.T) {
	cause := fmt.Errorf("wrapped: %w", fixable{})
	store := &planningStore{plan: &Plan{ID: "plan_1", Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Status: StatusPlanning}}
	svc := inputsService(t, store, sampleApp, &digests{err: cause}, nil)
	status, err := svc.Plan(t.Context(), "org", "plan_1")
	if err != nil || status != StatusFailed {
		t.Fatalf("plan: %q, %v", status, err)
	}
	if want := (fixable{}).Error(); store.failed != want {
		t.Errorf("failed with %q, want %q", store.failed, want)
	}

	store.plan = &Plan{ID: "plan_1", Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Status: StatusReady, Revision: "r1"}
	status, err = svc.ApplyDecision(t.Context(), "org", "plan_1", orchestration.Decision{Approve: true, Revision: "r1", By: "usr_1"})
	if err != nil || status != StatusStale {
		t.Fatalf("approve: %q, %v", status, err)
	}
	if want := (fixable{}).Error() + "."; store.note != want {
		t.Errorf("settled with %q, want %q", store.note, want)
	}
}
