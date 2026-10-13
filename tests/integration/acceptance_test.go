//go:build integration && acceptance

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

// The acceptance run deploys a real repository to real clouds, checks it
// answers, and takes it all down again. It's what the fakes can't prove:
// that the clouds' real APIs agree with them. It costs money while it
// runs, so it only runs when asked: make acceptance, with the variables
// docs/acceptance.md lists.

func acceptanceEnv(name string) string {
	return strings.TrimSpace(os.Getenv("LOOMPLANE_ACCEPTANCE_" + name))
}

func TestAcceptance_aRealDeploymentAnswersAndIsTakenDown(t *testing.T) {
	repo, manifestFile := acceptanceEnv("REPO"), acceptanceEnv("MANIFEST")
	if repo == "" || manifestFile == "" {
		t.Skip("set LOOMPLANE_ACCEPTANCE_REPO and LOOMPLANE_ACCEPTANCE_MANIFEST; docs/acceptance.md says how")
	}
	manifest, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatalf("read the manifest: %v", err)
	}
	timeout := 60 * time.Minute
	if v := acceptanceEnv("TIMEOUT"); v != "" {
		if timeout, err = time.ParseDuration(v); err != nil {
			t.Fatalf("LOOMPLANE_ACCEPTANCE_TIMEOUT: %v", err)
		}
	}

	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 2 * time.Second
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	// Cleanups run last first: the teardown below runs before this stops
	// Loomplane.
	t.Cleanup(r.stop)

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Acceptance", "repository_url": "https://github.com/" + repo}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)

	connect := func(cloud, credential string, settings map[string]string) {
		var conn struct {
			Status  string   `json:"status"`
			Account string   `json:"account"`
			Missing []string `json:"missing"`
			Message string   `json:"message"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
			map[string]any{"cloud": cloud, "name": cloud, "credential": credential, "settings": settings}, &conn)
		if conn.Status != "ready" {
			t.Fatalf("the %s connection is %s: missing %v %s", cloud, conn.Status, conn.Missing, conn.Message)
		}
		t.Logf("connected %s as %s", cloud, conn.Account)
	}
	if f := acceptanceEnv("GCP_KEY_FILE"); f != "" {
		key, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read the Google Cloud key: %v", rerr)
		}
		connect("gcp", string(key), nil)
	}
	if tok := acceptanceEnv("VERCEL_TOKEN"); tok != "" {
		connect("vercel", tok, map[string]string{"team_id": acceptanceEnv("VERCEL_TEAM")})
	}
	if id := acceptanceEnv("AWS_ACCESS_KEY_ID"); id != "" {
		cred, _ := json.Marshal(map[string]string{"access_key_id": id, "secret_access_key": acceptanceEnv("AWS_SECRET_ACCESS_KEY")})
		connect("aws", string(cred), map[string]string{
			"region": acceptanceEnv("AWS_REGION"), "certificate_arn": acceptanceEnv("AWS_CERTIFICATE_ARN"),
		})
	}
	if k := acceptanceEnv("RENDER_API_KEY"); k != "" {
		connect("render", k, map[string]string{"owner_id": acceptanceEnv("RENDER_OWNER_ID")})
	}

	// deploy plans what manifest says and carries it out.
	deploy := func(what string, yaml string) deploymentBody {
		c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": yaml}, nil)
		var plan planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
		waitPlanned(t, c, &plan)
		if !plan.Executable {
			t.Fatalf("the %s plan can't be carried out: %+v", what, plan.Problems)
		}
		for _, a := range plan.Actions {
			t.Logf("%s: %s %s on %s", what, a.Op, a.ID, a.Provider)
		}
		c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
		var d deploymentBody
		deadline := time.Now().Add(timeout)
		for d.Status == "" || d.Status == "running" {
			if time.Now().After(deadline) {
				t.Fatalf("the %s deployment is still %q after %s", what, d.Status, timeout)
			}
			time.Sleep(10 * time.Second)
			if c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &d) == http.StatusNotFound {
				d.Status = ""
			}
		}
		for _, s := range d.Steps {
			t.Logf("%s: %s %s %s %s", what, s.ActionID, s.Status, s.URL, s.Message)
		}
		return d
	}

	// Whatever happens, take it down, unless you asked to keep it.
	t.Cleanup(func() {
		if acceptanceEnv("KEEP") == "1" {
			t.Log("LOOMPLANE_ACCEPTANCE_KEEP is set: what was deployed is still running, and still costing money")
			return
		}
		d := deploy("teardown", "version: 1\n")
		if d.Status != "succeeded" {
			t.Errorf("the teardown %s: %s. Check both clouds' consoles for what's left", d.Status, d.Error)
		}
		var bindings struct {
			Bindings []struct {
				Name string `json:"name"`
			} `json:"bindings"`
		}
		c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
		if len(bindings.Bindings) != 0 {
			t.Errorf("still deployed after the teardown: %+v", bindings.Bindings)
		}
	})

	d := deploy("deployment", string(manifest))
	if d.Status != "succeeded" {
		t.Fatalf("the deployment %s: %s", d.Status, d.Error)
	}
	// Every public address answers with something other than a server
	// error, within a few minutes of going live.
	for _, s := range d.Steps {
		if s.URL == "" || !strings.HasPrefix(s.URL, "http") {
			continue
		}
		deadline := time.Now().Add(5 * time.Minute)
		for {
			res, gerr := http.Get(s.URL) //nolint:gosec,noctx // a test reaching what it deployed
			if gerr == nil {
				_ = res.Body.Close()
				if res.StatusCode < 500 {
					t.Logf("%s answers %s with %d", s.ActionID, s.URL, res.StatusCode)
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s at %s doesn't answer: %v", s.ActionID, s.URL, gerr)
			}
			time.Sleep(10 * time.Second)
		}
	}
}
