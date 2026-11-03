//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ctl"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestDeviceLogin_signsInAsTheApproverInTheOrganizationTheyPick(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	ctx := context.Background()

	signUp := func(email string) *client {
		c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
		account(t, c, email)
		return c
	}
	type org struct {
		ID string `json:"id"`
	}
	ada := signUp("ada@example.com")
	var engines, looms, strangers org
	ada.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &engines)
	ada.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Looms", "slug": "looms"}, &looms)
	bob := signUp("bob@example.com")
	bob.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Strangers", "slug": "strangers"}, &strangers)

	login, err := lpclient.StartDeviceLogin(ctx, r.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(login.VerificationURI, "/device") || login.VerificationURIComplete != login.VerificationURI+"?code="+login.UserCode {
		t.Fatalf("login %+v", login)
	}
	// The approval needs a signed-in person; the code alone does nothing.
	anonymous := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	anonymous.must(http.StatusUnauthorized, http.MethodPost, "/authsome/v1/oauth/device/complete", map[string]string{"user_code": login.UserCode, "action": "approve"}, nil)
	// Without the dash, the way someone might type it.
	ada.must(http.StatusOK, http.MethodPost, "/authsome/v1/oauth/device/complete", map[string]string{"user_code": strings.ReplaceAll(login.UserCode, "-", ""), "action": "approve"}, nil)
	tok, err := login.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatalf("token %+v", tok)
	}

	// The token is Ada, in no organization yet.
	cli := lpclient.New(r.srv.URL, tok.AccessToken)
	if _, noOrgErr := cli.Session(ctx); !errors.Is(noOrgErr, lpclient.ErrForbidden) {
		t.Fatalf("session before picking an organization: %v", noOrgErr)
	}
	orgs, err := cli.Organizations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Ada's, and only Ada's: setup gave the first account one more.
	ids := map[string]bool{}
	for _, o := range orgs.Organizations {
		ids[o.ID] = true
	}
	if !ids[engines.ID] || !ids[looms.ID] || ids[strangers.ID] || orgs.Active != "" {
		t.Fatalf("organizations %+v", orgs)
	}
	if _, useErr := cli.UseOrganization(ctx, strangers.ID); !errors.Is(useErr, lpclient.ErrForbidden) {
		t.Fatalf("picking a stranger's organization: %v", useErr)
	}
	if orgs, err = cli.UseOrganization(ctx, engines.ID); err != nil || orgs.Active != engines.ID {
		t.Fatalf("use engines: %+v %v", orgs, err)
	}
	who, err := cli.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if who.OrgID != engines.ID || who.UserID == "" || len(who.Permissions) == 0 {
		t.Fatalf("session %+v", who)
	}
	if _, projErr := cli.Projects(ctx); projErr != nil {
		t.Fatal(projErr)
	}

	// A refreshed token keeps the organization.
	fresh, err := lpclient.RefreshToken(ctx, r.srv.URL, tok.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if who, err = lpclient.New(r.srv.URL, fresh.AccessToken).Session(ctx); err != nil || who.OrgID != engines.ID {
		t.Fatalf("session after refresh: %+v %v", who, err)
	}

	// Revoked, it's nobody.
	if revokeErr := lpclient.RevokeToken(ctx, r.srv.URL, fresh.AccessToken); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if _, revokedErr := lpclient.New(r.srv.URL, fresh.AccessToken).Session(ctx); !errors.Is(revokedErr, lpclient.ErrUnauthorized) {
		t.Fatalf("session after revoke: %v", revokedErr)
	}

	// A code someone denies gets no token.
	denied, err := lpclient.StartDeviceLogin(ctx, r.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ada.must(http.StatusOK, http.MethodPost, "/authsome/v1/oauth/device/complete", map[string]string{"user_code": denied.UserCode, "action": "deny"}, nil)
	if _, err := denied.Wait(ctx); !errors.Is(err, lpclient.ErrDeviceDenied) {
		t.Fatalf("denied login: %v", err)
	}
}

func TestDeviceLogin_loomctlSignsInThroughTheBrowser(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()

	ada := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	account(t, ada, "ada@example.com")
	var engines, looms struct {
		ID string `json:"id"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &engines)
	ada.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Looms", "slug": "looms"}, &looms)

	path := filepath.Join(t.TempDir(), "loomctl.json")
	var out bytes.Buffer
	opened := ""
	app := ctl.New(ctl.Options{
		Out: &out, In: strings.NewReader("looms\n"), ConfigPath: path,
		Getenv: func(string) string { return "" },
		// Ada's browser: she opens the page and approves the code it shows.
		OpenBrowser: func(link string) error {
			opened = link
			u, err := url.Parse(link)
			if err != nil {
				return err
			}
			ada.must(http.StatusOK, http.MethodPost, "/authsome/v1/oauth/device/complete", map[string]string{"user_code": u.Query().Get("code"), "action": "approve"}, nil)
			return nil
		},
	})
	run := func(args ...string) string {
		t.Helper()
		out.Reset()
		if err := app.Run(args); err != nil {
			t.Fatalf("loomctl %s: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		return out.String()
	}
	whoami := func() string {
		t.Helper()
		var who lpclient.Session
		if err := json.Unmarshal([]byte(run("whoami", "--json")), &who); err != nil {
			t.Fatal(err)
		}
		return who.OrgID
	}

	said := run("login", "--url", r.srv.URL)
	if !strings.Contains(opened, "/device?code=") || !strings.Contains(said, "Logged in to "+r.srv.URL+", in Looms (looms).") {
		t.Fatalf("opened %q, said:\n%s", opened, said)
	}
	if org := whoami(); org != looms.ID {
		t.Fatalf("acting in %s, want looms", org)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the saved login: %v %v", info, err)
	}

	run("organizations", "use", "engines")
	if org := whoami(); org != engines.ID {
		t.Fatalf("acting in %s after switching, want engines", org)
	}

	var saved ctl.Settings
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &saved) != nil {
		t.Fatalf("read the saved login: %v", err)
	}
	if said := run("logout"); !strings.Contains(said, "ended the session") {
		t.Fatalf("logout said %q", said)
	}
	if _, err := lpclient.New(r.srv.URL, saved.Key).Session(context.Background()); !errors.Is(err, lpclient.ErrUnauthorized) {
		t.Fatalf("the token after logout: %v", err)
	}
}
