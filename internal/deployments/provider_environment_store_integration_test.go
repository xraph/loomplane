//go:build integration

package deployments

import (
	"testing"
	"time"
)

// providerEnvFixture adds to the admission fixture's project "proj" (with
// environment "env", production) a preview, a staging and a later production.
func providerEnvFixture(t *testing.T) *PgStore {
	t.Helper()
	dsn, template := admissionTemplate(t)
	pg := admissionFixture(t, dsn, template).db
	for _, stmt := range []string{
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env_prev','org','proj','preview','preview',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env_stg','org','proj','staging','staging',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('env_prod2','org','proj','prod2','production',now()+interval '1 minute',now())`,
	} {
		if _, err := pg.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return NewPgStore(pg)
}

func providerEnv(env, scope, mode string) *ProviderEnvironment {
	return &ProviderEnvironment{OrgID: "org", ProjectID: "proj", EnvironmentID: env, Provider: "vercel", ConnectionID: "conn", Scope: scope, Mode: mode, ProjectRef: "prj_1", EnvironmentRef: "ev_1", Reason: "r"}
}

func TestProviderEnvironmentRoundTrip(t *testing.T) {
	store := providerEnvFixture(t)
	want := providerEnv("env", "web", ModeNative)
	if err := store.SaveProviderEnvironment(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.ProviderEnvironment(t.Context(), "org", "env", "vercel", "conn", "web")
	if err != nil || got == nil {
		t.Fatal(got, err)
	}
	if got.OrgID != want.OrgID || got.ProjectID != want.ProjectID || got.EnvironmentID != want.EnvironmentID || got.Provider != want.Provider ||
		got.ConnectionID != want.ConnectionID || got.Scope != want.Scope || got.Mode != ModeNative || got.Permanent || got.ProjectRef != "prj_1" ||
		got.EnvironmentRef != "ev_1" || got.Reason != "r" || got.NotifiedAt != nil || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("round trip changed the record: %+v", got)
	}
	again := providerEnv("env", "web", ModeFallback)
	again.Permanent, again.ProjectRef, again.EnvironmentRef, again.Reason = true, "", "", "plan"
	if err = store.SaveProviderEnvironment(t.Context(), again); err != nil {
		t.Fatal(err)
	}
	got, err = store.ProviderEnvironment(t.Context(), "org", "env", "vercel", "conn", "web")
	if err != nil || got == nil || got.Mode != ModeFallback || !got.Permanent || got.ProjectRef != "" || got.Reason != "plan" {
		t.Fatalf("second save did not update in place: %+v %v", got, err)
	}
	if all, err := store.ProviderEnvironments(t.Context(), "org", "proj"); err != nil || len(all) != 1 {
		t.Fatalf("expected one row: %v %v", all, err)
	}
	none, err := store.ProviderEnvironment(t.Context(), "org", "env", "vercel", "conn", "other")
	if none != nil || err != nil {
		t.Fatalf("unknown key: %v %v", none, err)
	}
}

func TestProviderEnvironmentClaimNoticeOnce(t *testing.T) {
	store := providerEnvFixture(t)
	pe := providerEnv("env", "", ModeFallback)
	if err := store.SaveProviderEnvironment(t.Context(), pe); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		got, err := store.ClaimFallbackNotice(t.Context(), pe, time.Now())
		if err != nil || got != want {
			t.Fatalf("claim %d: got %v, %v", i, got, err)
		}
	}
	// A later save keeps the notice.
	if err := store.SaveProviderEnvironment(t.Context(), providerEnv("env", "", ModeFallback)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ClaimFallbackNotice(t.Context(), pe, time.Now()); err != nil || got {
		t.Fatalf("notice claimed again after a save: %v %v", got, err)
	}
}

func TestResetFallbacksSkipsPermanent(t *testing.T) {
	store := providerEnvFixture(t)
	retry, perm := providerEnv("env_stg", "a", ModeFallback), providerEnv("env_stg", "b", ModeFallback)
	perm.Permanent = true
	for _, pe := range []*ProviderEnvironment{retry, perm} {
		if err := store.SaveProviderEnvironment(t.Context(), pe); err != nil {
			t.Fatal(err)
		}
	}
	reset, permanent, err := store.ResetFallbacks(t.Context(), "org", "env_stg")
	if err != nil || reset != 1 || permanent != 1 {
		t.Fatalf("got %d, %d, %v", reset, permanent, err)
	}
	left, err := store.ProviderEnvironments(t.Context(), "org", "proj")
	if err != nil || len(left) != 1 || left[0].Scope != "b" {
		t.Fatalf("left: %v %v", left, err)
	}
}

// TestSharingProjectListsNativeOnly also lists a native row deployed
// through another connection: two connections to one Vercel team share the
// project, and its id is the same through either.
func TestSharingProjectListsNativeOnly(t *testing.T) {
	store := providerEnvFixture(t)
	otherConnection := providerEnv("env_prod2", "web", ModeNative)
	otherConnection.ConnectionID = "conn_b"
	for _, pe := range []*ProviderEnvironment{providerEnv("env", "web", ModeNative), providerEnv("env_stg", "web", ModeNative), providerEnv("env_prev", "web", ModeFallback), otherConnection} {
		if err := store.SaveProviderEnvironment(t.Context(), pe); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.SharingProject(t.Context(), "org", "vercel", "prj_1")
	if err != nil || len(got) != 3 {
		t.Fatalf("got %v %v", got, err)
	}
	for _, pe := range got {
		if pe.Mode != ModeNative {
			t.Fatalf("a fallback row listed: %+v", pe)
		}
	}
}

func TestFellBack(t *testing.T) {
	store := providerEnvFixture(t)
	for _, pe := range []*ProviderEnvironment{providerEnv("env", "web", ModeNative), providerEnv("env_stg", "web", ModeFallback)} {
		if err := store.SaveProviderEnvironment(t.Context(), pe); err != nil {
			t.Fatal(err)
		}
	}
	for env, want := range map[string]bool{"env": false, "env_stg": true, "env_prev": false} {
		got, err := store.FellBack(t.Context(), "org", env, "vercel", "conn", "web")
		if err != nil || got != want {
			t.Errorf("%s: got %v, %v", env, got, err)
		}
	}
}

func TestEnvironmentRole(t *testing.T) {
	store := providerEnvFixture(t)
	for _, c := range []struct {
		id, kind, name string
		first          bool
	}{
		{"env", "production", "production", true},
		{"env_prod2", "production", "prod2", false},
		{"env_stg", "staging", "staging", true},
		{"env_prev", "preview", "preview", true},
	} {
		kind, name, first, err := store.EnvironmentRole(t.Context(), "org", "proj", c.id)
		if err != nil || kind != c.kind || name != c.name || first != c.first {
			t.Errorf("%s: got %q %q %v %v", c.id, kind, name, first, err)
		}
	}
}

func TestProviderEnvironmentCascades(t *testing.T) {
	store := providerEnvFixture(t)
	if err := store.SaveProviderEnvironment(t.Context(), providerEnv("env_stg", "web", ModeNative)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(t.Context(), `DELETE FROM loomplane_environments WHERE id='env_stg'`); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ProviderEnvironment(t.Context(), "org", "env_stg", "vercel", "conn", "web"); got != nil || err != nil {
		t.Fatalf("record survived its environment: %v %v", got, err)
	}
}
