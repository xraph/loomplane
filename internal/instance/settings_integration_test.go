//go:build integration

package instance

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
)

// scratchSettings is a Settings over a database of its own, dropped when
// the test ends, with the instance settings migration applied.
func scratchSettings(t *testing.T) *Settings {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the instance settings test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("instance_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	applied := false
	for _, m := range migrations.Group().Migrations() {
		if m.Name != "create_instance_settings" {
			continue
		}
		for range 2 { // a rerun must be harmless
			if err = m.Up(ctx, exec); err != nil {
				t.Fatal(err)
			}
		}
		applied = true
	}
	if !applied {
		t.Fatal("no create_instance_settings migration")
	}
	return New(pg)
}

func TestSettings_waitlistRoundTrip(t *testing.T) {
	s := scratchSettings(t)
	ctx := t.Context()

	on, stored, err := s.Waitlist(ctx)
	if err != nil || on || stored {
		t.Fatalf("fresh database: on %v stored %v err %v", on, stored, err)
	}
	for _, want := range []bool{true, false} {
		if err = s.SetWaitlist(ctx, want); err != nil {
			t.Fatal(err)
		}
		on, stored, err = s.Waitlist(ctx)
		if err != nil || on != want || !stored {
			t.Fatalf("after SetWaitlist(%v): on %v stored %v err %v", want, on, stored, err)
		}
	}
}

func TestSettings_setupRecord(t *testing.T) {
	s := scratchSettings(t)
	ctx := t.Context()

	if done, err := s.SetupCompleted(ctx); err != nil || done {
		t.Fatalf("fresh database: done %v err %v", done, err)
	}
	// The backfill leaves the waitlist alone.
	if err := s.MarkSetupCompleted(ctx); err != nil {
		t.Fatal(err)
	}
	if _, stored, err := s.Waitlist(ctx); err != nil || stored {
		t.Fatalf("after MarkSetupCompleted: waitlist stored %v err %v", stored, err)
	}
	// Setup's own write sets both, and rewriting either is harmless.
	if err := s.CompleteSetup(ctx, true); err != nil {
		t.Fatal(err)
	}
	done, err := s.SetupCompleted(ctx)
	on, stored, werr := s.Waitlist(ctx)
	if err != nil || werr != nil || !done || !on || !stored {
		t.Fatalf("after CompleteSetup(true): done %v waitlist %v/%v errs %v %v", done, on, stored, err, werr)
	}
}

func TestSettings_cloudSignInOffer(t *testing.T) {
	s := scratchSettings(t)
	ctx := t.Context()

	// An instance set up before the offer existed is never offered it.
	if err := s.MarkSetupCompleted(ctx); err != nil {
		t.Fatal(err)
	}
	if on, err := s.CloudSignInOffer(ctx); err != nil || on {
		t.Fatalf("after MarkSetupCompleted: offer %v err %v, want off", on, err)
	}
	if err := s.CompleteSetup(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, err := s.CloudSignInOffer(ctx); err != nil || !on {
		t.Fatalf("after CompleteSetup: offer %v err %v, want on", on, err)
	}
	for range 2 { // ending it again is harmless
		if err := s.EndCloudSignInOffer(ctx); err != nil {
			t.Fatal(err)
		}
		if on, err := s.CloudSignInOffer(ctx); err != nil || on {
			t.Fatalf("after EndCloudSignInOffer: offer %v err %v, want off", on, err)
		}
	}
}

func TestSettings_platformCloudRoundTrip(t *testing.T) {
	s := scratchSettings(t)
	ctx := t.Context()

	got, err := s.PlatformCloud(ctx)
	if err != nil || got.Attach != "manual" || got.DefaultLimits != (PlatformLimits{VCPUs: 4, MemoryMiB: 8192, VMs: 10, ConcurrentBuilds: 1}) {
		t.Fatalf("fresh database: %+v err %v", got, err)
	}
	want := PlatformCloudSettings{
		Attach:        "all_workspaces",
		DefaultLimits: PlatformLimits{VCPUs: 8, MemoryMiB: 16384, VMs: 20, ConcurrentBuilds: 3},
		Apex:          "apps.example.com",
		Registry:      RegistrySettings{Endpoint: "registry.example.com", Repository: "loomplane", AuthEndpoint: "https://auth.example.com/token"},
	}
	if err = s.SetPlatformCloud(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = s.PlatformCloud(ctx)
	if err != nil || got != want {
		t.Fatalf("round trip: %+v err %v, want %+v", got, err, want)
	}
}
