package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"
	dashauth "github.com/xraph/forge/extensions/dashboard/auth"
	"github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
)

var operator = contract.Principal{User: &dashauth.UserInfo{Subject: "usr_ops", Email: "ops@example.com"}}

// fakeConsoleService records what the console asked of the service.
type fakeConsoleService struct {
	apps      []connections.SignInApp
	health    map[string]connections.SignInCounts
	healthErr error
	saved     []string
	removed   []string
	checked   []string
	setErr    error
	filter    connections.SignedInFilter
}

func (f *fakeConsoleService) SignInApps(context.Context) ([]connections.SignInApp, error) {
	return f.apps, nil
}

func (f *fakeConsoleService) SetSignInApp(_ context.Context, userID, cloud string, in connections.AppInput) (*connections.SignInApp, error) {
	if f.setErr != nil {
		return nil, f.setErr
	}
	f.saved = append(f.saved, userID+" "+cloud+" "+in.ClientID)
	return &connections.SignInApp{Cloud: cloud}, nil
}

func (f *fakeConsoleService) DeleteSignInApp(_ context.Context, userID, cloud string) error {
	f.removed = append(f.removed, userID+" "+cloud)
	return nil
}

func (f *fakeConsoleService) CheckSignInApp(_ context.Context, cloud string, in connections.AppCheckInput) (*connections.AppCheck, error) {
	f.checked = append(f.checked, cloud+" "+in.ClientID)
	return &connections.AppCheck{Result: connections.AppCheckAccepted}, nil
}

func (f *fakeConsoleService) SignInHealth(context.Context) (map[string]connections.SignInCounts, error) {
	return f.health, f.healthErr
}

func (f *fakeConsoleService) SignedIn(_ context.Context, in connections.SignedInFilter) (connections.SignedInPage, error) {
	f.filter = in
	return connections.SignedInPage{Items: []connections.SignedInConnection{}}, nil
}

type fakeUsers map[string]string

func (f fakeUsers) UserEmails(context.Context, []string) (map[string]string, error) { return f, nil }

func testConsole() (*console, *fakeConsoleService) {
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	svc := &fakeConsoleService{
		apps: []connections.SignInApp{
			{Cloud: connections.Azure, Source: connections.AppFromNone},
			{Cloud: connections.DigitalOcean, Source: connections.AppFromPlatform, ClientID: "do-app", UpdatedBy: "usr_1", UpdatedAt: &at, Connections: 3, Organizations: 2},
			{Cloud: connections.GCP, Source: connections.AppFromConfig, ClientID: "gcp-app"},
			{Cloud: connections.Vercel, Source: connections.AppFromPlatform, ClientID: "v", Slug: "loomplane-acme"},
		},
		health: map[string]connections.SignInCounts{
			connections.DigitalOcean: {Total: 4, Healthy: 3, AppChanged: 1},
		},
	}
	cfg := config.Config{PublicURL: "https://lp.example/"}
	cfg.GitHub.WebURL = "https://github.com"
	cfg.GitLab.APIURL = "https://gitlab.com"
	cfg.GitLab.ClientID = "gl-app"
	cfg.GitLab.Hosts = []config.GitLabHostConfig{{WebURL: "https://git.example.com", ClientID: "self"}}
	return &console{
		cfg: cfg, svc: svc, users: fakeUsers{"usr_1": "first@example.com"}, logger: forge.NewNoopLogger(),
		callbackURL: "https://lp.example/integrations/oauth/callback",
	}, svc
}

