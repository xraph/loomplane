//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ctl"
	"github.com/xraph/loomplane/internal/platform"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

// loomctl runs the CLI against srv with key, the way CI would, and returns
// what it printed.
type loomctl struct {
	t      *testing.T
	url    string
	key    string
	config string
}

func (l loomctl) run(stdin string, args ...string) (string, error) {
	var out bytes.Buffer
	env := map[string]string{ctl.EnvURL: l.url, ctl.EnvKey: l.key}
	err := ctl.New(ctl.Options{
		Version: "test", Out: &out, In: strings.NewReader(stdin), ConfigPath: l.config,
		Getenv: func(k string) string { return env[k] },
	}).Run(args)
	return out.String(), err
}

func (l loomctl) must(args ...string) string {
	l.t.Helper()
	out, err := l.run("", args...)
	if err != nil {
		l.t.Fatalf("loomctl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestCLI_planAndApproveADeploymentFromTheCommandLine(t *testing.T) {
	lpclient.Every = 100 * time.Millisecond
	fake := newFakeAWS(t)
	cloud := installFakeECS(t, fake)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.AWSEndpoint = fake.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "ada@example.com", "Correct-horse-battery-9")
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": "aws", "name": "AWS", "credential": awsKey(awsKeyID), "settings": map[string]string{"region": "us-east-1"}}, nil)
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "ci", "access": "write"}, &key)

	dir := t.TempDir()
	lc := loomctl{t: t, url: r.srv.URL, key: key.Secret, config: filepath.Join(dir, "loomctl.json")}

	// login checks the key and saves it, readable by you only.
	if out, err := (loomctl{t: t, config: lc.config}).run(key.Secret+"\n", "login", "--url", r.srv.URL, "--key", "-"); err != nil || !strings.Contains(out, org.ID) {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if info, err := os.Stat(lc.config); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the saved login: %v, %v", info, err)
	}
	if out, err := (loomctl{t: t, config: lc.config}).run("", "whoami"); err != nil || !strings.Contains(out, org.ID) {
		t.Fatalf("whoami from the saved login: %v\n%s", err, out)
	}
	if _, err := (loomctl{t: t, config: lc.config}).run("", "login", "--url", r.srv.URL, "--key", "lp_live_"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("login accepted a key that doesn't work")
	}

	var who lpclient.Session
	if err := json.Unmarshal([]byte(lc.must("whoami", "--json")), &who); err != nil || who.OrgID != org.ID {
		t.Fatalf("whoami %+v (%v)", who, err)
	}
	lc.must("projects", "create", "Mono", "--repo", "https://github.com/acme/mono")
	if out := lc.must("projects"); !strings.Contains(out, "mono") || !strings.Contains(out, "acme/mono") {
		t.Fatalf("projects:\n%s", out)
	}

	worker, err := platform.NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	out := lc.must("analyze", "mono")
	_ = worker.Stop(context.Background())
	if !strings.Contains(out, "mono: found 7 services") {
		t.Fatalf("analyze:\n%s", out)
	}
	if draft := lc.must("manifest", "mono", "--draft"); !strings.Contains(draft, "version: 1") {
		t.Fatalf("draft:\n%s", draft)
	}
	manifest := filepath.Join(dir, "loomplane.yaml")
	if err := os.WriteFile(manifest, []byte(apiAndWorkerOnECS), 0o600); err != nil {
		t.Fatal(err)
	}
	lc.must("manifest", "save", "mono", "-f", manifest)
	if got := lc.must("manifest", "mono"); got != apiAndWorkerOnECS {
		t.Fatalf("manifest:\n%s", got)
	}

	// A secret from standard input, never echoed back.
	const secret = "from-stdin-secret"
	if set, serr := lc.run(secret+"\n", "vars", "set", "mono", "API_TOKEN=-", "--secret", "--env", "production"); serr != nil {
		t.Fatalf("vars set: %v\n%s", serr, set)
	}
	if vars := lc.must("vars", "mono"); !strings.Contains(vars, "API_TOKEN") || strings.Contains(vars, secret) {
		t.Fatalf("vars:\n%s", vars)
	}

	var plan lpclient.Plan
	if err := json.Unmarshal([]byte(lc.must("plan", "mono", "--env", "production", "--json")), &plan); err != nil || plan.Status != "ready" || !plan.Executable {
		t.Fatalf("plan %+v (%v)", plan, err)
	}
	if shown := lc.must("show", plan.ID); !strings.Contains(shown, "loomctl approve "+plan.ID) || !strings.Contains(shown, "plain-http") {
		t.Fatalf("show:\n%s", shown)
	}
	out = lc.must("approve", plan.ID, "--yes", "--note", "from CI")
	if !strings.Contains(out, "Approved.") || !strings.Contains(out, ": succeeded") {
		t.Fatalf("approve:\n%s", out)
	}
	if out := lc.must("status", "mono"); !strings.Contains(out, "api") || !strings.Contains(out, "ecs-fargate") || !strings.Contains(out, "in sync") {
		t.Fatalf("status:\n%s", out)
	}
	if out := lc.must("deployments", "mono"); !strings.Contains(out, "succeeded") {
		t.Fatalf("deployments:\n%s", out)
	}
	cloud.mu.Lock()
	stored := false
	for _, v := range cloud.secrets {
		stored = stored || v == secret
	}
	cloud.mu.Unlock()
	if !stored {
		t.Fatal("the secret set from the CLI didn't reach Secrets Manager")
	}

	// An approved plan can't be approved again.
	if _, err := lc.run("", "approve", plan.ID, "--yes"); err == nil {
		t.Fatal("approved the same plan twice")
	}
	lc.must("logout")
	if _, err := os.Stat(lc.config); !os.IsNotExist(err) {
		t.Fatalf("logout left %s: %v", lc.config, err)
	}
}
