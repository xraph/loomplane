//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/examples/provider-plugin/statichost"
	"github.com/xraph/loomplane/internal/config"
)

// refused calls and fails unless the answer is 403 or 404: refused, and
// without saying whether the thing exists.
func refused(t *testing.T, c *client, method, path string, body any) {
	t.Helper()
	if code := c.do(method, path, body, nil); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("%s %s answered %d; want it refused", method, path, code)
	}
}

func TestSecurity_organizationsAndRolesHoldEverywhere(t *testing.T) {
	host := &statichost.Host{Dir: t.TempDir(), Token: "host-token-0123456789"}
	plugin := httptest.NewServer(host.Handler("plugin-token-0123456789"))
	defer plugin.Close()
	host.BaseURL = plugin.URL

	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Providers.Plugins = []config.PluginConfig{{URL: plugin.URL, Token: "plugin-token-0123456789"}}
	r := start(t, cfg)
	defer r.stop()

	signUp := func(email string) *client {
		c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
		account(t, c, email)
		return c
	}
	newOrg := func(c *client, slug string) string {
		var org struct {
			ID string `json:"id"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": slug, "slug": slug}, &org)
		c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
		return org.ID
	}
	join := func(owner *client, orgID, email, role string) *client {
		var created struct {
			AcceptPath string `json:"accept_path"`
		}
		owner.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": email, "role": role}, &created)
		u, err := url.Parse(created.AcceptPath)
		if err != nil {
			t.Fatal(err)
		}
		c := signUp(email)
		if code := c.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": u.Query().Get("token")}, nil); code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("accept: %d", code)
		}
		c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": orgID}, nil)
		return c
	}

	// Ada's organization has one of everything.
	ada := signUp("ada@example.com")
	orgA := newOrg(ada, "engines")
	var proj struct {
		ID string `json:"id"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, ada, proj.ID)
	var analyses struct {
		Analyses []struct {
			ID string `json:"id"`
		} `json:"analyses"`
	}
	ada.must(http.StatusOK, http.MethodGet, base+"/analyses", nil, &analyses)
	var conn struct {
		ID string `json:"id"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "static-host", "name": "Sites", "credential": host.Token}, &conn)
	var variable struct {
		ID string `json:"id"`
	}
	ada.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "TOKEN", "value": "s3cret", "secret": true}, &variable)
	var key keyBody
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "ci", "access": "write"}, &key)
	ada.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": docsOnStaticHost}, nil)
	var plan planBody
	ada.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, ada, &plan)
	d := approveAndWait(t, ada, plan)
	var pending planBody
	ada.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &pending)
	waitPlanned(t, ada, &pending)

	// Grace runs another organization: none of Ada's things answer her.
	grace := signUp("grace@example.com")
	newOrg(grace, "compilers")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, base}, {http.MethodPatch, base}, {http.MethodDelete, base},
		{http.MethodGet, base + "/manifest"}, {http.MethodPut, base + "/manifest"},
		{http.MethodGet, base + "/plans"}, {http.MethodPost, base + "/plans"},
		{http.MethodGet, base + "/deployments"}, {http.MethodGet, base + "/bindings?environment=production"},
		{http.MethodGet, base + "/variables"}, {http.MethodPost, base + "/variables"},
		{http.MethodGet, base + "/environments"}, {http.MethodPost, base + "/analyses"},
		{http.MethodGet, base + "/analyses"}, {http.MethodGet, base + "/graph"}, {http.MethodGet, base + "/manifest/draft"},
		{http.MethodGet, base + "/sources"}, {http.MethodPost, base + "/sources"},
		{http.MethodPatch, base + "/sources/mono"}, {http.MethodDelete, base + "/sources/mono"},
		{http.MethodGet, base + "/environments/production/deployed"},
		{http.MethodGet, "/api/v1/analyses/" + analyses.Analyses[0].ID},
		{http.MethodGet, "/api/v1/analyses/" + analyses.Analyses[0].ID + "/graph"},
		{http.MethodGet, "/api/v1/plans/" + pending.ID},
		{http.MethodPost, "/api/v1/plans/" + pending.ID + "/approve"}, {http.MethodPost, "/api/v1/plans/" + pending.ID + "/reject"},
		{http.MethodGet, "/api/v1/plans/" + plan.ID + "/deployment"},
		{http.MethodGet, "/api/v1/deployments/" + d.ID}, {http.MethodPost, "/api/v1/deployments/" + d.ID + "/retry"},
		{http.MethodGet, "/api/v1/connections/" + conn.ID}, {http.MethodDelete, "/api/v1/connections/" + conn.ID},
		{http.MethodPost, "/api/v1/connections/" + conn.ID + "/check"}, {http.MethodPut, "/api/v1/connections/" + conn.ID + "/credential"},
		{http.MethodPatch, "/api/v1/variables/" + variable.ID}, {http.MethodDelete, "/api/v1/variables/" + variable.ID},
		{http.MethodDelete, "/api/v1/api-keys/" + key.Key.ID},
	} {
		var body any
		switch {
		case c.path == base+"/plans" && c.method == http.MethodPost:
			body = map[string]string{"environment": "production"}
		case c.method == http.MethodPost && len(c.path) > 8 && c.path[len(c.path)-8:] == "/approve":
			body = map[string]string{"revision": pending.Revision}
		case c.method != http.MethodGet && c.method != http.MethodDelete:
			body = map[string]any{}
		}
		refused(t, grace, c.method, c.path, body)
	}
	// Activity counts an organization's own: Ada's deployment is in hers
	// and not in Grace's.
	var act struct {
		Deployments struct {
			Succeeded int `json:"succeeded"`
		} `json:"deployments"`
		Drift struct {
			Deployed int `json:"deployed"`
		} `json:"drift"`
	}
	grace.must(http.StatusOK, http.MethodGet, "/api/v1/activity", nil, &act)
	if act.Deployments.Succeeded != 0 || act.Drift.Deployed != 0 {
		t.Fatalf("Grace sees Ada's activity: %+v", act)
	}
	ada.must(http.StatusOK, http.MethodGet, "/api/v1/activity", nil, &act)
	if act.Deployments.Succeeded != 1 {
		t.Fatalf("Ada's activity %+v", act)
	}
	// Ada's key doesn't reach Grace's organization either: it's Ada's.
	as := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey, token: key.Secret}
	as.must(http.StatusOK, http.MethodGet, base, nil, nil)

	// A developer plans and doesn't approve, retry, or touch connections.
	dev := join(ada, orgA, "dev@example.com", "developer")
	dev.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+pending.ID, nil, nil)
	refused(t, dev, http.MethodPost, "/api/v1/plans/"+pending.ID+"/approve", map[string]string{"revision": pending.Revision})
	refused(t, dev, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil)
	refused(t, dev, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "static-host", "name": "Mine", "credential": host.Token})
	refused(t, dev, http.MethodDelete, "/api/v1/connections/"+conn.ID, nil)
	refused(t, dev, http.MethodDelete, base, nil)
	refused(t, dev, http.MethodDelete, "/api/v1/api-keys/"+key.Key.ID, nil)
	// Their key can't do more than they can.
	var devKey keyBody
	dev.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "laptop", "access": "write"}, &devKey)
	devAs := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey, token: devKey.Secret}
	refused(t, devAs, http.MethodPost, "/api/v1/plans/"+pending.ID+"/approve", map[string]string{"revision": pending.Revision})

	// A viewer reads and changes nothing. Grace joins as one; authsome
	// lets only three people sign up from one address in a window.
	var invite struct {
		AcceptPath string `json:"accept_path"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "grace@example.com", "role": "viewer"}, &invite)
	iu, err := url.Parse(invite.AcceptPath)
	if err != nil {
		t.Fatal(err)
	}
	if code := grace.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": iu.Query().Get("token")}, nil); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("accept: %d", code)
	}
	grace.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": orgA}, nil)
	viewer := grace
	viewer.must(http.StatusOK, http.MethodGet, base, nil, nil)
	viewer.must(http.StatusOK, http.MethodGet, base+"/variables", nil, nil)
	refused(t, viewer, http.MethodPost, base+"/plans", map[string]string{"environment": "production"})
	refused(t, viewer, http.MethodPut, base+"/manifest", map[string]string{"yaml": docsOnStaticHost})
	refused(t, viewer, http.MethodPost, base+"/variables", map[string]any{"key": "X", "value": "1"})
	refused(t, viewer, http.MethodPost, base+"/analyses", nil)
	refused(t, viewer, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Other"})
	refused(t, viewer, http.MethodPost, "/api/v1/plans/"+pending.ID+"/reject", map[string]string{})

	// And a secret's value never comes back, to anyone.
	if raw := ada.raw(base + "/variables"); strings.Contains(raw, "s3cret") || !strings.Contains(raw, "TOKEN") {
		t.Fatalf("variables %s", raw)
	}
}
