//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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

// gitLabPush is the part of GitLab's Push Hook the webhook reads.
func gitLabPush(path, ref string) []byte {
	b, _ := json.Marshal(map[string]any{
		"object_kind": "push", "ref": ref, "after": "da1560886d4f094c3e6c9ef40349f7d38b5d27d7",
		"project": map[string]any{"path_with_namespace": path, "default_branch": "main"},
	})
	return b
}

func TestGitLab_pushReanalyzes(t *testing.T) {
	fake := fakeGitLab(t, tarball(t, "api-"+exampleSHA+"-"+exampleSHA, siteFiles))
	cfg, host := gitLabInstance(t, fake)
	// No worker runs, so the analyses a push starts stay queued.
	r := start(t, cfg)
	defer r.stop()
	ada := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, ada)

	state, _ := gitLabSignIn(t, ada, "instance:"+host)
	var conn struct {
		ID         string `json:"id"`
		WebhookSet bool   `json:"webhook_set"`
		WebhookURL string `json:"webhook_url"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/gitlab/connections", map[string]string{"code": fakeGitLabCode, "state": state}, &conn)
	if conn.WebhookSet || conn.WebhookURL != strings.TrimRight(cfg.PublicURL, "/")+"/api/v1/gitlab/webhook/"+conn.ID {
		t.Fatalf("a new connection %+v", conn)
	}

	var proj struct {
		ID string `json:"id"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Lab"}, &proj)
	var src sourceBody
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+proj.ID+"/sources",
		map[string]string{"repository": fake.URL + "/grp/sub/api", "alias": "api"}, &src)
	if src.Repository != host+"/grp/sub/api" {
		t.Fatalf("attached source %+v", src)
	}

	// Grace's organization has a project on the same GitLab project, which
	// Ada's connection has nothing to do with.
	grace := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	account(t, grace, "grace@example.com")
	var org struct {
		ID string `json:"id"`
	}
	grace.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other", "slug": "other"}, &org)
	grace.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var graceProj struct {
		ID string `json:"id"`
	}
	grace.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Theirs"}, &graceProj)
	db, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if _, err = db.Exec(context.Background(), `INSERT INTO loomplane_project_sources
		(id, org_id, project_id, alias, repository, root, ref, created_by, created_at, updated_at)
		VALUES ('src_grace', $1, $2, 'api', $3, '', '', 'test', now(), now())`,
		org.ID, graceProj.ID, strings.ToUpper(host)+"/Grp/Sub/API"); err != nil {
		t.Fatal(err)
	}

	hookURL := r.srv.URL + "/api/v1/gitlab/webhook/" + conn.ID
	analyses := func(c *client, projectID string) []analysisBody {
		t.Helper()
		var list struct {
			Analyses []analysisBody `json:"analyses"`
		}
		c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+projectID+"/analyses", nil, &list)
		return list.Analyses
	}
	// deliver posts a Push Hook the way GitLab does, signed or with its
	// secret token, and returns the status, the body and how many started.
	deliver := func(url string, body []byte, headers map[string]string) (int, string, int) {
		t.Helper()
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gitlab-Event", "Push Hook")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatal(doErr)
		}
		defer res.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(res.Body)
		var out struct {
			Started int `json:"analyses_started"`
		}
		_ = json.Unmarshal(buf.Bytes(), &out)
		return res.StatusCode, buf.String(), out.Started
	}
	signWith := func(secret, body []byte) map[string]string {
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		m := hmac.New(sha256.New, secret)
		m.Write([]byte("msg_1." + stamp + "."))
		m.Write(body)
		return map[string]string{
			"Webhook-Id": "msg_1", "Webhook-Timestamp": stamp,
			"Webhook-Signature": "v1," + base64.StdEncoding.EncodeToString(m.Sum(nil)),
		}
	}
	push := gitLabPush("grp/sub/api", "refs/heads/main")

	// Before a secret exists, nothing can be verified: the webhook isn't there.
	if code, _, _ := deliver(hookURL, push, map[string]string{"X-Gitlab-Token": "guessed"}); code != http.StatusNotFound {
		t.Fatalf("a delivery before any secret got %d, want 404", code)
	}

	// Only a member who can connect makes one, only for an own connection.
	var read keyBody
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "dashboards", "access": "read"}, &read)
	viewer := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey, token: read.Secret}
	viewer.must(http.StatusForbidden, http.MethodPost, "/api/v1/gitlab/connections/"+conn.ID+"/webhook", nil, nil)
	grace.must(http.StatusNotFound, http.MethodPost, "/api/v1/gitlab/connections/"+conn.ID+"/webhook", nil, nil)
	ada.must(http.StatusNotFound, http.MethodPost, "/api/v1/gitlab/connections/glc_none/webhook", nil, nil)
	// A pasted signing token has to be whsec_ and 32 bytes of base64.
	ada.must(http.StatusBadRequest, http.MethodPost, "/api/v1/gitlab/connections/"+conn.ID+"/webhook", map[string]string{"signing_token": "whsec_c2hvcnQ="}, nil)

	var secret struct {
		URL          string `json:"url"`
		SigningToken string `json:"signing_token"`
		SecretToken  string `json:"secret_token"`
	}
	ada.must(http.StatusOK, http.MethodPost, "/api/v1/gitlab/connections/"+conn.ID+"/webhook", nil, &secret)
	raw, ok := strings.CutPrefix(secret.SigningToken, "whsec_")
	if !ok || secret.URL != conn.WebhookURL || secret.SecretToken == "" {
		t.Fatalf("secret %+v", secret)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 || base64.RawURLEncoding.EncodeToString(key) != secret.SecretToken {
		t.Fatalf("signing token %q decodes to %d bytes, %v", secret.SigningToken, len(key), err)
	}
	// It's shown once: the status says one is set and nothing more.
	var status struct {
		Connections []struct {
			ID         string `json:"id"`
			WebhookSet bool   `json:"webhook_set"`
		} `json:"connections"`
	}
	ada.must(http.StatusOK, http.MethodGet, "/api/v1/gitlab", nil, &status)
	if len(status.Connections) != 1 || !status.Connections[0].WebhookSet {
		t.Fatalf("status %+v", status)
	}
	if body := ada.raw("/api/v1/gitlab"); strings.Contains(body, secret.SecretToken) || strings.Contains(body, raw) {
		t.Fatalf("status leaks the webhook secret: %s", body)
	}

	// A forged delivery, a stale one, and one for a connection that isn't
	// there are refused, and answer with none of what was sent.
	wrong := bytes.Repeat([]byte{9}, 32)
	code, resp, _ := deliver(hookURL, push, signWith(wrong, push))
	if code != http.StatusUnauthorized || strings.Contains(resp, secret.SecretToken) || strings.Contains(resp, "grp/sub/api") {
		t.Fatalf("a delivery signed with the wrong secret got %d %q", code, resp)
	}
	if code, _, _ = deliver(hookURL, push, map[string]string{"X-Gitlab-Token": base64.RawURLEncoding.EncodeToString(wrong)}); code != http.StatusUnauthorized {
		t.Fatalf("a wrong secret token got %d", code)
	}
	if code, _, _ = deliver(hookURL, push, nil); code != http.StatusUnauthorized {
		t.Fatalf("an unsigned delivery got %d", code)
	}
	stale := signWith(key, push)
	old := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("msg_1." + old + "."))
	m.Write(push)
	stale["Webhook-Timestamp"], stale["Webhook-Signature"] = old, "v1,"+base64.StdEncoding.EncodeToString(m.Sum(nil))
	if code, _, _ = deliver(hookURL, push, stale); code != http.StatusUnauthorized {
		t.Fatalf("a replayed delivery got %d", code)
	}
	if code, _, _ = deliver(r.srv.URL+"/api/v1/gitlab/webhook/glc_none", push, signWith(key, push)); code != http.StatusNotFound {
		t.Fatalf("a delivery for an unknown connection got %d", code)
	}
	if a := analyses(ada, proj.ID); len(a) != 0 {
		t.Fatalf("refused deliveries started %+v", a)
	}

	// Events and branches that aren't the default branch's push do nothing.
	if code, _, n := deliver(hookURL, push, mergeEvent(signWith(key, push))); code != http.StatusAccepted || n != 0 {
		t.Fatalf("a merge request event: %d, %d", code, n)
	}
	other := gitLabPush("grp/sub/api", "refs/heads/feature")
	if code, _, n := deliver(hookURL, other, signWith(key, other)); code != http.StatusAccepted || n != 0 {
		t.Fatalf("a push to another branch: %d, %d", code, n)
	}

	// The signed push re-analyzes Ada's source, whatever case GitLab spells
	// the path in, and Grace's source on the same project hears nothing.
	cased := gitLabPush("Grp/Sub/API", "refs/heads/main")
	if code, resp, n := deliver(hookURL, cased, signWith(key, cased)); code != http.StatusAccepted || n != 1 {
		t.Fatalf("the signed push: %d, %d, %s", code, n, resp)
	}
	if a := analyses(ada, proj.ID); len(a) != 1 || a[0].Status != "queued" || a[0].Source != "api" {
		t.Fatalf("ada's analyses %+v", a)
	}
	if a := analyses(grace, graceProj.ID); len(a) != 0 {
		t.Fatalf("a push through Ada's connection started analyses in another organization: %+v", a)
	}
	var by string
	if err = db.QueryRow(context.Background(), `SELECT created_by FROM loomplane_analyses WHERE project_id = $1`, proj.ID).Scan(&by); err != nil || by != "gitlab-push" {
		t.Fatalf("created by %q, %v; want gitlab-push", by, err)
	}
	// GitLab before 19 sends the secret token instead. With an analysis
	// already under way the source is left alone, but the delivery is taken.
	if code, _, n := deliver(hookURL, push, map[string]string{"X-Gitlab-Token": secret.SecretToken}); code != http.StatusAccepted || n != 0 {
		t.Fatalf("the secret token while one runs: %d, %d", code, n)
	}
	if a := analyses(ada, proj.ID); len(a) != 1 {
		t.Fatalf("a push during an analysis queued another: %+v", a)
	}

	// Rotating replaces the secret: the old one stops working at once.
	var rotated struct {
		SecretToken string `json:"secret_token"`
	}
	ada.must(http.StatusOK, http.MethodPost, "/api/v1/gitlab/connections/"+conn.ID+"/webhook", nil, &rotated)
	if rotated.SecretToken == secret.SecretToken {
		t.Fatal("rotating kept the secret")
	}
	if code, _, _ := deliver(hookURL, push, map[string]string{"X-Gitlab-Token": secret.SecretToken}); code != http.StatusUnauthorized {
		t.Fatalf("the old secret token got %d after rotating", code)
	}
	if code, _, _ := deliver(hookURL, push, map[string]string{"X-Gitlab-Token": rotated.SecretToken}); code != http.StatusAccepted {
		t.Fatalf("the new secret token got %d", code)
	}

	// Disconnected, the webhook goes with the connection.
	ada.must(http.StatusNoContent, http.MethodDelete, "/api/v1/gitlab/connections/"+conn.ID, nil, nil)
	if code, _, _ := deliver(hookURL, push, map[string]string{"X-Gitlab-Token": rotated.SecretToken}); code != http.StatusNotFound {
		t.Fatalf("a delivery after disconnecting got %d", code)
	}
}

// mergeEvent turns a signed delivery's event into one the webhook ignores.
func mergeEvent(h map[string]string) map[string]string {
	h["X-Gitlab-Event"] = "Merge Request Hook"
	return h
}
