package planning

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/provider"
)

func appliedTestBinding(t *testing.T, name, commit string) *Binding {
	t.Helper()
	cfg := ServiceConfig{Provider: "vercel", Repo: "app", CommitSHA: commit, Service: name, Workload: provider.WebApp, Execution: provider.Serverless, Public: true,
		CloudPlacement: &CloudPlacement{CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, TargetID: "target", TargetRevision: 1, TargetName: "frontend", Provider: "vercel", ConnectionID: "old", AccountIdentity: "vercel:old"},
		AppliedInputs:  &AppliedInputs{Variables: "org/env/applied-inputs/dep/" + name, Source: &PlanSource{SourceID: "src", Alias: "app", Repository: "owner/app", CommitSHA: commit}, ArtifactKey: "sources/org/project/app/" + commit + ".tar.gz"}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &Binding{ID: "bind_" + name, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Name: name, Subject: ServiceSubject, Provider: "vercel", ConnectionID: "old", CommitSHA: commit, Config: raw, ExternalID: "ext_" + name, PlanID: "plan_" + name}
}
func appliedAssignment(connection string) *cloudprofiles.Assignment {
	return &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 1, Targets: []cloudprofiles.Target{{ID: "target", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "frontend", Provider: "vercel", ConnectionID: connection, Revision: 2}}}
}
func TestAppliedCloudPlanMixedSourceAndDependents(t *testing.T) {
	registry := providers.Registry()
	api := appliedTestBinding(t, "api", "older")
	web := appliedTestBinding(t, "web", "newer")
	var cfg ServiceConfig
	_ = json.Unmarshal(web.Config, &cfg)
	cfg.CloudPlacement.TargetID = "other"
	cfg.Env = []EnvLink{{Key: "API_URL", From: "api", Subject: ServiceSubject}}
	web.Config, _ = json.Marshal(cfg)
	result := AppliedCloudPlan([]*Binding{web, api}, appliedAssignment("new"), "target", []ConnectionOption{{ID: "new", Cloud: "vercel", Ready: true, Account: "new", Details: map[string]string{"team_id": "new"}}}, registry)
	if !result.Executable {
		t.Fatalf("blocked: %+v", result.Problems)
	}
	if len(result.Actions) != 2 || result.Actions[0].Name != "api" || result.Actions[0].Op != Replace || result.Actions[1].Op != Update {
		t.Fatalf("actions %+v", result.Actions)
	}
	for _, a := range result.Actions {
		want := "older"
		if a.Name == "web" {
			want = "newer"
		}
		if a.Service.CommitSHA != want || a.Service.AppliedInputs.Source.CommitSHA != want {
			t.Fatalf("lost exact applied commit: %+v", a.Service)
		}
	}
}
func TestAppliedCloudPlanEmptyAndAdoption(t *testing.T) {
	registry := providers.Registry()
	options := []ConnectionOption{{ID: "old", Cloud: "vercel", Ready: true, Account: "old", Details: map[string]string{"team_id": "old"}}}
	empty := AppliedCloudPlan(nil, appliedAssignment("old"), "target", options, registry)
	if !empty.Executable || len(empty.Actions) != 0 {
		t.Fatalf("empty target invented work: %+v", empty)
	}
	b := appliedTestBinding(t, "web", "commit")
	var cfg ServiceConfig
	_ = json.Unmarshal(b.Config, &cfg)
	cfg.AppliedInputs = nil
	b.Config, _ = json.Marshal(cfg)
	result := AppliedCloudPlan([]*Binding{b}, appliedAssignment("old"), "target", options, registry)
	if !result.Executable || result.Actions[0].Op != Keep || !result.Actions[0].AdoptCloudPlacement {
		t.Fatalf("metadata adoption needs no historical rebuild: %+v", result)
	}
	result = AppliedCloudPlan([]*Binding{b}, appliedAssignment("new"), "target", []ConnectionOption{{ID: "new", Cloud: "vercel", Ready: true, Account: "new", Details: map[string]string{"team_id": "new"}}}, registry)
	if result.Executable {
		t.Fatal("migration accepted missing historical variables")
	}
}

