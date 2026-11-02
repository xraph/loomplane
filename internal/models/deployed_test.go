package models

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

type mapSecrets map[string]string

func (m mapSecrets) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m[key]
	if !ok {
		return nil, deployments.ErrNoCredential
	}
	return []byte(v), nil
}

type keySecrets struct{ asked []string }

func (k *keySecrets) InternalSecret(_ context.Context, orgID string, ref deployments.ResourceRef, name string) ([]byte, error) {
	k.asked = append(k.asked, fmt.Sprintf("%s %s/%s/%s %s", orgID, ref.ProjectID, ref.EnvironmentID, ref.Resource, name))
	return []byte("lpm_secret"), nil
}

// DeployedFacts reads a model's state from the project's newest runs,
// page by page, and the environment's open plans, and its address and key
// from what deployments kept.
func TestDeployedFactsReadTheDeploymentStores(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	// 60 runs, newest first: only the 55th ran the model in env_1.
	runs := make([]*deployments.Deployment, 0, 60)
	for i := range 60 {
		d := &deployments.Deployment{ID: fmt.Sprintf("dep_%02d", i), EnvironmentID: "env_2", Status: deployments.StatusSucceeded,
			StartedAt: start.Add(-time.Duration(i) * time.Minute)}
		if i == 55 {
			d.EnvironmentID = "env_1"
			d.Steps = []deployments.Step{{ActionID: "resource:llm", Status: deployments.StepSucceeded}}
		}
		runs = append(runs, d)
	}
	var pages []page.Cursor
	keys := &keySecrets{}
	f := &DeployedFacts{
		ListBindings: func(context.Context, string, string) ([]*planning.Binding, error) {
			return []*planning.Binding{{Subject: planning.ResourceSubject, Name: "llm", ConnectionID: "conn_gpu"}}, nil
		},
		ListPlans: func(_ context.Context, _, projectID, _ string, _ page.Cursor, limit int) ([]*planning.Plan, error) {
			if projectID != "prj" || limit != openPlansRead {
				return nil, errors.New("wrong plans asked for")
			}
			return []*planning.Plan{
				{ID: "plan_old", Status: planning.StatusSuperseded, Actions: []planning.Action{{Subject: planning.ResourceSubject, Name: "llm", Op: planning.Update}}},
				{ID: "plan_new", Status: planning.StatusReady, Actions: []planning.Action{{Subject: planning.ResourceSubject, Name: "llm", Op: planning.Update}}},
			}, nil
		},
		ListRuns: func(_ context.Context, _, _ string, before page.Cursor, limit int) ([]*deployments.Deployment, error) {
			pages = append(pages, before)
			from := 0
			for i, d := range runs {
				if d.ID == before.ID {
					from = i + 1
				}
			}
			return runs[from:min(from+limit, len(runs))], nil
		},
		Secrets:   mapSecrets{deployments.ResourceURLKey("org", "env_1", "llm"): "http://llm--guard:8080"},
		Generated: keys,
	}
	st, err := f.State(ctx, "org", "prj", "env_1", "llm")
	if err != nil {
		t.Fatal(err)
	}
	want := catalogstate.State{Value: catalogstate.Ready, PlanID: "plan_new", DeploymentID: "dep_55", ConnectionID: "conn_gpu", Bound: true}
	if st != want || len(pages) != 2 || pages[1].ID != "dep_49" {
		t.Errorf("state %+v, pages %+v", st, pages)
	}
	if u, err := f.URL(ctx, "org", "env_1", "llm"); err != nil || u != "http://llm--guard:8080" {
		t.Errorf("url %q, %v", u, err)
	}
	if u, err := f.URL(ctx, "org", "env_1", "other"); err != nil || u != "" {
		t.Errorf("no url %q, %v", u, err)
	}
	if k, err := f.APIKey(ctx, "org", "prj", "env_1", "llm"); err != nil || k != "lpm_secret" || keys.asked[0] != "org prj/env_1/llm "+recipes.ModelAPIKey {
		t.Errorf("key %q, %v, %v", k, err, keys.asked)
	}
}
