package deployments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestRenderSpec_gitLabRepoURL(t *testing.T) {
	cfg := &planning.ServiceConfig{Source: "api", Ecosystem: "go", Port: 8080, Workload: provider.HTTPService, Execution: provider.Container}
	job := &Job{Deployment: &Deployment{ProjectID: "project_1", Environment: "production"}, Action: planning.Action{Name: "api", Service: cfg}}
	for key, want := range map[string]string{
		"gitlab.com/grp/sub/api": "https://gitlab.com/grp/sub/api",
		"acme/api":               "https://github.com/acme/api",
	} {
		spec, err := renderSpec(job, cfg, renderRepoURL(key))
		if err != nil {
			t.Fatal(err)
		}
		if spec.Repo != want {
			t.Errorf("key %q: repo %q, want %q", key, spec.Repo, want)
		}
	}
}

func TestDOAppSpec_gitLabSource(t *testing.T) {
	cfg := &planning.ServiceConfig{Source: "api", Ecosystem: "go", Port: 8080, Public: true, Workload: provider.HTTPService, Execution: provider.Container}
	job := doJob(cfg, "")
	comp := doAppSpec(job, cfg, "gitlab.com/grp/sub/api", "main").Services[0]
	if comp.GitHub != nil {
		t.Fatalf("a GitLab key got a GitHub source: %+v", comp.GitHub)
	}
	want := digitalocean.GitLabSource{Repo: "grp/sub/api", Branch: "main", DeployOnPush: false}
	if comp.GitLab == nil || *comp.GitLab != want {
		t.Fatalf("gitlab source %+v, want %+v", comp.GitLab, want)
	}
	raw, err := json.Marshal(comp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"gitlab":{"repo":"grp/sub/api","branch":"main","deploy_on_push":false}`) || strings.Contains(string(raw), `"github"`) {
		t.Fatalf("spec json %s", raw)
	}
}

func TestDOAppSpec_gitHubSourceUnchanged(t *testing.T) {
	cfg := &planning.ServiceConfig{Source: "api", Ecosystem: "go", Port: 8080, Public: true, Workload: provider.HTTPService, Execution: provider.Container}
	comp := doAppSpec(doJob(cfg, ""), cfg, "acme/mono", "main").Services[0]
	if comp.GitLab != nil || comp.GitHub == nil || *comp.GitHub != (digitalocean.GitHubSource{Repo: "acme/mono", Branch: "main"}) {
		t.Fatalf("github %+v, gitlab %+v", comp.GitHub, comp.GitLab)
	}
	raw, err := json.Marshal(comp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"github":{"repo":"acme/mono","branch":"main","deploy_on_push":false}`) || strings.Contains(string(raw), `"gitlab"`) {
		t.Fatalf("spec json %s", raw)
	}
}

func TestDOSupportedHost(t *testing.T) {
	for key, ok := range map[string]bool{
		"acme/mono": true, "gitlab.com/a/b": true, "GitLab.com/a/b/c": true,
		"gitlab.example.com/a/b": false, "bitbucket.org/a/b": false,
	} {
		if err := doSupportedHost(key); (err == nil) != ok {
			t.Errorf("%s: err %v, want ok %v", key, err, ok)
		}
	}
}