// The manifest loads, validates and registers with Forge's own loader, and
// every intent it declares has a handler.
func TestConsoleRegisters(t *testing.T) {
	c, _ := testConsole()
	reg := contract.NewRegistry()
	disp := dispatcher.New(nil)
	if err := registerConsole(disp, reg, nil, c); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"oauth.integrations", "oauth.integration", "connections.list", "oauth.save", "oauth.remove", "oauth.check"} {
		if _, ok := reg.Intent(ConsoleContributor, name, 1); !ok {
			t.Errorf("intent %s isn't registered", name)
		}
	}
	// A query goes through the dispatcher to its handler.
	raw, _, err := disp.Dispatch(t.Context(), contract.Request{
		Envelope: "v1", Kind: contract.KindQuery, Contributor: ConsoleContributor, Intent: "oauth.integrations", IntentVersion: 1,
		Payload: json.RawMessage(`{}`),
	}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"key":"digitalocean"`) {
		t.Fatalf("dispatched oauth.integrations: %s", raw)
	}
}

func TestConsoleIntegrations(t *testing.T) {
	c, _ := testConsole()
	got, err := c.integrations(t.Context(), struct{}{}, operator)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(got.Integrations))
	by := map[string]ConsoleIntegration{}
	for _, it := range got.Integrations {
		keys = append(keys, it.Key)
		by[it.Key] = it
	}
	want := []string{"azure", "digitalocean", "gcp", "vercel", "github", "gitlab", "gitlab-host:git.example.com", "bitbucket"}
	if !slices.Equal(keys, want) {
		t.Fatalf("keys %v, want %v", keys, want)
	}
	if !got.HealthAvailable || got.AppURL != "https://lp.example" || got.Totals.Total != 4 || got.Totals.AppChanged != 1 {
		t.Fatalf("response %+v", got)
	}
	do := by["digitalocean"]
	if !do.Editable || do.Kind != KindCloud || do.UpdatedBy != "first@example.com" || do.Organizations != 2 ||
		do.Connections == nil || do.Connections.Healthy != 3 || do.CallbackURL != c.callbackURL || do.Provider.ConsoleURL == "" {
		t.Fatalf("digitalocean %+v", do)
	}
	// Configuration wins, so the console can't change it.
	if g := by["gcp"]; g.Editable || g.Source != SourceConfig {
		t.Fatalf("gcp %+v", g)
	}
	if v := by["vercel"]; !v.OutlivesApp || len(v.Provider.Fields) == 0 {
		t.Fatalf("vercel %+v", v)
	}
	// Repository hosts are shown, never editable, with their own callbacks.
	if gh := by["github"]; gh.Editable || gh.Source != SourceNone || gh.CallbackURL != "https://lp.example/integrations/github/setup" {
		t.Fatalf("github %+v", gh)
	}
	if gl := by["gitlab-host:git.example.com"]; gl.Source != SourceConfig || gl.ClientID != "self" ||
		gl.Provider.ConsoleURL != "https://git.example.com/-/user_settings/applications" {
		t.Fatalf("self-managed gitlab %+v", gl)
	}
	if b := by["bitbucket"]; b.Source != SourceNone || b.Connections != nil {
		t.Fatalf("bitbucket %+v", b)
	}
}

func TestConsoleIntegrationsWithoutHealth(t *testing.T) {
	c, svc := testConsole()
	svc.healthErr = connections.ErrNoHealth
	got, err := c.integrations(t.Context(), struct{}{}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if got.HealthAvailable || got.Integrations[0].Connections != nil {
		t.Fatalf("health without a store: %+v", got)
	}
}

func TestConsoleIntegrationDetail(t *testing.T) {
	c, _ := testConsole()
	for key, want := range map[string]struct {
		env    []string
		config string
	}{
		"vercel":                      {[]string{"LOOMPLANE_VERCEL_OAUTH_CLIENT_ID", "LOOMPLANE_VERCEL_OAUTH_CLIENT_SECRET", "LOOMPLANE_VERCEL_OAUTH_SLUG"}, "clouds.oauth.vercel"},
		"gcp":                         {[]string{"LOOMPLANE_GCP_OAUTH_CLIENT_ID", "LOOMPLANE_GCP_OAUTH_CLIENT_SECRET"}, "clouds.oauth.gcp"},
		"bitbucket":                   {[]string{"LOOMPLANE_BITBUCKET_CLIENT_ID", "LOOMPLANE_BITBUCKET_CLIENT_SECRET"}, "bitbucket"},
		"gitlab-host:git.example.com": {[]string{}, "gitlab.hosts"},
	} {
		got, err := c.integration(t.Context(), keyInput{Key: key}, operator)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if !slices.Equal(got.EnvVars, want.env) || got.ConfigKey != want.config {
			t.Fatalf("%s: env %v config %q", key, got.EnvVars, got.ConfigKey)
		}
	}
	if _, err := c.integration(t.Context(), keyInput{Key: "nope"}, operator); codeOf(err) != contract.CodeNotFound {
		t.Fatalf("unknown key: %v", err)
	}
}

func TestConsoleCommands(t *testing.T) {
	c, svc := testConsole()
	if _, err := c.save(t.Context(), SaveInput{Key: "digitalocean", ClientID: "new"}, operator); err != nil {
		t.Fatal(err)
	}
	if _, err := c.remove(t.Context(), keyInput{Key: "azure"}, operator); err != nil {
		t.Fatal(err)
	}
	if _, err := c.check(t.Context(), CheckInput{Key: "gcp"}, operator); err != nil {
		t.Fatal(err)
	}
	// Each change is made as the signed-in operator, for the audit trail.
	if !slices.Equal(svc.saved, []string{"usr_ops digitalocean new"}) || !slices.Equal(svc.removed, []string{"usr_ops azure"}) ||
		!slices.Equal(svc.checked, []string{"gcp "}) {
		t.Fatalf("saved %v removed %v checked %v", svc.saved, svc.removed, svc.checked)
	}

	// Repository hosts live in configuration.
	// Without the discovery extension's store, repository apps are
	// read-only.
	if _, err := c.save(t.Context(), SaveInput{Key: "github", ClientID: "x"}, operator); codeOf(err) != contract.CodeUnavailable {
		t.Fatalf("save a repository host: %v", err)
	}
	if _, err := c.check(t.Context(), CheckInput{Key: "bitbucket"}, operator); codeOf(err) != contract.CodeBadRequest {
		t.Fatalf("check a repository host: %v", err)
	}
	if _, err := c.remove(t.Context(), keyInput{Key: "nowhere"}, operator); codeOf(err) != contract.CodeNotFound {
		t.Fatalf("remove an unknown key: %v", err)
	}
}

func TestConsoleMapsServiceErrors(t *testing.T) {
	c, svc := testConsole()
	for _, tc := range []struct {
		err  error
		code contract.ErrorCode
		msg  string
	}{
		{connections.ErrAppLocked, contract.CodeConflict, "configuration"},
		{fmt.Errorf("%w: give the app's client ID and client secret", connections.ErrInvalid), contract.CodeBadRequest, "Give the app's client ID and client secret."},
		{errors.New("pq: connection refused at 10.0.0.5"), contract.CodeInternal, "server log"},
	} {
		svc.setErr = tc.err
		_, err := c.save(t.Context(), SaveInput{Key: "digitalocean", ClientID: "x", ClientSecret: "y"}, operator)
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != tc.code || !strings.Contains(ce.Message, tc.msg) {
			t.Fatalf("%v became %v", tc.err, err)
		}
		// A cause the operator can't act on stays in the log.
		if strings.Contains(ce.Message, "10.0.0.5") {
			t.Fatalf("internal detail on the wire: %q", ce.Message)
		}
	}
}

func TestConsoleNeedsASignedInUser(t *testing.T) {
	c, svc := testConsole()
	if _, err := c.integrations(t.Context(), struct{}{}, contract.Principal{}); codeOf(err) != contract.CodeUnauthenticated {
		t.Fatalf("no user: %v", err)
	}
	if _, err := c.save(t.Context(), SaveInput{Key: "digitalocean", ClientID: "x"}, contract.Principal{}); codeOf(err) != contract.CodeUnauthenticated {
		t.Fatalf("no user: %v", err)
	}
	if len(svc.saved) != 0 {
		t.Fatalf("saved without a user: %v", svc.saved)
	}
}

func TestConsoleConnectionsPassesTheFilter(t *testing.T) {
	c, svc := testConsole()
	in := connections.SignedInFilter{Health: "problems", Cloud: "gcp", Search: "acme", Limit: 10, Offset: 20}
	if _, err := c.connectionsList(t.Context(), in, operator); err != nil {
		t.Fatal(err)
	}
	if svc.filter != in {
		t.Fatalf("filter %+v", svc.filter)
	}
}

func codeOf(err error) contract.ErrorCode {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}
