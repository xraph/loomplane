package connections

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

type fakeAppStore struct {
	AppStore
	apps map[string]*StoredApp
}

func (f *fakeAppStore) key(org, cloud string) string { return org + "/" + cloud }

func (f *fakeAppStore) GetApp(_ context.Context, org, cloud string) (*StoredApp, error) {
	if a, ok := f.apps[f.key(org, cloud)]; ok {
		return a, nil
	}
	return nil, ErrNotFound
}

func (f *fakeAppStore) PutApp(_ context.Context, a *StoredApp) error {
	f.apps[f.key(a.OrgID, a.Cloud)] = a
	return nil
}

func (f *fakeAppStore) DeleteApp(_ context.Context, org, cloud string) error {
	delete(f.apps, f.key(org, cloud))
	return nil
}

func (f *fakeAppStore) ListApps(_ context.Context, org string) ([]*StoredApp, error) {
	var out []*StoredApp
	for _, a := range f.apps {
		if a.OrgID == org {
			out = append(out, a)
		}
	}
	return out, nil
}

type fakeAppSecrets struct {
	Secrets
	kept map[string]string
}

func (f *fakeAppSecrets) Put(_ context.Context, org, cloud string, secret []byte) error {
	f.kept[org+"/"+cloud] = string(secret)
	return nil
}

func (f *fakeAppSecrets) Reveal(_ context.Context, org, cloud string) ([]byte, error) {
	if v, ok := f.kept[org+"/"+cloud]; ok {
		return []byte(v), nil
	}
	return nil, ErrNotFound
}

func (f *fakeAppSecrets) Delete(_ context.Context, org, cloud string) error {
	delete(f.kept, org+"/"+cloud)
	return nil
}

type appAudit struct {
	actions []string
	meta    []string
}

func (a *appAudit) Record(context.Context, string, security.Principal, *Connection) {}

func (a *appAudit) RecordApp(_ context.Context, action string, _ security.Principal, cloud, clientID string) {
	a.actions = append(a.actions, action)
	a.meta = append(a.meta, cloud+" "+clientID)
}

type noProvider struct{ Provider }

func signInService(config map[string]oauth.App, denied security.Permission) (*Service, *fakeAppStore, *fakeAppSecrets, *appAudit) {
	store := &fakeAppStore{apps: map[string]*StoredApp{}}
	secrets := &fakeAppSecrets{kept: map[string]string{}}
	audit := &appAudit{}
	return &Service{
		Providers:  map[string]Provider{DigitalOcean: noProvider{}, GCP: noProvider{}},
		ConfigApps: config, AppSecrets: secrets, Apps: store,
		authz: inventoryAuthorization{denied: denied}, audit: audit, now: time.Now,
	}, store, secrets, audit
}

var viewer = security.Principal{OrgID: "org", UserID: "viewer"}

func TestSignInAppsShowWhereEachCameFrom(t *testing.T) {
	s, store, _, _ := signInService(map[string]oauth.App{DigitalOcean: {ClientID: "cfg-id", ClientSecret: "cfg-secret"}}, security.Permission{})
	store.apps["org/"+GCP] = &StoredApp{OrgID: "org", Cloud: GCP, ClientID: "org-id", UpdatedBy: "u"}
	apps, err := s.SignInApps(t.Context(), viewer)
	if err != nil || len(apps) != 2 {
		t.Fatalf("apps=%v err=%v", apps, err)
	}
	if apps[0].Cloud != DigitalOcean || apps[0].Source != "config" || apps[0].ClientID != "cfg-id" {
		t.Fatalf("config app: %+v", apps[0])
	}
	if apps[1].Cloud != GCP || apps[1].Source != "organization" || apps[1].ClientID != "org-id" {
		t.Fatalf("organization app: %+v", apps[1])
	}
	delete(store.apps, "org/"+GCP)
	apps, _ = s.SignInApps(t.Context(), viewer)
	if apps[1].Source != "none" || apps[1].ClientID != "" {
		t.Fatalf("missing app: %+v", apps[1])
	}
}

