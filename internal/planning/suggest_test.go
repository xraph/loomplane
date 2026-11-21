package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/manifest"
)

// Every suggestion is an add_service edit that the manifest takes as it
// is: a free name, a source the planner finds, and needs it already has.
func TestSuggest_addsWhatTheManifestDoesntRun(t *testing.T) {
	g := polyglot(t)
	reg := providers.Registry()
	m := Draft(g, reg)
	api := m.Services["api"]
	delete(m.Services, "worker")
	delete(m.Services, "web")
	// Something else took the name worker.
	m.Resources["worker"] = manifest.Resource{Kind: "redis", Provider: "memorystore"}

	got := Suggest(g, m, reg)
	names := map[string]ServiceSuggestion{}
	for _, s := range got {
		names[s.Service.Name] = s
	}
	if len(got) != 2 || names["web"].Service.Source == "" || names["worker-2"].Service.Source == "" {
		t.Fatalf("suggested %+v", got)
	}
	if names["web"].Service.Provider != "vercel" {
		t.Errorf("web on %q, want vercel", names["web"].Service.Provider)
	}
	if names["worker-2"].Service.Provider != "ecs-fargate" {
		t.Errorf("worker on %q, want ecs-fargate", names["worker-2"].Service.Provider)
	}

	// Each suggestion goes into the manifest as it is.
	doc, err := manifest.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		in := s.Service
		out, err := manifest.WithService(doc, in.Name, manifest.NewService{
			Repo: in.Repo, Source: in.Source, Provider: in.Provider, Needs: in.Needs,
		})
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		if _, err := manifest.Parse(out); err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
	}

	// Nothing the manifest runs comes back, however its source is written.
	if slices.ContainsFunc(got, func(s ServiceSuggestion) bool { return s.Service.Source == api.Source }) {
		t.Errorf("suggested api, which the manifest runs")
	}
}

// A service with a default provider that runs it inherits the default.
func TestSuggest_inheritsTheDefaultProvider(t *testing.T) {
	g := polyglot(t)
	m := &manifest.Manifest{Version: manifest.Version, Defaults: manifest.Defaults{Provider: "cloud-run"}}
	for _, s := range Suggest(g, m, providers.Registry()) {
		if s.Service.Name == "api" && s.Service.Provider != "" {
			t.Errorf("api on %q, want the default", s.Service.Provider)
		}
	}
}
