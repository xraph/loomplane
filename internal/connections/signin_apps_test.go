package connections

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

type fakeAppStore struct {
	AppStore
	apps     map[string]*StoredApp
	signIns  map[string]*PendingSignIn
	problems map[string]string
}

func (f *fakeAppStore) GetApp(_ context.Context, cloud string) (*StoredApp, error) {
	if a, ok := f.apps[cloud]; ok {
		return a, nil
	}
	return nil, ErrNotFound
}

func (f *fakeAppStore) PutApp(_ context.Context, a *StoredApp) error {
	f.apps[a.Cloud] = a
	return nil
}

func (f *fakeAppStore) DeleteApp(_ context.Context, cloud string) error {
	delete(f.apps, cloud)
	return nil
}

func (f *fakeAppStore) ListApps(context.Context) ([]*StoredApp, error) {
	var out []*StoredApp
	for _, a := range f.apps {
		out = append(out, a)
	}
	return out, nil
}

type fakeAppSecrets struct{ kept map[string]string }

func (f *fakeAppSecrets) Put(_ context.Context, cloud string, secret []byte) error {
	f.kept[cloud] = string(secret)
	return nil
}

func (f *fakeAppSecrets) Reveal(_ context.Context, cloud string) ([]byte, error) {
	if v, ok := f.kept[cloud]; ok {
		return []byte(v), nil
	}
	return nil, ErrNotFound
}

func (f *fakeAppSecrets) Delete(_ context.Context, cloud string) error {
	delete(f.kept, cloud)
	return nil
}

type appAudit struct {
	actions []string
	meta    []string
}

func (a *appAudit) Record(context.Context, string, security.Principal, *Connection) {}

func (a *appAudit) RecordApp(_ context.Context, action, userID, cloud, clientID string) {
	a.actions = append(a.actions, action)
	a.meta = append(a.meta, userID+" "+cloud+" "+clientID)
}

type noProvider struct{ Provider }

func signInService(config map[string]oauth.App) (*Service, *fakeAppStore, *fakeAppSecrets, *appAudit) {
	store := &fakeAppStore{apps: map[string]*StoredApp{}}
	secrets := &fakeAppSecrets{kept: map[string]string{}}
	audit := &appAudit{}
	return &Service{
		Providers:  map[string]Provider{DigitalOcean: noProvider{}, GCP: noProvider{}, Azure: noProvider{}},
		ConfigApps: config, AppSecrets: secrets, Apps: store,
		authz: inventoryAuthorization{}, audit: audit, now: time.Now,
	}, store, secrets, audit
}

func TestSignInAppsPrecedence(t *testing.T) {
	s, store, _, _ := signInService(map[string]oauth.App{DigitalOcean: {ClientID: "X", ClientSecret: "cfg-secret"}})
	store.apps[DigitalOcean] = &StoredApp{Cloud: DigitalOcean, ClientID: "Y", UpdatedBy: "u"}
	store.apps[GCP] = &StoredApp{Cloud: GCP, ClientID: "Y", UpdatedBy: "u"}
	apps, err := s.SignInApps(t.Context())
	if err != nil || len(apps) != 3 {
		t.Fatalf("apps=%v err=%v", apps, err)
	}
	by := map[string]SignInApp{}
	for _, a := range apps {
		by[a.Cloud] = a
	}
	if a := by[DigitalOcean]; a.Source != "config" || a.ClientID != "X" || a.UpdatedBy != "" {
		t.Fatalf("config over platform: %+v", a)
	}
	if a := by[GCP]; a.Source != "platform" || a.ClientID != "Y" || a.UpdatedBy != "u" {
		t.Fatalf("platform app: %+v", a)
	}
	if a := by[Azure]; a.Source != "none" || a.ClientID != "" {
		t.Fatalf("no app: %+v", a)
	}
}