func TestSetSignInAppIsRefusedWhenConfigSetsIt(t *testing.T) {
	s, _, secrets, _ := signInService(map[string]oauth.App{DigitalOcean: {ClientID: "x"}}, security.Permission{})
	_, err := s.SetSignInApp(t.Context(), viewer, DigitalOcean, AppInput{ClientID: "a", ClientSecret: "b"})
	if !errors.Is(err, ErrAppLocked) || len(secrets.kept) != 0 {
		t.Fatalf("err=%v kept=%v", err, secrets.kept)
	}
	if err := s.DeleteSignInApp(t.Context(), viewer, DigitalOcean); !errors.Is(err, ErrAppLocked) {
		t.Fatalf("delete: %v", err)
	}
}

func TestSetSignInAppNeedsCloudAccountManage(t *testing.T) {
	s, store, secrets, audit := signInService(nil, security.CloudAccountManage)
	_, err := s.SetSignInApp(t.Context(), viewer, DigitalOcean, AppInput{ClientID: "a", ClientSecret: "b"})
	if !errors.Is(err, security.ErrForbidden) || len(secrets.kept) != 0 || len(store.apps) != 0 || len(audit.actions) != 0 {
		t.Fatalf("err=%v kept=%v", err, secrets.kept)
	}
	if err := s.DeleteSignInApp(t.Context(), viewer, DigitalOcean); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("delete: %v", err)
	}
}

func TestSetSignInAppKeepsTheSecretInVaultOnly(t *testing.T) {
	s, store, secrets, audit := signInService(nil, security.Permission{})
	got, err := s.SetSignInApp(t.Context(), viewer, DigitalOcean, AppInput{ClientID: " the-id ", ClientSecret: "the-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if secrets.kept["org/"+DigitalOcean] != "the-secret" {
		t.Fatalf("vault: %v", secrets.kept)
	}
	if got.Source != "organization" || got.ClientID != "the-id" || got.UpdatedBy != "viewer" {
		t.Fatalf("returned: %+v", got)
	}
	if strings.Contains(strings.Join(audit.meta, "|"), "the-secret") || audit.actions[0] != "cloud_signin_app.set" || audit.meta[0] != "digitalocean the-id" {
		t.Fatalf("audit: %v %v", audit.actions, audit.meta)
	}
	if store.apps["org/"+DigitalOcean].ClientID != "the-id" {
		t.Fatalf("stored: %+v", store.apps)
	}
	if _, err := s.SetSignInApp(t.Context(), viewer, DigitalOcean, AppInput{ClientID: "", ClientSecret: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty id: %v", err)
	}
	if _, err := s.SetSignInApp(t.Context(), viewer, "nope", AppInput{ClientID: "a", ClientSecret: "b"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown cloud: %v", err)
	}
	if err := s.DeleteSignInApp(t.Context(), viewer, DigitalOcean); err != nil {
		t.Fatal(err)
	}
	if len(secrets.kept) != 0 || len(store.apps) != 0 || audit.actions[1] != "cloud_signin_app.delete" {
		t.Fatalf("after delete: %v %v %v", secrets.kept, store.apps, audit.actions)
	}
}

func TestAppResolutionPrefersConfig(t *testing.T) {
	s, store, secrets, _ := signInService(map[string]oauth.App{DigitalOcean: {ClientID: "cfg", ClientSecret: "cfg-secret"}}, security.Permission{})
	store.apps["org/"+DigitalOcean] = &StoredApp{OrgID: "org", Cloud: DigitalOcean, ClientID: "org-id"}
	secrets.kept["org/"+DigitalOcean] = "org-secret"
	app, err := s.app(t.Context(), "org", DigitalOcean)
	if err != nil || app.ClientID != "cfg" || app.ClientSecret != "cfg-secret" {
		t.Fatalf("%+v %v", app, err)
	}
	s.ConfigApps = nil
	app, err = s.app(t.Context(), "org", DigitalOcean)
	if err != nil || app.ClientID != "org-id" || app.ClientSecret != "org-secret" {
		t.Fatalf("%+v %v", app, err)
	}
}

func TestAppIsMissingWithoutConfigOrOrganization(t *testing.T) {
	s, _, _, _ := signInService(nil, security.Permission{})
	if _, err := s.app(t.Context(), "org", DigitalOcean); !errors.Is(err, ErrNoApp) {
		t.Fatalf("got %v", err)
	}
}
