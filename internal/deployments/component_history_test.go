package deployments

import (
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestRecordedComponentUsesOnlyRegisteredHistory(t *testing.T) {
	oldRegistry := graphKinds(t, nil)
	f := newComponentFixture(t, oldRegistry)
	recorded := f.graphJob(t)
	old, _ := oldRegistry.Kind("graph")
	current, _ := oldRegistry.Kind("graph")
	current.Releases[0].Revision = "new"
	current.Components[0].Env = append(current.Components[0].Env, recipes.EnvVar{Name: "REVISION", Value: "new"})
	registry := recipes.NewRegistry()
	if err := registry.Register(current); err != nil {
		t.Fatal(err)
	}
	f.target.Kinds = registry
	if _, err := f.target.checkComponent(recorded.Action.Resource); err == nil {
		t.Fatal("accepted unregistered old recipe")
	}
	if err := registry.RegisterHistorical(old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.target.checkComponent(recorded.Action.Resource); err != nil {
		t.Fatal(err)
	}
	recorded.Action.Resource.Component.Component.Args = []string{"unreviewed"}
	if _, err := f.target.checkComponent(recorded.Action.Resource); err == nil {
		t.Fatal("accepted modified historical component")
	}
}

func TestUnsafeElasticsearchHistoryCannotReplayButCanBeRemoved(t *testing.T) {
	for _, revision := range []string{"1", "2"} {
		kind, composition, err := recipes.Default().ResolveRevision("elasticsearch", "9.4", revision, "amd64", "server")
		if err != nil {
			t.Fatal(err)
		}
		f := newComponentFixture(t, recipes.Default())
		job := f.graphJobForEngine(t, "elasticsearch", "9.4", composition)
		if _, err = f.target.checkComponent(job.Action.Resource); (revision == "1") != (err != nil) {
			t.Fatalf("revision %s apply: %v", revision, err)
		}
		if revision == "2" && (kind.Components[0].Readiness.Path != "/" || kind.Components[0].Readiness.Checker != "") {
			t.Fatal("changed historical revision2")
		}
		f.secrets.m[ResourceKey("org", "env", "search", "secret:password")] = []byte("kept-password")
		binding := bound(job, &Outcome{ExternalID: "instance"})
		if err = f.target.forgetPublished(t.Context(), binding, job.Action.Resource.Component); err != nil {
			t.Fatal(err)
		}
		if _, exists := f.secrets.m[ResourceKey("org", "env", "search", "secret:password")]; exists {
			t.Fatal("historical cleanup kept password")
		}
	}
}