func TestAppliedCloudRegionMatchesNextOrdinaryPlan(t *testing.T) {
	for _, tc := range []struct{ name, defaults, settings, want string }{
		{"service override equal default", "", "region: us-central1", "us-central1"},
		{"environment override equal default", "", "environments: {production: {region: us-central1}}", "us-central1"},
		{"manifest default equal target default", "defaults: {region: us-central1}\n", "", "us-central1"},
		{"inherited target default", "", "", "us-east1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assigned, _ := cloudTargetFixture()
			assigned.Targets = assigned.Targets[:1]
			assigned.Targets[0].Provider, assigned.Targets[0].Region = "cloud-run", "us-central1"
			conns := []ConnectionOption{{ID: "a", Cloud: "gcp", Ready: true, Account: "project-a", Details: map[string]string{"project_id": "project-a"}}}
			doc := "version: 1\n" + tc.defaults + "services:\n  web: {source: cmd/api, cloud_target: frontend"
			if tc.settings != "" {
				doc += ", " + tc.settings
			}
			m := parse(t, doc+"}\n")
			ordinary := func() *Result {
				resolved, pins, err := ResolveCloudTargets(m, "production", assigned, conns)
				if err != nil {
					t.Fatal(err)
				}
				result := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: resolved, Providers: providers.Registry(), Connections: conns, CloudPlacements: pins})
				if !result.Executable {
					t.Fatalf("ordinary: %+v", result.Problems)
				}
				return result
			}
			first := action(t, ordinary(), "service:web")
			first.Service.Repo, first.Service.CommitSHA = "app", "commit"
			first.Service.AppliedInputs = &AppliedInputs{Variables: "vars", Source: &PlanSource{Alias: "app", CommitSHA: "commit"}, ArtifactKey: "artifact"}
			raw, _ := json.Marshal(first.Service)
			binding := &Binding{ID: "binding", CommitSHA: "commit", Subject: ServiceSubject, Name: "web", Provider: first.Provider, Region: first.Region, ConnectionID: first.ConnectionID, Config: raw}
			before := *assigned
			before.Targets = append([]cloudprofiles.Target(nil), assigned.Targets...)
			assigned.Targets[0].Region = "us-east1"
			assigned.Targets[0].Revision++
			migrated := AppliedCloudPlan([]*Binding{binding}, assigned, assigned.Targets[0].ID, conns, providers.Registry(), &before)
			if !migrated.Executable {
				t.Fatalf("migration: %+v", migrated.Problems)
			}
			got := action(t, migrated, "service:web").Region
			next := action(t, ordinary(), "service:web").Region
			if got != tc.want || got != next {
				t.Fatalf("migration region %q, next ordinary region %q, want %q", got, next, tc.want)
			}
			first.Service.CloudPlacement.RegionSource = ""
			binding.Config, _ = json.Marshal(first.Service)
			historical := AppliedCloudPlan([]*Binding{binding}, assigned, assigned.Targets[0].ID, conns, providers.Registry(), &before)
			if !historical.Executable || action(t, historical, "service:web").Region != "us-central1" {
				t.Fatalf("historical binding changed applied region: %+v", historical)
			}
			if tc.settings == "" && tc.defaults == "" {
				assigned.Targets[0].Region = ""
				inheritedProvider := action(t, ordinary(), "service:web")
				inheritedProvider.Service.Repo, inheritedProvider.Service.CommitSHA = "app", "commit"
				inheritedProvider.Service.AppliedInputs = first.Service.AppliedInputs
				binding.Config, _ = json.Marshal(inheritedProvider.Service)
				binding.Region = inheritedProvider.Region
				assigned.Targets[0].Region = "us-east1"
				migrated = AppliedCloudPlan([]*Binding{binding}, assigned, assigned.Targets[0].ID, conns, providers.Registry())
				if !migrated.Executable || action(t, migrated, "service:web").Region != action(t, ordinary(), "service:web").Region {
					t.Fatalf("provider-default inheritance diverged: %+v", migrated)
				}
			}
		})
	}
}

// A cloud sync onto a direct target records the Cloud server its
// connection runs, and warns as an ordinary plan does when the connection
// runs more than one.
func TestAppliedCloudPlanRecordsTheCloudServer(t *testing.T) {
	registry := providers.Registry()
	b := appliedTestBinding(t, "web", "commit")
	option := ConnectionOption{ID: "old", Cloud: "vercel", Ready: true, Account: "old", Details: map[string]string{"team_id": "old"}}
	option.Servers = []CloudServer{{ID: "server_1", CloudID: "cloud"}}
	result := AppliedCloudPlan([]*Binding{b}, appliedAssignment("old"), "target", []ConnectionOption{option}, registry)
	if a := result.Actions[0]; !result.Executable || a.Service.CloudPlacement.ServerID != "server_1" || hasCode(result.Warnings, "cloud-server-ambiguous") {
		t.Fatalf("one server: %+v %+v", a.Service.CloudPlacement, result.Warnings)
	}
	option.Servers = append(option.Servers, CloudServer{ID: "server_2", CloudID: "cloud"})
	result = AppliedCloudPlan([]*Binding{b}, appliedAssignment("old"), "target", []ConnectionOption{option}, registry)
	w := findCode(result.Warnings, "cloud-server-ambiguous")
	if a := result.Actions[0]; a.Service.CloudPlacement.ServerID != "" || w == nil || w.Name != "web" || w.Path != "services.web" || !strings.Contains(w.Message, "target frontend") {
		t.Fatalf("two servers: %+v %+v", a.Service.CloudPlacement, result.Warnings)
	}
}
