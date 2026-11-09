//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/authsome/organization"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platformadmin"
	"github.com/xraph/loomplane/internal/setuptoken"
)

// ada is the setup form a fresh instance gets in these tests.
var ada = map[string]string{
	"name":              "Ada Lovelace",
	"email":             "ada@example.com",
	"password":          "Correct-horse-battery-9",
	"organization_name": "Acme",
}

type publicConfig struct {
	SetupPending       bool    `json:"setup_pending"`
	SetupTokenRequired bool    `json:"setup_token_required"`
	Waitlist           bool    `json:"waitlist"`
	ConsolePath        *string `json:"console_path"`
	PlatformAdmin      bool    `json:"platform_admin"`
	CloudSignInOffer   bool    `json:"cloud_sign_in_offer"`
}

// setupClient talks to the API the way the dashboard does in a browser:
// with the session cookie, never a bearer token.
type setupClient struct {
	t      *testing.T
	base   string
	key    string
	cookie *http.Cookie
	// from, when set, is the address requests say they come from. The test
	// server's peer is loopback, which authsome trusts as a proxy, so
	// X-Forwarded-For decides the address its rate limits count.
	from string
}

// send makes one request and returns the status, the body and the
// response's cookies.
func (c *setupClient) send(method, path string, body any) (int, []byte, []*http.Cookie) {
	c.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", c.key)
	if c.from != "" {
		req.Header.Set("X-Forwarded-For", c.from)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return res.StatusCode, raw, res.Cookies()
}

func (c *setupClient) get(path string, out any) {
	c.t.Helper()
	status, raw, _ := c.send(http.MethodGet, path, nil)
	if status != http.StatusOK {
		c.t.Fatalf("GET %s: %d %s", path, status, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		c.t.Fatalf("GET %s: decode %q: %v", path, raw, err)
	}
}

func (c *setupClient) publicConfig() publicConfig {
	c.t.Helper()
	var out publicConfig
	c.get("/api/v1/public-config", &out)
	return out
}

// setup posts the setup form, and on success keeps the session cookie it
// sets.
func (c *setupClient) setup(form map[string]string) (int, string) {
	c.t.Helper()
	status, raw, cookies := c.send(http.MethodPost, "/api/v1/setup", form)
	if status == http.StatusOK {
		for _, ck := range cookies {
			if ck.Name == "loomplane_session" && ck.Value != "" {
				c.cookie = ck
			}
		}
		if c.cookie == nil {
			c.t.Fatalf("setup succeeded without a loomplane_session cookie: %v", cookies)
		}
		if !c.cookie.HttpOnly {
			c.t.Fatal("the session cookie isn't HttpOnly")
		}
	}
	return status, string(raw)
}

func countRows(t *testing.T, dsn, query string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

const (
	liveUsers   = `SELECT count(*) FROM authsome_users WHERE deleted_at IS NULL`
	setupMarker = `SELECT count(*) FROM loomplane_instance_settings WHERE key = 'setup_completed' AND value::text = 'true'`
)

// setupStatusBody is GET /api/v1/setup's answer.
type setupStatusBody struct {
	Pending bool `json:"pending"`
}

// freshSetup starts a fresh instance as a deploy would: with no waitlist
// override unless the test sets one.
func freshSetup(t *testing.T, cfg *config.Config) (*running, *setupClient) {
	t.Helper()
	cfg.Database.URL = freshDatabase(t)
	r := startStoredWaitlist(t, *cfg)
	return r, &setupClient{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
}

func TestSetup_freshInstance(t *testing.T) {
	cfg := config.Defaults()
	r, c := freshSetup(t, &cfg)
	defer r.stop()

	var status struct {
		Pending bool `json:"pending"`
	}
	c.get("/api/v1/setup", &status)
	if !status.Pending {
		t.Fatal("GET /api/v1/setup says a fresh instance isn't pending")
	}
	if got := c.publicConfig(); !got.SetupPending || got.Waitlist || got.ConsolePath != nil {
		t.Fatalf("fresh public config = %+v, want pending, no waitlist, no console path", got)
	}

	code, body := c.setup(ada)
	if code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	var res struct {
		OK                   bool `json:"ok"`
		VerificationRequired bool `json:"verification_required"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil || !res.OK || res.VerificationRequired {
		t.Fatalf("setup body %s (err %v), want ok and no verification", body, err)
	}

	got := c.publicConfig()
	if got.SetupPending || !got.Waitlist || got.ConsolePath == nil || *got.ConsolePath != "/__console" {
		t.Fatalf("public config after setup = %+v, want not pending, waitlist on, console at /__console", got)
	}
	c.get("/api/v1/setup", &status)
	if status.Pending {
		t.Fatal("GET /api/v1/setup still pending after setup")
	}
	// Someone without a session never learns where the console is.
	anon := &setupClient{t: t, base: c.base, key: c.key}
	if got := anon.publicConfig(); got.ConsolePath != nil {
		t.Fatalf("an anonymous visitor was told the console path: %+v", got)
	}

	var orgs struct {
		Organizations []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"organizations"`
	}
	c.get("/authsome/v1/orgs", &orgs)
	if len(orgs.Organizations) != 1 || orgs.Organizations[0].Name != "Acme" || orgs.Organizations[0].Slug != "acme" {
		t.Fatalf("organizations = %+v, want only Acme", orgs.Organizations)
	}
	// The session already acts in Acme, so Loomplane's own API answers.
	var session identity.Session
	c.get("/api/v1/session", &session)
	if session.OrgID != orgs.Organizations[0].ID {
		t.Fatalf("session org %q, want Acme %q", session.OrgID, orgs.Organizations[0].ID)
	}
	var members identity.OrgMemberList
	c.get("/api/v1/members", &members)
	if len(members.Members) != 1 || members.Members[0].Email != "ada@example.com" || members.Members[0].Role != "owner" || members.Members[0].Name != "Ada Lovelace" {
		t.Fatalf("members = %+v, want Ada as owner", members.Members)
	}

	// Ada holds platform-owner in Loomplane's app.
	ctx := t.Context()
	users, roles, closeDB, err := platformadmin.Open(ctx, cfg.Database.URL, cfg.Identity.AppID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	id, err := users.UserIDByEmail(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	held, err := roles.ListUserRolesForApp(ctx, cfg.Identity.AppID, id)
	if err != nil {
		t.Fatal(err)
	}
	if !platformadmin.Holds(held, cfg.Identity.AppID) {
		t.Fatalf("Ada holds no platform role: %+v", held)
	}
}

// A production instance won't let the first visitor take it: setup needs
// the token derived from the secret key.
func TestSetup_productionNeedsToken(t *testing.T) {
	cfg := config.Defaults()
	cfg.Environment = config.EnvProduction
	r, c := freshSetup(t, &cfg)
	defer r.stop()

	if got := c.publicConfig(); !got.SetupPending || !got.SetupTokenRequired {
		t.Fatalf("fresh production public config = %+v, want pending and token required", got)
	}
	code, body := c.setup(ada)
	if code != http.StatusForbidden || !strings.Contains(body, "a valid setup token is required") {
		t.Fatalf("setup without a token: %d %s, want 403 a valid setup token is required", code, body)
	}
	wrong := map[string]string{"token": "nope"}
	for k, v := range ada {
		wrong[k] = v
	}
	if code, body := c.setup(wrong); code != http.StatusForbidden {
		t.Fatalf("setup with a wrong token: %d %s, want 403", code, body)
	}
	if n := countRows(t, cfg.Database.URL, liveUsers); n != 0 {
		t.Fatalf("%d users after refused setups, want 0", n)
	}

	token, err := setuptoken.Derive(cfg.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	form := map[string]string{"token": token}
	for k, v := range ada {
		form[k] = v
	}
	if code, body := c.setup(form); code != http.StatusOK {
		t.Fatalf("setup with the token: %d %s, want 200", code, body)
	}
	if got := c.publicConfig(); got.SetupPending || got.SetupTokenRequired {
		t.Fatalf("public config after setup = %+v, want not pending and no token required", got)
	}
}

func TestSetup_onlyOnce(t *testing.T) {
	cfg := config.Defaults()
	r, c := freshSetup(t, &cfg)
	defer r.stop()
	if code, body := c.setup(ada); code != http.StatusOK {
		t.Fatalf("first setup: %d %s", code, body)
	}
	again := &setupClient{t: t, base: c.base, key: c.key}
	second := map[string]string{"name": "Mallory", "email": "mallory@example.com", "password": "Correct-horse-battery-9", "organization_name": "Evil"}
	code, body := again.setup(second)
	if code != http.StatusForbidden || !strings.Contains(body, "setup has already been completed") {
		t.Fatalf("second setup: %d %s, want 403 setup has already been completed", code, body)
	}
}

// Any account can delete itself, which leaves only a tombstone. Once every
// account has gone, the instance must still know it was set up, or the
// next visitor could make themselves owner of what the old organizations
// left behind.
func TestSetup_staysDoneWhenEveryoneLeaves(t *testing.T) {
	cfg := config.Defaults()
	r, c := freshSetup(t, &cfg)
	defer r.stop()
	if code, body := c.setup(ada); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	if status, raw, _ := c.send(http.MethodDelete, "/authsome/v1/me", nil); status != http.StatusOK {
		t.Fatalf("Ada deletes her account: %d %s", status, raw)
	}
	if n := countRows(t, cfg.Database.URL, liveUsers); n != 0 {
		t.Fatalf("%d live users after the only one deleted her account, want 0", n)
	}

	anon := &setupClient{t: t, base: c.base, key: c.key}
	if got := anon.publicConfig(); got.SetupPending {
		t.Fatalf("public config with every account deleted = %+v, want setup done", got)
	}
	var status setupStatusBody
	anon.get("/api/v1/setup", &status)
	if status.Pending {
		t.Fatal("GET /api/v1/setup says pending once every account is deleted")
	}
	mallory := map[string]string{"name": "Mallory", "email": "mallory@example.com", "password": "Correct-horse-battery-9", "organization_name": "Evil"}
	if code, body := anon.setup(mallory); code != http.StatusForbidden || !strings.Contains(body, "setup has already been completed") {
		t.Fatalf("setup once every account is deleted: %d %s, want 403 setup has already been completed", code, body)
	}
	// Sign-up is judged as on any set-up instance, where setup turned the
	// waitlist on.
	if status, why := signUp(anon, "mallory@example.com"); status != http.StatusForbidden || why.Type != "waitlist_required" {
		t.Fatalf("sign-up once every account is deleted: %d %+v, want 403 waitlist_required", status, why)
	}
}

// An instance set up before setup kept a record of it has users and
// nothing stored. Starting it records that it's set up, so it stays that
// way whoever leaves. Nothing turned its waitlist on, so sign-up stays
// open, as it was.
func TestSetup_upgradedInstanceIsMarkedDone(t *testing.T) {
	cfg := config.Defaults()
	r, c := freshSetup(t, &cfg)
	if code, body := c.setup(ada); code != http.StatusOK {
		r.stop()
		t.Fatalf("setup: %d %s", code, body)
	}
	r.stop()
	execSQL(t, cfg.Database.URL, `DELETE FROM loomplane_instance_settings`)

	r = startStoredWaitlist(t, cfg)
	defer r.stop()
	if n := countRows(t, cfg.Database.URL, setupMarker); n != 1 {
		t.Fatalf("%d setup records after starting an instance that has users, want 1", n)
	}
	anon := &setupClient{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	if got := anon.publicConfig(); got.SetupPending || got.Waitlist {
		t.Fatalf("public config after the upgrade = %+v, want set up with the waitlist off", got)
	}
	if status, why := signUp(anon, "grace@example.com"); status != http.StatusCreated {
		t.Fatalf("a stranger's sign-up after the upgrade: %d %+v, want 201", status, why)
	}
}

func TestSetup_validation(t *testing.T) {
	cfg := config.Defaults()
	r, c := freshSetup(t, &cfg)
	defer r.stop()
	long := strings.Repeat("x", 65)
	// Five, the most one address may send a minute. The unit tests cover
	// the other refusals.
	for name, form := range map[string]map[string]string{
		"no name":         {"email": "ada@example.com", "password": "Correct-horse-battery-9", "organization_name": "Acme"},
		"bad email":       {"name": "Ada", "email": "not an email", "password": "Correct-horse-battery-9", "organization_name": "Acme"},
		"no organization": {"name": "Ada", "email": "ada@example.com", "password": "Correct-horse-battery-9"},
		"long org name":   {"name": "Ada", "email": "ada@example.com", "password": "Correct-horse-battery-9", "organization_name": long},
		"weak password":   {"name": "Ada", "email": "ada@example.com", "password": "pw", "organization_name": "Acme"},
	} {
		if code, body := c.setup(form); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, body)
		}
	}
	if code, body := c.setup(ada); code != http.StatusTooManyRequests {
		t.Fatalf("a sixth attempt in a minute: %d %s, want 429", code, body)
	}
	if !c.publicConfig().SetupPending {
		t.Fatal("a refused form left the instance set up")
	}
}

func TestSetup_race(t *testing.T) {
	cfg := config.Defaults()
	r, c := freshSetup(t, &cfg)
	defer r.stop()

	// Off the test goroutine, so each result comes back in results rather
	// than through t. post sends no cookie, so neither sees the other's.
	var wg sync.WaitGroup
	results := make([]postResult, 2)
	for i, email := range []string{"ada@example.com", "grace@example.com"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			form := map[string]string{"name": "Someone", "email": email, "password": "Correct-horse-battery-9", "organization_name": "Org " + email}
			results[i] = post(t.Context(), http.DefaultClient, c.base+"/api/v1/setup", c.key, form)
		}()
	}
	wg.Wait()
	ok, refused := 0, 0
	for i, res := range results {
		switch {
		case res.err != nil:
			t.Errorf("request %d: %v", i, res.err)
		case res.status == http.StatusOK:
			ok++
		case res.status == http.StatusForbidden && strings.Contains(res.body, "setup has already been completed"):
			refused++
		default:
			t.Errorf("request %d: %d %s", i, res.status, res.body)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("racing setups: %d succeeded and %d were refused (%+v), want one each", ok, refused, results)
	}
	if n := countRows(t, cfg.Database.URL, liveUsers); n != 1 {
		t.Fatalf("%d users after racing setups, want 1", n)
	}
}

func TestSetup_rollback(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	var failures atomic.Int32
	r := startApp(t, cfg, func(app forge.App) {
		ext, err := app.GetExtension(identity.Name)
		if err != nil {
			t.Fatal(err)
		}
		// The first organization is created and then reported as failed,
		// so the rollback has an organization to remove as well as a user.
		ext.(*identity.Extension).SetSetupOrgHook(func(ctx context.Context, o *organization.Organization, create func(context.Context, *organization.Organization) error) error {
			if err := create(ctx, o); err != nil {
				return err
			}
			if failures.Add(1) == 1 {
				return errors.New("injected organization failure")
			}
			return nil
		})
	})
	defer func() { r.stop() }()
	c := &setupClient{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}

	code, body := c.setup(ada)
	if code != http.StatusInternalServerError {
		t.Fatalf("setup with a failing organization: %d %s, want 500", code, body)
	}
	if c.cookie != nil {
		t.Fatal("a failed setup signed someone in")
	}
	if !c.publicConfig().SetupPending {
		t.Fatal("a failed setup left the instance set up")
	}
	if n := countRows(t, cfg.Database.URL, liveUsers); n != 0 {
		t.Fatalf("%d users after a failed setup, want 0", n)
	}
	if n := countRows(t, cfg.Database.URL, `SELECT count(*) FROM authsome_organizations`); n != 0 {
		t.Fatalf("%d organizations after a failed setup, want 0", n)
	}
	// The half-made user lost the platform role too.
	ctx := t.Context()
	_, roles, closeDB, err := platformadmin.Open(ctx, cfg.Database.URL, cfg.Identity.AppID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var gone string
	if err = conn.QueryRow(ctx, `SELECT id FROM authsome_users WHERE deleted_at IS NOT NULL`).Scan(&gone); err != nil {
		t.Fatalf("find the rolled-back user: %v", err)
	}
	held, err := roles.ListUserRolesForApp(ctx, cfg.Identity.AppID, gone)
	if err != nil {
		t.Fatal(err)
	}
	if platformadmin.Holds(held, cfg.Identity.AppID) {
		t.Fatalf("the rolled-back user still holds a platform role: %+v", held)
	}
	if n := countRows(t, cfg.Database.URL, setupMarker); n != 0 {
		t.Fatalf("a failed setup recorded the instance as set up (%d)", n)
	}

	// The rolled-back user's tombstone is no sign of an earlier setup, so a
	// restart leaves the instance waiting for one.
	r.stop()
	r = start(t, cfg)
	c.base = r.srv.URL
	if !c.publicConfig().SetupPending {
		t.Fatal("a restart after a failed setup left the instance set up")
	}

	// The same person can try again with the same email and organization.
	if code, body = c.setup(ada); code != http.StatusOK {
		t.Fatalf("setup after a rollback: %d %s", code, body)
	}
	if got := c.publicConfig(); got.SetupPending || got.ConsolePath == nil {
		t.Fatalf("public config after the retry = %+v", got)
	}
	if n := countRows(t, cfg.Database.URL, setupMarker); n != 1 {
		t.Fatalf("%d setup records after the retry, want 1", n)
	}
}

func TestSetup_envOverride(t *testing.T) {
	cfg := config.Defaults()
	off := false
	cfg.Identity.Waitlist = &off
	r, c := freshSetup(t, &cfg)
	defer r.stop()
	if code, body := c.setup(ada); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	if got := c.publicConfig(); got.Waitlist {
		t.Fatalf("public config = %+v, want the override to keep the waitlist off", got)
	}
}

// With email verification required, the account that runs setup is let
// in anyway: the setup token, or running the instance at all in
// development, already proves who it is. Anyone who signs up after it gets
// no session until they verify, in this tab or any other.
func TestSetup_verificationRequired(t *testing.T) {
	cfg := config.Defaults()
	cfg.Identity.RequireEmailVerification = true
	off := false
	cfg.Identity.Waitlist = &off
	r, c := freshSetup(t, &cfg)
	defer r.stop()

	code, body := c.setup(ada)
	if code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	var res struct {
		OK                   bool `json:"ok"`
		VerificationRequired bool `json:"verification_required"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil || !res.OK || res.VerificationRequired {
		t.Fatalf("setup body %s (err %v), want ok and the owner verified", body, err)
	}
	var sess struct {
		UserID string `json:"user_id"`
	}
	c.get("/api/v1/session", &sess)
	if sess.UserID == "" {
		t.Fatal("the setup owner has no working session")
	}

	newcomer := &setupClient{t: t, base: c.base, key: c.key}
	creds := map[string]string{"email": "grace@example.com", "password": "Hopper-Compiler-1952"}
	status, raw, cookies := newcomer.send(http.MethodPost, "/authsome/v1/signup", creds)
	if status != http.StatusCreated {
		t.Fatalf("sign-up: %d %s", status, raw)
	}
	var signedUp struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(raw, &signedUp); err != nil {
		t.Fatal(err)
	}
	if signedUp.SessionToken != "" {
		t.Fatal("an unverified sign-up got a session token")
	}
	for _, ck := range cookies {
		if ck.Name == "loomplane_session" && ck.Value != "" {
			t.Fatal("an unverified sign-up got a session cookie, which works in any tab")
		}
	}
	if status, raw, _ := newcomer.send(http.MethodPost, "/authsome/v1/signin", creds); status != http.StatusForbidden {
		t.Fatalf("an unverified sign-in: %d %s, want 403", status, raw)
	}
}

// An instance set up before setup verified its owner can have an owner
// whose email was never verified. Once verification is required, starting
// it verifies that owner, so the person who set it up isn't locked out.
func TestSetup_earlierOwnerIsVerifiedOnStart(t *testing.T) {
	cfg := config.Defaults()
	r, c := freshSetup(t, &cfg)
	if code, body := c.setup(ada); code != http.StatusOK {
		r.stop()
		t.Fatalf("setup: %d %s", code, body)
	}
	r.stop()
	execSQL(t, cfg.Database.URL, `UPDATE authsome_users SET email_verified = false`)

	cfg.Identity.RequireEmailVerification = true
	r = startStoredWaitlist(t, cfg)
	defer r.stop()
	if n := countRows(t, cfg.Database.URL, `SELECT count(*) FROM authsome_users WHERE email_verified`); n != 1 {
		t.Fatalf("%d verified users after the start, want the owner", n)
	}
	owner := &setupClient{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	creds := map[string]string{"email": ada["email"], "password": ada["password"]}
	if status, raw, _ := owner.send(http.MethodPost, "/authsome/v1/signin", creds); status != http.StatusOK {
		t.Fatalf("the owner's sign-in after the upgrade: %d %s, want 200", status, raw)
	}
}

// ordinaryMember invites email to owner's organization as a viewer and
// returns a client signed in as them. They hold no platform role.
func ordinaryMember(t *testing.T, owner *setupClient, email string) *setupClient {
	t.Helper()
	var created struct {
		AcceptPath string `json:"accept_path"`
	}
	status, raw, _ := owner.send(http.MethodPost, "/api/v1/invitations", map[string]string{"email": email, "role": "viewer"})
	if status != http.StatusCreated || json.Unmarshal(raw, &created) != nil {
		t.Fatalf("invite %s: %d %s", email, status, raw)
	}
	_, token, _ := strings.Cut(created.AcceptPath, "token=")
	if signupStatus, why := signUp(owner, email); signupStatus != http.StatusCreated {
		t.Fatalf("%s sign-up: %d %+v", email, signupStatus, why)
	}
	member := &setupClient{t: t, base: owner.base, key: owner.key}
	status, raw, cookies := member.send(http.MethodPost, "/authsome/v1/signin", map[string]string{"email": email, "password": "Correct-horse-battery-9"})
	if status != http.StatusOK {
		t.Fatalf("%s sign-in: %d %s", email, status, raw)
	}
	for _, ck := range cookies {
		if ck.Name == "loomplane_session" && ck.Value != "" {
			member.cookie = ck
		}
	}
	if member.cookie == nil {
		t.Fatalf("%s sign-in set no session cookie", email)
	}
	if status, raw, _ := member.send(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": token}); status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("%s accept: %d %s", email, status, raw)
	}
	var sess identity.Session
	owner.get("/api/v1/session", &sess)
	if status, raw, _ := member.send(http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": sess.OrgID}); status != http.StatusOK {
		t.Fatalf("%s switch org: %d %s", email, status, raw)
	}
	return member
}

// The platform owner's public config says so and carries the setup offer;
// an ordinary member's carries neither, and can't end the offer.
func TestSetup_cloudSignInOffer(t *testing.T) {
	cfg := config.Defaults()
	r, owner := freshSetup(t, &cfg)
	defer r.stop()
	if code, body := owner.setup(ada); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	if got := owner.publicConfig(); !got.PlatformAdmin || !got.CloudSignInOffer {
		t.Fatalf("owner public config = %+v, want platform_admin and cloud_sign_in_offer", got)
	}
	anon := &setupClient{t: t, base: owner.base, key: owner.key}
	if got := anon.publicConfig(); got.PlatformAdmin || got.CloudSignInOffer {
		t.Fatalf("anonymous public config = %+v, want neither", got)
	}

	grace := ordinaryMember(t, owner, "grace@example.com")
	if got := grace.publicConfig(); got.PlatformAdmin || got.CloudSignInOffer {
		t.Fatalf("an ordinary member's public config = %+v, want neither", got)
	}
	const path = "/api/v1/setup/cloud-sign-in-offer"
	if status, raw, _ := grace.send(http.MethodDelete, path, nil); status != http.StatusNotFound {
		t.Fatalf("an ordinary member ending the offer: %d %s, want 404", status, raw)
	}
	if got := owner.publicConfig(); !got.CloudSignInOffer {
		t.Fatal("the offer ended though the request was refused")
	}
	if status, raw, _ := owner.send(http.MethodDelete, path, nil); status != http.StatusNoContent {
		t.Fatalf("the owner ending the offer: %d %s, want 204", status, raw)
	}
	if got := owner.publicConfig(); !got.PlatformAdmin || got.CloudSignInOffer {
		t.Fatalf("owner public config after ending the offer = %+v, want platform_admin only", got)
	}
}

// The platform role is read live: once Warden no longer has it, the next
// request is no longer a platform admin's.
func TestPlatformUserRoleRevoked(t *testing.T) {
	cfg := config.Defaults()
	r, owner := freshSetup(t, &cfg)
	defer r.stop()
	if code, body := owner.setup(ada); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	if got := owner.publicConfig(); !got.PlatformAdmin {
		t.Fatalf("owner public config = %+v, want platform_admin", got)
	}

	ctx := t.Context()
	users, roles, closeDB, err := platformadmin.Open(ctx, cfg.Database.URL, cfg.Identity.AppID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	id, err := users.UserIDByEmail(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	held, err := roles.ListUserRolesForApp(ctx, cfg.Identity.AppID, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range held {
		if err := roles.UnassignUserRole(ctx, cfg.Identity.AppID, id, role.ID); err != nil {
			t.Fatal(err)
		}
	}
	got := owner.publicConfig()
	if got.PlatformAdmin || got.CloudSignInOffer || got.ConsolePath != nil {
		t.Fatalf("public config after the role was removed = %+v, want no platform fields", got)
	}
	if status, raw, _ := owner.send(http.MethodDelete, "/api/v1/setup/cloud-sign-in-offer", nil); status != http.StatusNotFound {
		t.Fatalf("ending the offer without the role: %d %s, want 404", status, raw)
	}
}
