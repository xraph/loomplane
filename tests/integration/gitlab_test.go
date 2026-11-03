//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

func TestGitLab_aPublicProjectIsAnalyzed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitLab.APIURL = fakeGitLabProjects(t, map[string][]byte{
		"grp/sub/api": tarball(t, "api-"+exampleSHA+"-"+exampleSHA, siteFiles),
	}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Lab"}, &proj)
	base := "/api/v1/projects/" + proj.ID + "/sources"

	var api sourceBody
	c.must(http.StatusCreated, http.MethodPost, base, map[string]string{"repository": "https://gitlab.com/grp/sub/api", "alias": "api"}, &api)
	if api.Repository != "gitlab.com/grp/sub/api" {
		t.Fatalf("attached source %+v, want gitlab.com/grp/sub/api", api)
	}
	// A project the fake GitLab doesn't serve can't be read, so it isn't added.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base, map[string]string{"repository": "https://gitlab.com/grp/sub/nowhere"}, nil)

	analyses := analyzeAll(t, cfg, c, proj.ID)
	if len(analyses) != 1 || analyses[0].Source != "api" || analyses[0].Status != "succeeded" ||
		analyses[0].CommitSHA != exampleSHA || analyses[0].Summary.Services == 0 {
		t.Fatalf("analyses %+v", analyses)
	}

	var list struct {
		Sources []sourceBody `json:"sources"`
	}
	c.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Sources) != 1 || list.Sources[0].Repository != "gitlab.com/grp/sub/api" {
		t.Fatalf("sources %+v", list.Sources)
	}
}

// gitLabInstance configures a Loomplane that signs in through the fake
// GitLab as one of the operator's hosts, and returns its host name.
func gitLabInstance(t *testing.T, fake *gitLabFake) (config.Config, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.GitLab.Hosts = []config.GitLabHostConfig{{WebURL: fake.URL, ClientID: fakeGitLabClientID, ClientSecret: fakeGitLabSecret}}
	u, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, u.Host
}

type gitLabStatusBody struct {
	Hosts []struct {
		ID       string `json:"id"`
		Host     string `json:"host"`
		Instance bool   `json:"instance"`
		ClientID string `json:"client_id"`
	} `json:"hosts"`
	Connections []struct {
		ID               string `json:"id"`
		Host             string `json:"host"`
		Username         string `json:"username"`
		Version          string `json:"version"`
		VersionSupported bool   `json:"version_supported"`
		Lapsed           bool   `json:"lapsed"`
	} `json:"connections"`
	CanConnect        bool   `json:"can_connect"`
	OrganizationHosts bool   `json:"organization_hosts"`
	CallbackURL       string `json:"callback_url"`
}

// gitLabSignIn starts a sign-in through host and returns the state GitLab's
// redirect would carry back.
func gitLabSignIn(t *testing.T, c *client, hostID string) (state string, authorize *url.URL) {
	t.Helper()
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/gitlab/sign-ins", map[string]string{"host_id": hostID}, &start)
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state"), u
}

