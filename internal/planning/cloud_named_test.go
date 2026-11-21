package planning

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/providers"
)

// namedTargetResolver adds Clouds a manifest can name to the default one.
type namedTargetResolver struct {
	*targetResolver
	named map[string]*cloudprofiles.ProjectCloud
}

func (n namedTargetResolver) ResolveNamedCloud(_ context.Context, _, _, name string) (*cloudprofiles.ProjectCloud, error) {
	if c := n.named[name]; c != nil {
		return c, nil
	}
	return nil, cloudprofiles.ErrNotFound
}

// gpuLab has a target called frontend too, so only the Cloud tells the two apart.
func gpuLab() *cloudprofiles.ProjectCloud {
	return &cloudprofiles.ProjectCloud{OrgID: "org", ProjectID: "project", CloudID: "lab", Name: "GPU Lab", CloudRevision: 1, GrantRevision: 2, Targets: []cloudprofiles.Target{
		{ID: "lab-frontend", OrgID: "org", ProjectID: "project", CloudID: "lab", Name: "frontend", Provider: "vercel", ConnectionID: "b", Revision: 7},
	}}
}

func TestNamedCloudsPlaceEachServiceThroughItsOwnCloud(t *testing.T) {
	s, p, r := targetPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n  trainer: {source: cmd/api, cloud: GPU Lab, cloud_target: frontend}\n")
	s.cloudProfiles = namedTargetResolver{r, map[string]*cloudprofiles.ProjectCloud{"GPU Lab": gpuLab()}}
	result, _, err := s.compute(t.Context(), p)
	if err != nil || !result.Executable {
		t.Fatalf("plan: %+v %v", result.Problems, err)
	}
	for name, want := range map[string]CloudPlacement{
		"web":     {CloudID: "cloud", TargetID: "target-a", ConnectionID: "a", GrantRevision: 4},
		"trainer": {CloudID: "lab", TargetID: "lab-frontend", ConnectionID: "b", GrantRevision: 2, Named: true},
	} {
		var got *CloudPlacement
		for _, a := range result.Actions {
			if a.Name == name && a.Service != nil {
				got = a.Service.CloudPlacement
			}
		}
		if got == nil || got.CloudID != want.CloudID || got.TargetID != want.TargetID || got.ConnectionID != want.ConnectionID || got.GrantRevision != want.GrantRevision || got.Named != want.Named {
			t.Errorf("%s placed %+v, want %+v", name, got, want)
		}
	}
}

func TestNamedCloudsFindings(t *testing.T) {
	for _, tc := range []struct {
		name, doc, code, path string
	}{
		{"unknown Cloud", "version: 1\nservices:\n  trainer: {source: cmd/api, cloud: Nowhere, cloud_target: frontend}\n", "cloud-unknown", "services.trainer.cloud"},
		{"no default placement", "version: 1\nservices:\n  trainer: {source: cmd/api, cloud: GPU Lab}\n", "cloud-default-missing", "services.trainer.cloud"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, r := targetPlanningService(t, tc.doc)
			s.cloudProfiles = namedTargetResolver{r, map[string]*cloudprofiles.ProjectCloud{"GPU Lab": gpuLab()}}
			result, _, err := s.compute(t.Context(), p)
			if err != nil || result.Executable || len(result.Problems) != 1 {
				t.Fatalf("plan: %+v %v", result, err)
			}
			if f := result.Problems[0]; f.Code != tc.code || f.Path != tc.path || f.Name != "trainer" {
				t.Fatalf("finding %+v, want %s at %s", f, tc.code, tc.path)
			}
		})
	}
}

func TestMissingTargetSaysWhichCloud(t *testing.T) {
	s, p, r := targetPlanningService(t, "version: 1\nservices:\n  trainer: {source: cmd/api, cloud: GPU Lab, cloud_target: preview}\n")
	s.cloudProfiles = namedTargetResolver{r, map[string]*cloudprofiles.ProjectCloud{"GPU Lab": gpuLab()}}
	result, _, err := s.compute(t.Context(), p)
	if err != nil || result.Executable || len(result.Problems) != 1 {
		t.Fatalf("plan: %+v %v", result, err)
	}
	if got := result.Problems[0].Message; got != `services.trainer.cloud_target: target "preview" isn't in Cloud GPU Lab; choose one of its targets, or name another Cloud with cloud` {
		t.Fatalf("message %q", got)
	}
}

// Changing the project's default moves services that reach their Cloud as
// the default, and keeps a service whose manifest names its Cloud.
func TestDefaultChangeKeepsServicesThatNameTheirCloud(t *testing.T) {
	registry := providers.Registry()
	follows := appliedTestBinding(t, "api", "older")
	names := appliedTestBinding(t, "web", "older")
	var cfg ServiceConfig
	_ = json.Unmarshal(names.Config, &cfg)
	cfg.CloudPlacement.Named = true
	names.Config, _ = json.Marshal(cfg)
	after := appliedAssignment("new")
	choose := func(pin *CloudPlacement) (*cloudprofiles.ProjectCloud, bool) { return after, !pin.Named }
	result := appliedCloudPlanChoosing([]*Binding{follows, names}, choose, "", []ConnectionOption{{ID: "new", Cloud: "vercel", Ready: true, Account: "new", Details: map[string]string{"team_id": "new"}}}, registry, nil, nil)
	if !result.Executable {
		t.Fatalf("blocked: %+v", result.Problems)
	}
	ops := map[string]Op{}
	for _, a := range result.Actions {
		ops[a.Name] = a.Op
	}
	if ops["api"] == Keep || ops["web"] != Keep {
		t.Fatalf("api should move and web stay: %+v", ops)
	}
}
