//go:build integration

package platformcloud

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/instance"
)

// projection is what a workspace sees of the platform Cloud.
type projection struct {
	profile *cloudprofiles.Profile
	targets []*cloudprofiles.Target
}

func readProjection(t *testing.T, pg *pgdriver.PgDB, orgID string) projection {
	t.Helper()
	ctx := t.Context()
	clouds := cloudprofiles.NewPgStore(pg)
	profiles, err := clouds.ListProfiles(ctx, orgID, "")
	if err != nil {
		t.Fatal(err)
	}
	var out projection
	for _, p := range profiles {
		if !p.PlatformOwned {
			continue
		}
		if out.profile != nil {
			t.Fatalf("org %s has two platform-owned Clouds", orgID)
		}
		out.profile = p
	}
	if out.profile == nil {
		return out
	}
	if out.targets, err = clouds.ListTargets(ctx, orgID, "", out.profile.ID); err != nil {
		t.Fatal(err)
	}
	return out
}

func newTenantService(t *testing.T, orgs ...string) (*Service, *PgStore, *pgdriver.PgDB) {
	t.Helper()
	s, pg := scratchStore(t)
	lister := func(context.Context) ([]string, error) { return orgs, nil }
	return NewService(s, instance.New(pg), lister), s, pg
}

func seedRegions(t *testing.T, s *PgStore) {
	t.Helper()
	for _, r := range []*Region{
		{ID: "reg_eu", Name: "eu-west", DisplayName: "EU West", Status: "active"},
		{ID: "reg_us", Name: "us-east", DisplayName: "US East", Status: "active"},
		{ID: "reg_ap", Name: "ap-south", DisplayName: "AP South", Status: "closed"},
	} {
		if err := s.CreateRegion(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnableTenantCreatesProjectionWithoutGrants(t *testing.T) {
	svc, s, pg := newTenantService(t)
	ctx := t.Context()
	seedRegions(t, s)

	tenant, err := svc.EnableTenant(ctx, "org_a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.Status != "enabled" || tenant.Limits != nil {
		t.Fatalf("tenant = %+v", tenant)
	}
	got := readProjection(t, pg, "org_a")
	if got.profile == nil {
		t.Fatal("no platform-owned Cloud")
	}
	if got.profile.Name != ProjectionName || got.profile.ProjectID != "" || got.profile.DefaultPlacement != nil {
		t.Fatalf("profile = %+v", got.profile)
	}
	if len(got.targets) != 2 {
		t.Fatalf("targets = %d, want one per active region", len(got.targets))
	}
	want := map[string]string{"eu-west": "EU West", "us-east": "US East"}
	for _, target := range got.targets {
		if target.Runtime != RuntimePlatform || target.Provider != "platform" || target.ProjectID != "" || target.ConnectionID != "" || target.PoolID != "" ||
			target.Name != target.Region || want[target.Region] != target.DisplayName {
			t.Fatalf("target = %+v", target)
		}
	}
	var grants int
	if err = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_project_grants WHERE org_id=$1 AND cloud_id=$2`, "org_a", got.profile.ID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatalf("grants = %d, want 0", grants)
	}

	again, err := svc.EnableTenant(ctx, "org_a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != tenant.Revision {
		t.Fatalf("tenant revision %d -> %d on a repeat enable", tenant.Revision, again.Revision)
	}
	after := readProjection(t, pg, "org_a")
	if after.profile.ID != got.profile.ID || after.profile.Revision != got.profile.Revision || len(after.targets) != len(got.targets) {
		t.Fatalf("repeat enable changed the projection: %+v -> %+v", got.profile, after.profile)
	}
	for i := range after.targets {
		if after.targets[i].ID != got.targets[i].ID || after.targets[i].Revision != got.targets[i].Revision {
			t.Fatalf("repeat enable changed target %+v -> %+v", got.targets[i], after.targets[i])
		}
	}

	// The org's own config tools can't write the projection.
	clouds := cloudprofiles.NewPgStore(pg)
	renamed := *after.profile
	renamed.Name, renamed.Revision = "Mine", renamed.Revision+1
	if err = clouds.UpdateProfile(ctx, &renamed, after.profile.Revision); err == nil {
		t.Fatal("store rename of the projection succeeded")
	}
}

func TestEnableTenantKeepsLimitsAndClearsOwnerSuspension(t *testing.T) {
	svc, s, _ := newTenantService(t)
	ctx := t.Context()
	seedRegions(t, s)
	limits := &Limits{VCPUs: 2, MemoryMiB: 1024, VMs: 3, ConcurrentBuilds: 1}
	if _, err := svc.EnableTenant(ctx, "org_a", limits); err != nil {
		t.Fatal(err)
	}
	if err := svc.DisableTenant(ctx, "org_a"); err != nil {
		t.Fatal(err)
	}
	disabled, err := s.GetTenant(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != "disabled" || !slices.Contains(disabled.SuspendReasons, ReasonPlatformOwner) {
		t.Fatalf("disabled tenant = %+v", disabled)
	}
	if err = s.WithTenantLock(ctx, "org_a", func(driverTx) error { return nil }); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("new work on a disabled tenant: %v", err)
	}
	// Disabling twice is harmless and adds the reason once.
	if err = svc.DisableTenant(ctx, "org_a"); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetTenant(ctx, "org_a"); len(again.SuspendReasons) != 1 {
		t.Fatalf("reasons = %v", again.SuspendReasons)
	}
	if err = svc.DisableTenant(ctx, "org_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disable unknown org: %v", err)
	}

	enabled, err := svc.EnableTenant(ctx, "org_a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Status != "enabled" || len(enabled.SuspendReasons) != 0 || enabled.Limits == nil || *enabled.Limits != *limits {
		t.Fatalf("re-enabled tenant = %+v", enabled)
	}
}

func TestSyncProjectionsAddsNewRegion(t *testing.T) {
	svc, s, pg := newTenantService(t)
	ctx := t.Context()
	seedRegions(t, s)
	for _, org := range []string{"org_a", "org_b"} {
		if _, err := svc.EnableTenant(ctx, org, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.DisableTenant(ctx, "org_b"); err != nil {
		t.Fatal(err)
	}
	before := readProjection(t, pg, "org_a")
	if err := s.CreateRegion(ctx, &Region{ID: "reg_sa", Name: "sa-east", DisplayName: "SA East", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncProjections(ctx); err != nil {
		t.Fatal(err)
	}
	after := readProjection(t, pg, "org_a")
	if len(after.targets) != 3 {
		t.Fatalf("targets = %d, want 3", len(after.targets))
	}
	if after.profile.Revision != before.profile.Revision {
		t.Fatal("adding a target rewrote the profile")
	}
	var added *cloudprofiles.Target
	for _, target := range after.targets {
		if target.Region == "sa-east" {
			added = target
		}
	}
	if added == nil || added.DisplayName != "SA East" || added.Runtime != RuntimePlatform {
		t.Fatalf("new region target = %+v", added)
	}
	// A disabled tenant gets no new regions.
	if got := readProjection(t, pg, "org_b"); len(got.targets) != 2 {
		t.Fatalf("disabled tenant targets = %d, want 2", len(got.targets))
	}
	// Running it again adds nothing.
	if err := svc.SyncProjections(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readProjection(t, pg, "org_a"); len(got.targets) != 3 {
		t.Fatalf("second sync targets = %d", len(got.targets))
	}
}

func TestSyncProjectionsAllWorkspaces(t *testing.T) {
	svc, s, pg := newTenantService(t, "org_a", "org_b", "org_c")
	ctx := t.Context()
	seedRegions(t, s)
	if _, err := svc.EnableTenant(ctx, "org_c", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.DisableTenant(ctx, "org_c"); err != nil {
		t.Fatal(err)
	}

	// Manual attach enables nobody.
	if err := svc.SyncProjections(ctx); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{"org_a", "org_b"} {
		if _, err := s.GetTenant(ctx, org); !errors.Is(err, ErrNotFound) {
			t.Fatalf("manual attach created tenant %s: %v", org, err)
		}
		if got := readProjection(t, pg, org); got.profile != nil {
			t.Fatalf("manual attach projected into %s", org)
		}
	}

	settings := instance.New(pg)
	cfg, err := settings.PlatformCloud(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Attach = "all_workspaces"
	if err = settings.SetPlatformCloud(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err = svc.SyncProjections(ctx); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{"org_a", "org_b"} {
		tenant, err := s.GetTenant(ctx, org)
		if err != nil || tenant.Status != "enabled" {
			t.Fatalf("tenant %s = %+v, %v", org, tenant, err)
		}
		if got := readProjection(t, pg, org); got.profile == nil || len(got.targets) != 2 {
			t.Fatalf("projection for %s = %+v", org, got)
		}
	}
	// An org the owner disabled stays disabled.
	if tenant, _ := s.GetTenant(ctx, "org_c"); tenant.Status != "disabled" {
		t.Fatalf("all_workspaces re-enabled a disabled org: %+v", tenant)
	}
}
