//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

const webOnVercel = "version: 1\nservices:\n  web:\n    source: node:apps/web\n    provider: vercel\nignore:\n  resources: [postgres, redis]\n"

// A frontend release that Vercel reports as failed has nothing left in
// flight, so the next plan can release the service. One whose outcome was
// lost still holds the service until that same release is recovered.
func TestFrontendFailureSettlesOnlyWhenTheProviderSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		settled bool
	}{{"build failed", true}, {"answer lost", false}} {
		t.Run(tc.name, func(t *testing.T) {
			const vercelToken = "vcl_0123456789abcdef"
			vc := newFakeVercelDeploys(t, vercelToken)
			vc.buildFails, vc.dropPolls = tc.settled, !tc.settled
			cfg := config.Defaults()
			cfg.Database.URL = freshDatabase(t)
			cfg.Orchestration.PollInterval = 100 * time.Millisecond
			cfg.GitHub.APIURL = fakeGitHubServing(t).URL
			cfg.Analysis.WorkDir = t.TempDir()
			cfg.Clouds.VercelEndpoint = vc.srv.URL
			cfg.Storage.URL = "file://" + t.TempDir()
			r := start(t, cfg)
			defer r.stop()

			c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
			var auth struct {
				SessionToken string `json:"session_token"`
			}
			c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
			c.token = auth.SessionToken
			var org struct {
				ID string `json:"id"`
			}
			c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
			c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
			var proj struct {
				ID string `json:"id"`
			}
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
			base := "/api/v1/projects/" + proj.ID
			analyze(t, cfg, c, proj.ID)
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
				map[string]string{"cloud": "vercel", "name": "Web", "credential": vercelToken}, nil)
			c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": webOnVercel}, nil)
			release := func() deploymentBody {
				t.Helper()
				var plan planBody
				c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
				waitPlanned(t, c, &plan)
				if !plan.Executable {
					t.Fatalf("plan %+v", plan)
				}
				return approveAndWait(t, c, plan)
			}
			if d := release(); d.Status != "failed" {
				t.Fatalf("first release: %+v", d)
			}
			vc.mu.Lock()
			vc.buildFails, vc.dropPolls = false, false
			vc.mu.Unlock()
			d := release()
			if tc.settled && d.Status != "succeeded" {
				t.Fatalf("release after a confirmed failure: %+v", d)
			}
			if !tc.settled && (d.Status != "failed" || !strings.Contains(d.Error, "recover the prior service attempt")) {
				t.Fatalf("release after a lost answer: %+v", d)
			}
		})
	}
}
