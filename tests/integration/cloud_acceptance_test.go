//go:build integration && acceptance

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

func TestAcceptanceCloudAccountSwitch(t *testing.T) {
	repo, source := acceptanceEnv("CLOUD_REPO"), acceptanceEnv("CLOUD_SOURCE")
	tokens := []vercel.Token{{Value: acceptanceEnv("CLOUD_VERCEL_A_TOKEN"), TeamID: acceptanceEnv("CLOUD_VERCEL_A_TEAM")}, {Value: acceptanceEnv("CLOUD_VERCEL_B_TOKEN"), TeamID: acceptanceEnv("CLOUD_VERCEL_B_TEAM")}}
	if repo == "" || source == "" || tokens[0].Value == "" || tokens[1].Value == "" {
		t.Skip("live Cloud switch requires CLOUD_REPO, CLOUD_SOURCE and two CLOUD_VERCEL_[AB]_TOKEN scopes; docs/acceptance.md")
	}
	if strings.ContainsAny(source, "\n\r\"\\") {
		t.Fatal("CLOUD_SOURCE must be one source identifier")
	}
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = time.Second
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	t.Cleanup(r.stop)
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var project struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Cloud acceptance", "repository_url": "https://github.com/" + repo}, &project)
	base := "/api/v1/projects/" + project.ID
	accounts := make([]connections.Connection, 2)
	for i, tok := range tokens {
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "vercel", "name": fmt.Sprintf("Acceptance scope %d", i), "credential": tok.Value, "settings": map[string]string{"team_id": tok.TeamID}}, &accounts[i])
		if accounts[i].Status != "ready" {
			t.Fatalf("scope %d not ready", i)
		}
	}
	if connections.AccountIdentity("vercel", accounts[0].Account, accounts[0].Details) == connections.AccountIdentity("vercel", accounts[1].Account, accounts[1].Details) {
		t.Fatal("two distinct verified account/team scopes are required")
	}
	provider := vercel.New("")
	name := deployments.ProjectName(&deployments.Job{Action: planning.Action{Name: "web"}, Deployment: &deployments.Deployment{ProjectID: project.ID, Environment: "production"}})
	// Prove the generated name was absent in both accounts before granting cleanup.
	for _, tok := range tokens {
		if _, err := provider.GetProject(t.Context(), tok, name); !vercel.IsNotFound(err) {
			t.Fatal("disposable project name is not confirmed absent; no deployment accepted")
		}
	}
	t.Logf("disposable project %s, Vercel name %s in verified distinct scopes", project.ID, name)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		for _, tok := range tokens {
			p, err := provider.GetProject(ctx, tok, name)
			if vercel.IsNotFound(err) {
				continue
			}
			if err != nil {
				t.Errorf("find disposable project: %v", err)
				continue
			}
			if p.Name != name {
				t.Error("cleanup refused unexpected project name")
				continue
			}
			if err = provider.DeleteProject(ctx, tok, p.ID); err != nil {
				t.Errorf("remove disposable project: %v", err)
				continue
			}
			if _, err = provider.GetProject(ctx, tok, p.ID); !vercel.IsNotFound(err) {
				t.Errorf("disposable project remains: %v", err)
			}
		}
	})
	analyze(t, cfg, c, project.ID)
	var cloud cloudprofiles.Profile
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Acceptance"}, &cloud)
	var target cloudprofiles.Target
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "frontend", Provider: "vercel", ConnectionID: accounts[0].ID}, &target)
	var envs struct{ Environments []struct{ ID, Name string } }
	c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	env := ""
	for _, e := range envs.Environments {
		if e.Name == "production" {
			env = e.ID
		}
	}
	if env == "" {
		t.Fatal("production missing")
	}
	wait := func(id string) *cloudprofiles.Sync {
		t.Helper()
		var sync cloudprofiles.Sync
		for deadline := time.Now().Add(45 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
			c.must(http.StatusOK, http.MethodGet, base+"/cloud-syncs/"+id, nil, &sync)
			switch sync.Status {
			case cloudprofiles.SyncSucceeded, cloudprofiles.SyncRouting:
				return &sync
			case cloudprofiles.SyncFailed:
				t.Fatalf("live migration failed: %s", sync.Error)
			case cloudprofiles.SyncCleanup:
				if sync.Error != "" {
					t.Fatalf("live cleanup failed: %s", sync.Error)
				}
			}
		}
		t.Fatalf("live migration timed out: %s", sync.Status)
		return nil
	}
	var preview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, base+"/environments/"+env+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID}, &preview)
	if got := wait(applyCloudAPI(t, c, base, &preview, "assign-live").ID); got.Status != cloudprofiles.SyncSucceeded {
		t.Fatalf("assignment %s", got.Status)
	}
	deploy := func(yaml string) planning.Binding {
		t.Helper()
		c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": yaml}, nil)
		var plan planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
		waitPlanned(t, c, &plan)
		if !plan.Executable || len(plan.Actions) != 1 || plan.Actions[0].ID != "service:web" || plan.Actions[0].Provider != "vercel" {
			t.Fatalf("refuse unexpected live plan: %+v", plan)
		}
		c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
		var d deploymentBody
		for deadline := time.Now().Add(45 * time.Minute); ; time.Sleep(time.Second) {
			if c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &d) == http.StatusOK && d.Status != "running" && d.Status != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("live release timed out")
			}
		}
		if d.Status != "succeeded" {
			t.Fatalf("live release failed: %s", d.Error)
		}
		var result struct{ Bindings []planning.Binding }
		c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &result)
		if len(result.Bindings) != 1 {
			t.Fatalf("unexpected bindings: %+v", result.Bindings)
		}
		return result.Bindings[0]
	}
	manifest := "version: 1\nservices:\n  web:\n    source: \"" + source + "\"\n    cloud_target: frontend\n"
	original := deploy(manifest)
	check := func(b planning.Binding, index int) {
		t.Helper()
		if b.ConnectionID != accounts[index].ID || b.CommitSHA == "" || b.URL == "" {
			t.Fatalf("wrong live placement: %+v", b)
		}
		p, err := provider.GetProject(t.Context(), tokens[index], b.ExternalID)
		if err != nil || p.Name != name {
			t.Fatalf("live project identity: %v", err)
		}
		d, err := provider.GetDeployment(t.Context(), tokens[index], b.Revision)
		if err != nil || d.ReadyState != "READY" || d.Address() != b.URL {
			t.Fatalf("live deployment readiness/URL: %v", err)
		}
		endpoint, err := url.Parse(vercel.DefaultEndpoint + "/v13/deployments/" + url.PathEscape(b.Revision))
		if err != nil {
			t.Fatal(err)
		}
		query := endpoint.Query()
		if tokens[index].TeamID != "" {
			query.Set("teamId", tokens[index].TeamID)
		}
		endpoint.RawQuery = query.Encode()
		metadataRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		metadataRequest.Header.Set("Authorization", "Bearer "+tokens[index].Value)
		metadataResponse, err := (&http.Client{Timeout: 30 * time.Second}).Do(metadataRequest)
		if err != nil {
			t.Fatal(err)
		}
		var metadata struct {
			Meta      map[string]string `json:"meta"`
			ProjectID string            `json:"projectId"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(metadataResponse.Body, 4<<20)).Decode(&metadata)
		_ = metadataResponse.Body.Close()
		if metadataResponse.StatusCode != http.StatusOK || decodeErr != nil || metadata.Meta["githubCommitSha"] != b.CommitSHA || metadata.ProjectID != b.ExternalID {
			t.Fatal("live provider source metadata or project identity differs from the applied binding")
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, b.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 400 {
			t.Fatalf("live URL answers %d", res.StatusCode)
		}
		t.Logf("scope %d: project %s, commit %s, deployment %s, URL %s", index, b.ExternalID, b.CommitSHA, b.Revision, b.URL)
	}
	check(original, 0)
	c.must(http.StatusOK, http.MethodPost, base+"/cloud-changes/preview", cloudprofiles.ChangeInput{CloudID: cloud.ID, TargetID: target.ID, Revision: 1, Provider: "vercel", ConnectionID: accounts[1].ID}, &preview)
	if len(preview.Blockers) != 0 || len(preview.Environments) != 1 || preview.Environments[0].Summary.Replace != 1 {
		t.Fatalf("live switch review: %+v", preview)
	}
	migration := wait(applyCloudAPI(t, c, base, &preview, "live-account-switch").ID)
	var moved struct{ Bindings []planning.Binding }
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &moved)
	if len(moved.Bindings) != 1 || moved.Bindings[0].CommitSHA != original.CommitSHA {
		t.Fatal("migration did not retain original applied source")
	}
	check(moved.Bindings[0], 1)
	if migration.Status == cloudprofiles.SyncRouting {
		c.must(http.StatusAccepted, http.MethodPost, base+"/cloud-syncs/"+migration.ID+"/routing", apiclient.CompleteCloudRoutingInput{EnvironmentID: env, Evidence: "Live destination account identity, READY deployment and HTTP URL verified"}, nil)
		if result := wait(migration.ID); result.Status != cloudprofiles.SyncSucceeded {
			t.Fatalf("cleanup %s", result.Status)
		}
	}
	if _, err := provider.GetProject(t.Context(), tokens[0], original.ExternalID); !vercel.IsNotFound(err) {
		t.Fatalf("old account project remains: %v", err)
	}
	next := deploy(strings.Replace(manifest, "cloud_target: frontend", "cloud_target: frontend\n    health_path: /", 1))
	check(next, 1)
	if next.CommitSHA != original.CommitSHA || next.Revision == moved.Bindings[0].Revision {
		t.Fatal("later release did not build a new deployment in destination account")
	}
}
