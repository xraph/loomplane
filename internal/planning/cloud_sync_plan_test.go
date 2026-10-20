package planning

import (
	"encoding/json"
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