func TestSetSignInAppTrims(t *testing.T) {
	s, store, secrets, audit := signInService(nil)
	got, err := s.SetSignInApp(t.Context(), "usr_1", DigitalOcean, AppInput{ClientID: "  abc  ", ClientSecret: " s "})
	if err != nil {
		t.Fatal(err)
	}
	if store.apps[DigitalOcean].ClientID != "abc" || got.ClientID != "abc" || secrets.kept[DigitalOcean] != "s" {
		t.Fatalf("stored %+v, returned %+v, secret %q", store.apps[DigitalOcean], got, secrets.kept[DigitalOcean])
	}
	if got.Source != AppFromPlatform || got.UpdatedBy != "usr_1" || audit.actions[0] != "cloud_signin_app.set" || audit.meta[0] != "usr_1 digitalocean abc" {
		t.Fatalf("returned %+v, audit %v %v", got, audit.actions, audit.meta)
	}
}

func TestSetSignInAppLocked(t *testing.T) {
	s, store, secrets, audit := signInService(map[string]oauth.App{DigitalOcean: {ClientID: "x"}})
	_, err := s.SetSignInApp(t.Context(), "usr_1", DigitalOcean, AppInput{ClientID: "a", ClientSecret: "b"})
	if !errors.Is(err, ErrAppLocked) || len(secrets.kept) != 0 || len(store.apps) != 0 || len(audit.actions) != 0 {
		t.Fatalf("err=%v kept=%v", err, secrets.kept)
	}
	if err := s.DeleteSignInApp(t.Context(), "usr_1", DigitalOcean); !errors.Is(err, ErrAppLocked) {
		t.Fatalf("delete: %v", err)
	}
}

