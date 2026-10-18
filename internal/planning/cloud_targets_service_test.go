package planning

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/providers"
)

type targetAnalyses struct{ Analyses }

func (targetAnalyses) Graph(context.Context, string, string) (*graph.Graph, error) {
	return cloudTargetGraph(), nil
}

type targetOptions struct{ connections []*connections.Connection }

func (c targetOptions) All(context.Context, string) ([]*connections.Connection, error) {
	return c.connections, nil
}

type targetResolver struct {
	assignment *cloudprofiles.Assignment
	calls      int
}

func (c *targetResolver) Resolve(context.Context, string, string, string) (*cloudprofiles.Assignment, error) {
	c.calls++
	return c.assignment, nil
}

func targetPlanningService(t *testing.T, doc string) (*Service, *Plan, *targetResolver) {
	t.Helper()
	a, c := cloudTargetFixture()
	options := targetOptions{}
	for _, option := range c {
		options.connections = append(options.connections, &connections.Connection{ID: option.ID, Cloud: option.Cloud, Status: connections.StatusReady, Details: option.Details})
	}
	resolver := &targetResolver{assignment: a}
	service := NewService(Deps{Store: catalogStore{}, Analyses: targetAnalyses{}, Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}, Providers: providers.Registry(), Connections: options, CloudProfiles: resolver})
	plan := &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Sources: []PlanSource{{Alias: "app", AnalysisID: "analysis", CommitSHA: "abc", InScope: true}}}
	return service, plan, resolver
}

func TestCloudTargetServiceScopeAndRevisionDigest(t *testing.T) {
	for _, scope := range []string{"org", "project", "environment"} {
		t.Run(scope, func(t *testing.T) {
			s, p, r := targetPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n")
			switch scope {
			case "org":
				r.assignment.OrgID = "another"
			case "project":
				r.assignment.ProjectID = "another"
			case "environment":
				r.assignment.EnvironmentID = "another"
			}
			result, _, err := s.compute(t.Context(), p)
			if err != nil || result.Executable || len(result.Problems) == 0 {
				t.Fatalf("wrong scope not blocked: %+v %v", result, err)
			}
		})
	}
	s, p, r := targetPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n")
	result, revision, err := s.compute(t.Context(), p)
	if err != nil || !result.Executable {
		t.Fatalf("target service failed: %+v %v", result, err)
	}
	r.assignment.CloudRevision++
	_, changed, err := s.compute(t.Context(), p)
	if err != nil || changed == revision {
		t.Fatalf("cloud revision omitted: %v", err)
	}
	r.assignment = nil
	result, _, err = s.compute(t.Context(), p)
	if err != nil || result.Executable || len(result.Actions) != 0 {
		t.Fatalf("missing assignment fell back: %+v %v", result, err)
	}
}

func TestCloudTargetServiceHeldRepositoriesIgnoreDesiredPlacement(t *testing.T) {
	s, p, r := targetPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: missing}\n")
	r.assignment = nil
	p.Sources[0].InScope = false
	cfg := ServiceConfig{CloudPlacement: &CloudPlacement{CloudID: "historical", ConnectionID: "a"}, Provider: "vercel", Repo: "app", Service: "svc:go:cmd/api", Source: "cmd/api"}
	raw, _ := json.Marshal(cfg)
	s.store = catalogStore{bindings: []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "web", Provider: "vercel", ConnectionID: "a", Config: raw}}}
	result, _, err := s.compute(t.Context(), p)
	if err != nil || !result.Executable || len(result.Actions) != 1 || result.Actions[0].Op != Keep || r.calls != 0 {
		t.Fatalf("held desired placement blocked: %+v %v, resolver calls=%d", result, err, r.calls)
	}
}

func TestCloudTargetServiceLegacyDoesNotRequireCloudResolver(t *testing.T) {
	s, p, r := targetPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, provider: vercel}\n")
	r.assignment.OrgID = "another"
	_, _, err := s.compute(t.Context(), p)
	if err != nil || r.calls != 0 {
		t.Fatalf("legacy requested Cloud assignment: %v, calls=%d", err, r.calls)
	}
}