func TestGitLab_signInListAndReadAPrivateProject(t *testing.T) {
	fake := fakeGitLab(t, tarball(t, "api-"+exampleSHA+"-"+exampleSHA, siteFiles))
	cfg, host := gitLabInstance(t, fake)
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var status gitLabStatusBody
	c.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab", nil, &status)
	hostID := "instance:" + host
	if len(status.Hosts) != 1 || status.Hosts[0].ID != hostID || status.Hosts[0].Host != host || !status.Hosts[0].Instance ||
		status.Hosts[0].ClientID != fakeGitLabClientID || len(status.Connections) != 0 || !status.CanConnect || !status.OrganizationHosts ||
		status.CallbackURL != strings.TrimRight(cfg.PublicURL, "/")+"/integrations/gitlab/callback" {
		t.Fatalf("status %+v", status)
	}

	// A host that isn't offered can't be signed in through.
	c.must(http.StatusNotFound, http.MethodPost, "/api/v1/gitlab/sign-ins", map[string]string{"host_id": "instance:gitlab.example.org"}, nil)

	state, authorize := gitLabSignIn(t, c, hostID)
	q := authorize.Query()
	if authorize.Scheme+"://"+authorize.Host != fake.URL || authorize.Path != "/oauth/authorize" || q.Get("scope") != "read_api" ||
		q.Get("client_id") != fakeGitLabClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("redirect_uri") != status.CallbackURL || state == "" {
		t.Fatalf("authorize URL %s", authorize)
	}

	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Lab"}, &proj)
	sources := "/api/v1/projects/" + proj.ID + "/sources"
	private := map[string]string{"repository": fake.URL + "/grp/sub/api", "alias": "api"}
	// Before anyone signs in, the private project can't be read.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, sources, private, nil)

	// A code GitLab doesn't know isn't a connection, and the sign-in is
	// spent by trying it.
	c.must(http.StatusBadRequest, http.MethodPost, "/api/v1/gitlab/connections", map[string]string{"code": "guessed", "state": state}, nil)
	state, _ = gitLabSignIn(t, c, hostID)
	var conn struct {
		ID       string `json:"id"`
		Host     string `json:"host"`
		Username string `json:"username"`
		Lapsed   bool   `json:"lapsed"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/gitlab/connections", map[string]string{"code": fakeGitLabCode, "state": state}, &conn)
	if conn.ID == "" || conn.Host != host || conn.Username != "ada" || conn.Lapsed {
		t.Fatalf("connection %+v", conn)
	}
	// A sign-in finishes once.
	c.must(http.StatusBadRequest, http.MethodPost, "/api/v1/gitlab/connections", map[string]string{"code": fakeGitLabCode, "state": state}, nil)

	c.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab", nil, &status)
	if len(status.Connections) != 1 || status.Connections[0].ID != conn.ID || status.Connections[0].Version != "17.4.2-ee" || !status.Connections[0].VersionSupported {
		t.Fatalf("status after connecting %+v", status)
	}
	// Nothing GitLab handed out comes back.
	for _, secret := range []string{fakeGitLabAccessToken, "glrt-it-refresh", fakeGitLabSecret} {
		if raw := c.raw("/api/v1/gitlab"); strings.Contains(raw, secret) {
			t.Fatalf("status leaks %q: %s", secret, raw)
		}
	}

	var repos struct {
		Repositories []struct {
			Key        string `json:"key"`
			Host       string `json:"host"`
			Connection string `json:"connection"`
			Private    bool   `json:"private"`
		} `json:"repositories"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab/repositories", nil, &repos)
	if len(repos.Repositories) != 1 || repos.Repositories[0].Key != host+"/grp/sub/api" || !repos.Repositories[0].Private ||
		repos.Repositories[0].Connection != conn.ID || repos.Repositories[0].Host != host {
		t.Fatalf("repositories %+v", repos)
	}

	var src sourceBody
	c.must(http.StatusCreated, http.MethodPost, sources, private, &src)
	if src.Repository != host+"/grp/sub/api" {
		t.Fatalf("attached source %+v", src)
	}
	analyses := analyzeAll(t, cfg, c, proj.ID)
	if len(analyses) != 1 || analyses[0].Source != "api" || analyses[0].Status != "succeeded" ||
		analyses[0].CommitSHA != exampleSHA || analyses[0].Summary.Services == 0 {
		t.Fatalf("analyses %+v", analyses)
	}

	// Disconnected, the sign-in is revoked at GitLab, and the connection is gone.
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/gitlab/connections/"+conn.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodDelete, "/api/v1/gitlab/connections/"+conn.ID, nil, nil)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab", nil, &status)
	if len(status.Connections) != 0 {
		t.Fatalf("connections after disconnecting %+v", status.Connections)
	}
	calls, archiveTokens, revoked := fake.seen()
	if len(archiveTokens) == 0 {
		t.Fatalf("GitLab never saw the private archive requested; calls %v", calls)
	}
	for _, tok := range archiveTokens {
		if tok != fakeGitLabAccessToken {
			t.Fatalf("the archive was read with %q, want the connection's token", tok)
		}
	}
	if len(revoked) != 1 || revoked[0] != fakeGitLabAccessToken {
		t.Fatalf("revoked %v, want the connection's token once", revoked)
	}
	if last := calls[len(calls)-1]; last != "revoke" {
		t.Fatalf("calls %v, want the revoke after every read", calls)
	}
}

func TestGitLab_otherOrganizationCantFinishSignIn(t *testing.T) {
	fake := fakeGitLab(t, nil)
	cfg, host := gitLabInstance(t, fake)
	r := start(t, cfg)
	defer r.stop()
	ada := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, ada)
	state, _ := gitLabSignIn(t, ada, "instance:"+host)

	// Grace has an organization of her own, and Ada's state.
	grace := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	account(t, grace, "grace@example.com")
	var org struct {
		ID string `json:"id"`
	}
	grace.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other", "slug": "other"}, &org)
	grace.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	grace.must(http.StatusBadRequest, http.MethodPost, "/api/v1/gitlab/connections", map[string]string{"code": fakeGitLabCode, "state": state}, nil)

	var status gitLabStatusBody
	grace.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab", nil, &status)
	if len(status.Connections) != 0 {
		t.Fatalf("Grace's organization got a connection: %+v", status.Connections)
	}
	// The attempt spent the state, so it's no use to Ada either.
	ada.must(http.StatusBadRequest, http.MethodPost, "/api/v1/gitlab/connections", map[string]string{"code": fakeGitLabCode, "state": state}, nil)
	ada.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab", nil, &status)
	if len(status.Connections) != 0 {
		t.Fatalf("a connection was made from a state that was replayed: %+v", status.Connections)
	}
	if _, _, revoked := fake.seen(); len(revoked) != 0 {
		t.Fatalf("revoked %v, but nothing was connected", revoked)
	}
}