func TestSetSignInAppVercelSlug(t *testing.T) {
	s, store, secrets, _ := signInService(nil)
	s.Providers[Vercel] = noProvider{}
	if _, err := s.SetSignInApp(t.Context(), "usr_1", Vercel, AppInput{ClientID: "oac_1", ClientSecret: "sec"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "slug") {
		t.Fatalf("no slug: %v", err)
	}
	if len(secrets.kept) != 0 || len(store.apps) != 0 {
		t.Fatalf("kept %v, stored %v", secrets.kept, store.apps)
	}
	got, err := s.SetSignInApp(t.Context(), "usr_1", Vercel, AppInput{ClientID: "oac_1", ClientSecret: "sec", Slug: " loomplane "})
	if err != nil || got.Slug != "loomplane" || store.apps[Vercel].Slug != "loomplane" {
		t.Fatalf("returned %+v, stored %+v: %v", got, store.apps[Vercel], err)
	}
	if app, err := s.app(t.Context(), Vercel); err != nil || app.Slug != "loomplane" {
		t.Fatalf("app %+v: %v", app, err)
	}
	if _, err := s.SetSignInApp(t.Context(), "usr_1", DigitalOcean, AppInput{ClientID: "a", ClientSecret: "b", Slug: "ignored"}); err != nil {
		t.Fatal(err)
	}
	if store.apps[DigitalOcean].Slug != "" {
		t.Fatalf("digitalocean kept a slug: %+v", store.apps[DigitalOcean])
	}
}

func TestSetSignInAppValidates(t *testing.T) {
	s, store, secrets, audit := signInService(nil)
	for _, c := range []struct {
		cloud string
		in    AppInput
	}{
		{DigitalOcean, AppInput{ClientSecret: "x"}},
		{DigitalOcean, AppInput{ClientID: "x"}},
		{"nope", AppInput{ClientID: "a", ClientSecret: "b"}},
	} {
		if _, err := s.SetSignInApp(t.Context(), "usr_1", c.cloud, c.in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s %+v: %v", c.cloud, c.in, err)
		}
	}
	if len(secrets.kept) != 0 || len(store.apps) != 0 || len(audit.actions) != 0 {
		t.Fatalf("something was saved: %v %v %v", secrets.kept, store.apps, audit.actions)
	}
}

func TestSignInAppNeverCarriesSecret(t *testing.T) {
	s, _, _, audit := signInService(map[string]oauth.App{GCP: {ClientID: "cfg", ClientSecret: "cfg-secret-value"}})
	if _, err := s.SetSignInApp(t.Context(), "usr_1", DigitalOcean, AppInput{ClientID: "the-id", ClientSecret: "platform-secret-value"}); err != nil {
		t.Fatal(err)
	}
	apps, err := s.SignInApps(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	shown, _ := json.Marshal(apps)
	for _, secret := range []string{"cfg-secret-value", "platform-secret-value"} {
		if strings.Contains(string(shown), secret) || strings.Contains(strings.Join(audit.meta, "|"), secret) {
			t.Fatalf("%q leaked: %s %v", secret, shown, audit.meta)
		}
	}
}

func TestSignInClouds(t *testing.T) {
	s, store, _, _ := signInService(map[string]oauth.App{DigitalOcean: {ClientID: "cfg", ClientSecret: "x"}})
	store.apps[GCP] = &StoredApp{Cloud: GCP, ClientID: "p"}
	got, err := s.SignInClouds(t.Context())
	want := map[string]bool{DigitalOcean: true, GCP: true, Azure: false}
	if err != nil || !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v: %v", got, want, err)
	}
}

func TestDeleteSignInApp(t *testing.T) {
	s, store, secrets, audit := signInService(nil)
	if _, err := s.SetSignInApp(t.Context(), "usr_1", DigitalOcean, AppInput{ClientID: "id", ClientSecret: "sec"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSignInApp(t.Context(), "usr_2", DigitalOcean); err != nil {
		t.Fatal(err)
	}
	if len(secrets.kept) != 0 || len(store.apps) != 0 || audit.actions[1] != "cloud_signin_app.delete" || audit.meta[1] != "usr_2 digitalocean id" {
		t.Fatalf("after delete: %v %v %v %v", secrets.kept, store.apps, audit.actions, audit.meta)
	}
	if err := s.DeleteSignInApp(t.Context(), "usr_2", DigitalOcean); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAppResolutionPrefersConfig(t *testing.T) {
	s, store, secrets, _ := signInService(map[string]oauth.App{DigitalOcean: {ClientID: "cfg", ClientSecret: "cfg-secret"}})
	store.apps[DigitalOcean] = &StoredApp{Cloud: DigitalOcean, ClientID: "platform-id"}
	secrets.kept[DigitalOcean] = "platform-secret"
	app, err := s.app(t.Context(), DigitalOcean)
	if err != nil || app.ClientID != "cfg" || app.ClientSecret != "cfg-secret" {
		t.Fatalf("%+v %v", app, err)
	}
	s.ConfigApps = nil
	app, err = s.app(t.Context(), DigitalOcean)
	if err != nil || app.ClientID != "platform-id" || app.ClientSecret != "platform-secret" {
		t.Fatalf("%+v %v", app, err)
	}
}

func TestAppIsMissingWithoutConfigOrPlatform(t *testing.T) {
	s, _, _, _ := signInService(nil)
	if _, err := s.app(t.Context(), DigitalOcean); !errors.Is(err, ErrNoApp) {
		t.Fatalf("got %v", err)
	}
}

func (f *fakeAppStore) SweepReservations(context.Context, time.Time) (int64, error) { return 0, nil }

func (f *fakeAppStore) SweepSignIns(_ context.Context, before time.Time) (int64, error) {
	var n int64
	for state, p := range f.signIns {
		if p.ExpiresAt.Before(before) {
			delete(f.signIns, state)
			n++
		}
	}
	return n, nil
}

func (f *fakeAppStore) SetAppProblem(_ context.Context, cloud, message string, _ time.Time) error {
	if _, ok := f.apps[cloud]; !ok {
		return nil
	}
	if f.problems == nil {
		f.problems = map[string]string{}
	}
	f.problems[cloud] = message
	return nil
}

func (f *fakeAppStore) ClearAppProblem(_ context.Context, cloud string) error {
	delete(f.problems, cloud)
	return nil
}

func (f *fakeAppStore) AppProblems(context.Context) (map[string]string, error) {
	return maps.Clone(f.problems), nil
}

// A removed app's problem goes with it, so a new app doesn't start out
// showing the old one's.
func TestDeleteSignInAppClearsItsProblem(t *testing.T) {
	s, store, _, _ := signInService(nil)
	if _, err := s.SetSignInApp(t.Context(), "usr_1", GCP, AppInput{ClientID: "id", ClientSecret: "old"}); err != nil {
		t.Fatal(err)
	}
	s.appRejected(t.Context(), GCP, "id")
	if store.problems[GCP] == "" {
		t.Fatal("no problem was recorded")
	}
	if err := s.DeleteSignInApp(t.Context(), "usr_1", GCP); err != nil {
		t.Fatal(err)
	}
	apps, err := s.SignInApps(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		if a.Problem != "" {
			t.Fatalf("the deleted app's problem stayed: %+v", a)
		}
	}
}

// usePlatformApp makes app the platform's app for cloud, with no
// configuration over it.
func usePlatformApp(s *Service, cloud string, app oauth.App) {
	delete(s.ConfigApps, cloud)
	s.Apps.(*fakeAppStore).apps[cloud] = &StoredApp{Cloud: cloud, ClientID: app.ClientID, Slug: app.Slug}
	s.AppSecrets.(*fakeAppSecrets).kept[cloud] = app.ClientSecret
}

// A config app that the cloud refuses is not the platform app under it, so
// the platform row doesn't get its problem.
func TestRefusedConfigAppLeavesTheShadowedPlatformRowAlone(t *testing.T) {
	s, store, _, _ := signInService(map[string]oauth.App{GCP: {ClientID: "cfg", ClientSecret: "x"}})
	store.apps[GCP] = &StoredApp{Cloud: GCP, ClientID: "platform"}
	if why := s.appRejected(t.Context(), GCP, "cfg"); why == "" {
		t.Fatal("no message")
	}
	if got, _ := store.AppProblems(t.Context()); len(got) != 0 {
		t.Fatalf("problems %v", got)
	}
	s.ConfigApps = nil
	s.appRejected(t.Context(), GCP, "platform")
	if got, _ := store.AppProblems(t.Context()); len(got) != 1 {
		t.Fatalf("a platform app's problem wasn't recorded: %v", got)
	}
}

func (m *memStore) SignedInWith(_ context.Context, cloud, clientID string) (int, int, error) {
	orgs := map[string]bool{}
	n := 0
	for _, c := range m.rows {
		if c.Cloud == cloud && c.Method == MethodOAuth && c.OAuthClientID == clientID && c.Status != StatusSignInNeeded {
			n++
			orgs[c.OrgID] = true
		}
	}
	return n, len(orgs), nil
}

func TestSignInAppsCounts(t *testing.T) {
	s, store, _, _ := signInService(nil)
	s.Providers[Vercel] = independentRenewer{}
	mem := &memStore{rows: map[string]Connection{
		"a": {ID: "a", OrgID: "o1", Cloud: DigitalOcean, Method: MethodOAuth, OAuthClientID: "id", Status: StatusReady},
		"b": {ID: "b", OrgID: "o2", Cloud: DigitalOcean, Method: MethodOAuth, OAuthClientID: "id", Status: StatusReady},
		"c": {ID: "c", OrgID: "o2", Cloud: DigitalOcean, Method: MethodOAuth, OAuthClientID: "id", Status: StatusSignInNeeded},
		"d": {ID: "d", OrgID: "o1", Cloud: DigitalOcean, Method: MethodOAuth, OAuthClientID: "other", Status: StatusReady},
		"e": {ID: "e", OrgID: "o1", Cloud: Vercel, Method: MethodOAuth, OAuthClientID: "vid", Status: StatusReady},
	}}
	s.store = mem
	store.apps[DigitalOcean] = &StoredApp{Cloud: DigitalOcean, ClientID: "id"}
	store.apps[Vercel] = &StoredApp{Cloud: Vercel, ClientID: "vid", Slug: "x"}
	apps, err := s.SignInApps(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		switch a.Cloud {
		case DigitalOcean:
			if a.Connections != 2 || a.Organizations != 2 {
				t.Fatalf("digitalocean: %+v", a)
			}
		case Vercel:
			if a.Connections != 0 || a.Organizations != 0 {
				t.Fatalf("vercel: %+v", a)
			}
		}
	}
}
