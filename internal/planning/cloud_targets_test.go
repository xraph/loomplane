package planning

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
)

func cloudTargetFixture() (*cloudprofiles.Assignment, []ConnectionOption) {
	return &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 3, Revision: 4, Targets: []cloudprofiles.Target{
			{ID: "target-a", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "frontend", Provider: "vercel", ConnectionID: "a", Revision: 5},
			{ID: "target-b", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "preview", Provider: "vercel", ConnectionID: "b", Revision: 6},
		}}, []ConnectionOption{
			{ID: "a", Cloud: "vercel", Ready: true, Details: map[string]string{"team_id": "team-a"}},
			{ID: "b", Cloud: "vercel", Ready: true, Details: map[string]string{"team_id": "team-b"}},
		}
}

func cloudTargetGraph() *graph.Graph {
	g := infrastructureGraph()
	g.Services[0].Kind = graph.WebApp
	return g
}

func TestCloudTargetsTwoManagedAccountsAndMetadataAdoption(t *testing.T) {
	assigned, conns := cloudTargetFixture()
	original := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n  other: {source: cmd/api, cloud_target: preview}\n")
	before, _ := json.Marshal(original)
	resolved, placements, err := ResolveCloudTargets(original, "production", assigned, conns)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Environment: "production", Graph: cloudTargetGraph(), Manifest: resolved, Providers: providers.Registry(), Connections: conns, CloudPlacements: placements}
	r := Compute(input)
	if !r.Executable {
		t.Fatalf("named accounts blocked: %+v", r.Problems)
	}
	for name, want := range map[string]string{"web": "a", "other": "b"} {
		a := action(t, r, "service:"+name)
		if a.ConnectionID != want || a.Service.CloudPlacement == nil || a.Service.CloudPlacement.CloudRevision != 3 || a.Service.CloudPlacement.AccountIdentity == "" {
			t.Fatalf("not pinned: %+v", a)
		}
		cfg := *a.Service
		cfg.CloudPlacement = nil
		raw, _ := json.Marshal(cfg)
		input.Bindings = append(input.Bindings, &Binding{ID: name, Subject: ServiceSubject, Name: name, Provider: a.Provider, Region: a.Region, ConnectionID: want, Config: raw})
	}
	adopted := Compute(input)
	for _, a := range adopted.Actions {
		if a.Op != Keep {
			t.Fatalf("adoption redeployed: %+v", a)
		}
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("resolution mutated stored manifest")
	}
	pinned := action(t, adopted, "service:web")
	digest, _ := Digest(pinned)
	pinned.Service.CloudPlacement.TargetRevision++
	changed, _ := Digest(pinned)
	if changed == digest {
		t.Fatal("review digest omitted target revision")
	}
	for _, b := range input.Bindings {
		if b.Name == "other" {
			b.ConnectionID = "old-account"
		}
	}
	if a := action(t, Compute(input), "service:other"); a.Op != Replace {
		t.Fatalf("account move was not replacement: %+v", a)
	}
}

func TestCloudTargetsResolutionFailuresAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, doc, message string
		mutate             func(*cloudprofiles.Assignment, []ConnectionOption) *cloudprofiles.Assignment
	}{
		{name: "missing assignment", message: "assigned", mutate: func(_ *cloudprofiles.Assignment, _ []ConnectionOption) *cloudprofiles.Assignment { return nil }},
		{name: "missing target", doc: "cloud_target: missing", message: "missing"},
		{name: "wrong scope", message: "scope", mutate: func(a *cloudprofiles.Assignment, _ []ConnectionOption) *cloudprofiles.Assignment {
			a.Targets[0].ProjectID = "other"
			return a
		}},
		{name: "unready", message: "ready", mutate: func(a *cloudprofiles.Assignment, c []ConnectionOption) *cloudprofiles.Assignment {
			c[0].Ready = false
			return a
		}},
		{name: "unverified", message: "verif", mutate: func(a *cloudprofiles.Assignment, c []ConnectionOption) *cloudprofiles.Assignment {
			c[0].Details = nil
			return a
		}},
		{name: "conflict", doc: "cloud_target: frontend, provider: vercel", message: "provider"},
		{name: "direct target conflict", doc: "cloud_target: frontend, target: conn_01m4vewksnea2bwdxwdy5aekhn", message: "target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, c := cloudTargetFixture()
			if tc.mutate != nil {
				a = tc.mutate(a, c)
			}
			doc := tc.doc
			if doc == "" {
				doc = "cloud_target: frontend"
			}
			_, _, err := ResolveCloudTargets(parse(t, "version: 1\nservices:\n  web: {source: cmd/api, "+doc+"}\n"), "production", a, c)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.message) {
				t.Fatalf("expected %s, got %v", tc.message, err)
			}
		})
	}
	a, c := cloudTargetFixture()
	m := parse(t, "version: 1\ndefaults: {provider: vercel}\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n")
	if _, _, err := ResolveCloudTargets(m, "production", a, c); err == nil {
		t.Fatal("inherited provider conflict accepted")
	}
	m = parse(t, "version: 1\nservices:\n  web:\n    source: cmd/api\n    cloud_target: frontend\n    health_path: /ready\n    environments:\n      staging: {cloud_target: preview, scale: {min: 1, max: 1}}\n")
	resolved, p, err := ResolveCloudTargets(m, "staging", a, c)
	if err != nil || p["web"].ConnectionID != "b" || resolved.Services["web"].HealthPath != "/ready" || resolved.Services["web"].Scale == nil {
		t.Fatalf("supported overrides lost: %+v %+v %v", resolved, p, err)
	}
	legacy := parse(t, "version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: conn_01m4vewksnea2bwdxwdy5aekhn}\n")
	got, p, err := ResolveCloudTargets(legacy, "production", nil, nil)
	if err != nil || len(p) != 0 || got.Services["api"].Target != legacy.Services["api"].Target {
		t.Fatalf("legacy changed: %+v %v", got, err)
	}
}

func TestCloudTargetsResolvedCopyDoesNotShareSettings(t *testing.T) {
	a, c := cloudTargetFixture()
	m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend, scale: {min: 1, max: 2}, build: {context: app}}\n")
	got, _, err := ResolveCloudTargets(m, "production", a, c)
	if err != nil {
		t.Fatal(err)
	}
	got.Services["web"].Scale.Max = 9
	got.Services["web"].Build.Context = "changed"
	if m.Services["web"].Scale.Max != 2 || m.Services["web"].Build.Context != "app" {
		t.Fatal("resolved manifest shares mutable settings with stored manifest")
	}
}

func TestCloudTargetsRejectMissingRevisions(t *testing.T) {
	a, c := cloudTargetFixture()
	a.Revision = 0
	_, _, err := ResolveCloudTargets(parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n"), "production", a, c)
	if err == nil {
		t.Fatal("unversioned assignment produced an unexecutable pin")
	}
}

func TestCloudTargetsManagedPrivateDependenciesStayInAccount(t *testing.T) {
	a, c := cloudTargetFixture()
	for _, cloud := range []string{"gcp", "aws"} {
		t.Run(cloud, func(t *testing.T) {
			providerName := "cloud-run"
			resourceProvider := "cloud-sql"
			kind := "postgres"
			if cloud == "aws" {
				providerName = "ecs-fargate"
				resourceProvider = "rds"
			}
			for i := range a.Targets {
				a.Targets[i].Provider = providerName
				c[i].Cloud = cloud
				c[i].Account = c[i].ID
				c[i].Details = map[string]string{"project_id": c[i].ID}
				if cloud == "aws" {
					c[i].Details["region"] = "us-east-1"
				}
			}
			doc := "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend, needs: [private]}\n  private: {source: cmd/api, cloud_target: preview, public: false}\n"
			m, pins, err := ResolveCloudTargets(parse(t, doc), "production", a, c)
			if err != nil {
				t.Fatal(err)
			}
			r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: m, Providers: providers.Registry(), Connections: c, CloudPlacements: pins})
			if r.Executable {
				t.Fatal("private dependency crossed account boundary")
			}
			doc = "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend, needs: [db]}\nresources:\n  db: {kind: " + kind + ", provider: " + resourceProvider + "}\n"
			m, pins, err = ResolveCloudTargets(parse(t, doc), "production", a, c)
			if err != nil {
				t.Fatal(err)
			}
			r = Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: m, Providers: providers.Registry(), Connections: c, CloudPlacements: pins, Chosen: map[string]string{cloud: "b"}})
			found := false
			for _, problem := range r.Problems {
				if problem.Code == "resource-across-accounts" {
					found = true
				}
			}
			if !found {
				t.Fatalf("resource did not report cross-account placement: %+v", r.Problems)
			}
		})
	}
}
