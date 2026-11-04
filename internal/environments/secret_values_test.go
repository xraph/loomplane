package environments

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// readOnlyStore fails the test on any write.
type readOnlyStore struct {
	*memStore
	t *testing.T
}

func (s readOnlyStore) CreateVariable(context.Context, *Variable) error {
	s.t.Error("SecretValuesFor created a variable")
	return nil
}

func (s readOnlyStore) UpdateVariable(context.Context, *Variable) error {
	s.t.Error("SecretValuesFor updated a variable")
	return nil
}

func (s readOnlyStore) DeleteVariable(context.Context, string, string) error {
	s.t.Error("SecretValuesFor deleted a variable")
	return nil
}

// readOnlySecrets fails the test on any write to Vault.
type readOnlySecrets struct {
	*VaultSecrets
	t *testing.T
}

func (s readOnlySecrets) Put(context.Context, string, string, []byte) error {
	s.t.Error("SecretValuesFor stored a secret")
	return nil
}

func (s readOnlySecrets) Delete(context.Context, string, string) error {
	s.t.Error("SecretValuesFor deleted a secret")
	return nil
}

func TestSecretValuesForCoverEveryLayerAndService(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.as(t, "ausr_admin", security.RoleAdmin)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	mustCreate := func(v *Variable, err error) *Variable {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	mustCreate(f.svc.CreateOrganization(ctx, admin, CreateVariableInput{Key: "ORG_TOKEN", Value: "org-secret-1", Secret: true}))
	mustCreate(f.svc.CreateOrganization(ctx, admin, CreateVariableInput{Key: "ORG_STAGING", Value: "org-staging-2", Secret: true, Environment: "staging"}))
	mustCreate(f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "DATABASE_URL", Value: "postgres://app:pw-proj@db/app", Secret: true}))
	// Production overrides it; the overridden value still counts.
	mustCreate(f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "DATABASE_URL", Value: "postgres://app:pw-prod@db/app", Secret: true, Environment: "production"}))
	mustCreate(f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "API_KEY", Value: "preview-key-3", Secret: true, Environment: "preview"}))
	web := mustCreate(f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "WEB_SECRET", Value: "web-secret-4", Secret: true, Service: webSvc}))
	mustCreate(f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "REGION", Value: "eu"}))

	svc := NewService(readOnlyStore{f.store, t}, readOnlySecrets{f.secrets, t}, fakeProjects{}, fakeServices{}, f.authz, nil, func(msg string, err error) {
		t.Errorf("%s: %v", msg, err)
	})
	sorted := func(env string) []string {
		t.Helper()
		got, err := svc.SecretValuesFor(ctx, org, project, env)
		if err != nil {
			t.Fatalf("SecretValuesFor %q: %v", env, err)
		}
		slices.Sort(got)
		return got
	}
	want := []string{"org-secret-1", "postgres://app:pw-prod@db/app", "postgres://app:pw-proj@db/app", "web-secret-4"}
	if got := sorted("production"); !slices.Equal(got, want) {
		t.Fatalf("production: %v", got)
	}
	want = []string{"org-secret-1", "org-staging-2", "postgres://app:pw-prod@db/app", "postgres://app:pw-proj@db/app", "preview-key-3", "web-secret-4"}
	if got := sorted(""); !slices.Equal(got, want) {
		t.Fatalf("every environment: %v", got)
	}

	// A report about an environment that was deleted still has every
	// secret the project could carry redacted.
	if got := sorted("staging"); !slices.Equal(got, want) {
		t.Fatalf("an environment the project doesn't have: %v", got)
	}
	if _, err := svc.SecretValuesFor(ctx, org, "proj_gone", ""); !errors.Is(err, projects.ErrNotFound) {
		t.Fatalf("another project: %v", err)
	}

	// A secret Vault can't return is named, and the rest still come back.
	if err := f.secrets.Delete(ctx, org, web.ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.SecretValuesFor(ctx, org, project, "")
	if err == nil || !strings.Contains(err.Error(), "WEB_SECRET") || strings.Contains(err.Error(), "web-secret-4") {
		t.Fatalf("a missing secret: %v", err)
	}
	if !slices.Contains(got, "preview-key-3") || slices.Contains(got, "web-secret-4") || len(got) != 5 {
		t.Fatalf("values with one missing: %v", got)
	}
}

// A secret pinned to a version is read through its pin, the way a
// deployment reads it, and a pin Vault can't honour is an error rather
// than a fall back to the latest value.
func TestSecretValuesForReadPinnedVersions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	v, err := f.svc.CreateProject(ctx, dev, project, CreateVariableInput{Key: "TOKEN", Value: "first-value", Secret: true})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := f.secrets.Metadata(ctx, org, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	setPin := func(p SecretVersion) {
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		f.store.vars[v.ID].secretIdentity, f.store.vars[v.ID].secretVersion = p.Identity, p.Version
	}
	setPin(pin)
	got, err := f.svc.SecretValuesFor(ctx, org, project, "production")
	if err != nil || !slices.Equal(got, []string{"first-value"}) {
		t.Fatalf("pinned: %v, %v", got, err)
	}
	setPin(SecretVersion{Identity: pin.Identity, Version: pin.Version + 5})
	if got, err = f.svc.SecretValuesFor(ctx, org, project, "production"); err == nil || len(got) != 0 {
		t.Fatalf("a pin Vault doesn't have: %v, %v", got, err)
	}
}