func TestGitLab_otherOrganizationCantReadAConnectedProject(t *testing.T) {
	fake := fakeGitLab(t, tarball(t, "api-"+exampleSHA+"-"+exampleSHA, siteFiles))
	cfg, host := gitLabInstance(t, fake)
	r := start(t, cfg)
	defer r.stop()
	ada := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, ada)
	state, _ := gitLabSignIn(t, ada, "instance:"+host)
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/gitlab/connections", map[string]string{"code": fakeGitLabCode, "state": state}, nil)

	grace := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	account(t, grace, "grace@example.com")
	var org struct {
		ID string `json:"id"`
	}
	grace.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other", "slug": "other"}, &org)
	grace.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	grace.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Theirs"}, &proj)

	before := len(fake.requestsSeen())
	grace.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/projects/"+proj.ID+"/sources",
		map[string]string{"repository": fake.URL + "/grp/sub/api", "alias": "api"}, nil)

	// GitLab was asked, with no token: Ada's connection isn't Grace's to use.
	after := fake.requestsSeen()[before:]
	if len(after) == 0 {
		t.Fatal("GitLab was never asked whether Grace's organization can read the project")
	}
	for _, req := range after {
		if !strings.HasSuffix(req, " anonymous") {
			t.Fatalf("GitLab saw %q for Grace's organization; Ada's token was used on her behalf (%v)", req, after)
		}
	}
	if _, archiveTokens, _ := fake.seen(); len(archiveTokens) != 0 {
		t.Fatalf("the private archive was read with %v", archiveTokens)
	}
}

func TestGitLab_viewerCantAddHost(t *testing.T) {
	fake := fakeGitLab(t, nil)
	cfg, host := gitLabInstance(t, fake)
	r := start(t, cfg)
	defer r.stop()
	ada := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, ada)
	var read keyBody
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "dashboards", "access": "read"}, &read)
	viewer := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey, token: read.Secret}

	newHost := map[string]string{"web_url": "https://git.example.com", "client_id": "app", "client_secret": "shh"}
	viewer.must(http.StatusForbidden, http.MethodPost, "/api/v1/gitlab/hosts", newHost, nil)
	viewer.must(http.StatusForbidden, http.MethodPost, "/api/v1/gitlab/sign-ins", map[string]string{"host_id": "instance:" + host}, nil)
	viewer.must(http.StatusForbidden, http.MethodPost, "/api/v1/gitlab/connections", map[string]string{"code": fakeGitLabCode, "state": "x"}, nil)
	viewer.must(http.StatusForbidden, http.MethodDelete, "/api/v1/gitlab/connections/glc_none", nil, nil)
	viewer.must(http.StatusForbidden, http.MethodDelete, "/api/v1/gitlab/hosts/glh_none", nil, nil)

	// A viewer reads what's there, and is told it can't connect.
	var status gitLabStatusBody
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab", nil, &status)
	if status.CanConnect || len(status.Hosts) != 1 {
		t.Fatalf("viewer's status %+v", status)
	}
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab/repositories", nil, nil)
	// Signed out, none of it is open.
	anon := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	anon.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/gitlab", nil, nil)
	anon.must(http.StatusUnauthorized, http.MethodPost, "/api/v1/gitlab/hosts", newHost, nil)

	// The member who can connect adds a host of her own, and removes it.
	var added struct {
		ID       string `json:"id"`
		Instance bool   `json:"instance"`
		Host     string `json:"host"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/gitlab/hosts", newHost, &added)
	if added.ID == "" || added.Instance || added.Host != "git.example.com" {
		t.Fatalf("added host %+v", added)
	}
	ada.must(http.StatusConflict, http.MethodPost, "/api/v1/gitlab/hosts", newHost, nil)
	ada.must(http.StatusBadRequest, http.MethodPost, "/api/v1/gitlab/hosts", map[string]string{"web_url": "http://git.example.com", "client_id": "a", "client_secret": "b"}, nil)
	ada.must(http.StatusBadRequest, http.MethodPost, "/api/v1/gitlab/hosts", map[string]string{"web_url": "https://other.example.com/gitlab", "client_id": "a", "client_secret": "b"}, nil)
	ada.must(http.StatusBadRequest, http.MethodPost, "/api/v1/gitlab/hosts", map[string]string{"web_url": "https://other.example.com", "client_id": "a"}, nil)
	ada.must(http.StatusNotFound, http.MethodDelete, "/api/v1/gitlab/hosts/instance:"+host, nil, nil)
	ada.must(http.StatusNoContent, http.MethodDelete, "/api/v1/gitlab/hosts/"+added.ID, nil, nil)
	ada.must(http.StatusNotFound, http.MethodDelete, "/api/v1/gitlab/hosts/"+added.ID, nil, nil)
}
