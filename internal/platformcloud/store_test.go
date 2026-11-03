//go:build integration

package platformcloud

import (
	"context"
	"errors"
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

// scratchStore is a PgStore over a database of its own, with every
// migration applied, dropped when the test ends.
func scratchStore(t *testing.T) (*PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the platform cloud store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "platformcloud_" + time.Now().Format("150405.000000000")
	name = pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name[1:len(name)-1]
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.NewOrchestrator(exec, migrations.All()...).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return NewPgStore(pg), pg
}

func seedHost(t *testing.T, s *PgStore) (*Region, *Host) {
	t.Helper()
	ctx := t.Context()
	r := &Region{ID: "reg_1", Name: "eu-west", DisplayName: "EU West", Status: "active"}
	if err := s.CreateRegion(ctx, r); err != nil {
		t.Fatal(err)
	}
	h := &Host{ID: "host_1", RegionID: r.ID, Endpoint: "https://h1:9443", HostID: "hw-1", Status: "pending"}
	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	return r, h
}

func TestStoreRegionCRUDAndRevisionConflict(t *testing.T) {
	s, _ := scratchStore(t)
	ctx := t.Context()
	r := &Region{ID: "reg_1", Name: "eu-west", DisplayName: "EU West", Status: "active"}
	if err := s.CreateRegion(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Revision != 1 {
		t.Fatalf("revision = %d, want 1", r.Revision)
	}
	if err := s.CreateRegion(ctx, &Region{ID: "reg_2", Name: "eu-west", DisplayName: "dup", Status: "active"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name err = %v, want ErrConflict", err)
	}
	stale := *r
	r.DisplayName = "Europe West"
	if err := s.UpdateRegion(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.Revision != 2 {
		t.Fatalf("revision = %d, want 2", r.Revision)
	}
	stale.DisplayName = "lost"
	if err := s.UpdateRegion(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update err = %v, want ErrConflict", err)
	}
	if err := s.UpdateRegion(ctx, &Region{ID: "nope", Name: "x", DisplayName: "x", Status: "active", Revision: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update err = %v, want ErrNotFound", err)
	}
	got, err := s.ListRegions(ctx)
	if err != nil || len(got) != 1 || got[0].DisplayName != "Europe West" || got[0].Revision != 2 {
		t.Fatalf("ListRegions = %+v, %v", got, err)
	}
}

func TestStoreHostCRUDAndRevisionConflict(t *testing.T) {
	s, _ := scratchStore(t)
	ctx := t.Context()
	_, h := seedHost(t, s)
	if h.Revision != 1 {
		t.Fatalf("revision = %d, want 1", h.Revision)
	}
	if err := s.CreateHost(ctx, &Host{ID: "host_2", RegionID: "reg_1", Endpoint: "e", HostID: "hw-1", Status: "pending"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate host_id err = %v, want ErrConflict", err)
	}
	if err := s.CreateHost(ctx, &Host{ID: "host_3", RegionID: "missing", Endpoint: "e", HostID: "hw-3", Status: "pending"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing region err = %v, want ErrConflict", err)
	}
	checked := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	stale := *h
	h.Status = "ready"
	h.Generation = 3
	h.ClientFingerprint = "fp"
	h.Allotment = Allotment{VCPUs: 16, MemoryMiB: 32768, DiskMiB: 100000, VMs: 20}
	h.Evidence = Evidence{Isolation: true, Platform: true, SMTActive: false, RuntimeIdentity: "rt", Architecture: "amd64",
		ProtocolVersion: "1", DeniedPorts: []int{22, 25}, PerOrgVMs: 5, PerOrgVCPUs: 8, PerOrgMemoryMiB: 4096,
		IngressAddress: "10.0.0.1", CheckedAt: checked}
	if err := s.UpdateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if h.Revision != 2 {
		t.Fatalf("revision = %d, want 2", h.Revision)
	}
	stale.Status = "retired"
	if err := s.UpdateHost(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update err = %v, want ErrConflict", err)
	}
	got, err := s.GetHost(ctx, "host_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ready" || got.Generation != 3 || got.Allotment.VMs != 20 || !got.Evidence.Isolation ||
		len(got.Evidence.DeniedPorts) != 2 || !got.Evidence.CheckedAt.Equal(checked) || got.Evidence.IngressAddress != "10.0.0.1" {
		t.Fatalf("GetHost = %+v", got)
	}
	if _, err = s.GetHost(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetHost missing err = %v", err)
	}
	all, err := s.ListHosts(ctx, "")
	if err != nil || len(all) != 1 {
		t.Fatalf("ListHosts = %v, %v", all, err)
	}
	if in, _ := s.ListHosts(ctx, "reg_other"); len(in) != 0 {
		t.Fatalf("ListHosts other region = %v", in)
	}
}

func addPlacement(t *testing.T, pg *pgdriver.PgDB, id, org, kind, state string, host *string, vcpus, mem, disk int) {
	t.Helper()
	_, err := pg.Exec(t.Context(), `INSERT INTO loomplane_platform_placements(id,org_id,project_id,environment_id,resource,kind,region_id,host_id,vcpus,memory_mib,disk_mib,state,created_at,updated_at)
VALUES($1,$2,'p','e',$1,$3,'reg_1',$4,$5,$6,$7,$8,now(),now())`, id, org, kind, host, vcpus, mem, disk, state)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreTenantUseAndHostUse(t *testing.T) {
	s, pg := scratchStore(t)
	ctx := t.Context()
	seedHost(t, s)
	host := "host_1"
	addPlacement(t, pg, "a", "org1", "service", "running", &host, 2, 1024, 10)
	addPlacement(t, pg, "b", "org1", "service", "suspended", &host, 1, 512, 5)
	addPlacement(t, pg, "c", "org1", "service", "released", &host, 8, 8192, 80)
	addPlacement(t, pg, "d", "org1", "build", "running", &host, 4, 4096, 20)
	addPlacement(t, pg, "e", "org1", "build", "stopped", &host, 4, 4096, 20)
	addPlacement(t, pg, "f", "org1", "service", "reserved", nil, 3, 256, 3)
	addPlacement(t, pg, "g", "org2", "service", "running", &host, 1, 100, 1)

	use, err := s.TenantUse(ctx, "org1")
	if err != nil {
		t.Fatal(err)
	}
	want := Use{VCPUs: 6, MemoryMiB: 1792, VMs: 3, ConcurrentBuilds: 1}
	if use != want {
		t.Fatalf("TenantUse = %+v, want %+v", use, want)
	}
	if use, err = s.TenantUse(ctx, "nobody"); err != nil || use != (Use{}) {
		t.Fatalf("empty TenantUse = %+v, %v", use, err)
	}

	hu, err := s.HostUse(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	wantHost := Allotment{VCPUs: 12, MemoryMiB: 1024 + 512 + 4096 + 4096 + 100, DiskMiB: 10 + 5 + 20 + 20 + 1, VMs: 5}
	if hu != wantHost {
		t.Fatalf("HostUse = %+v, want %+v", hu, wantHost)
	}

	ps, err := s.ListPlacements(ctx, "org1")
	if err != nil || len(ps) != 5 {
		t.Fatalf("ListPlacements = %d, %v", len(ps), err)
	}
	on, err := s.PlacementsOnHost(ctx, host)
	if err != nil || len(on) != 5 {
		t.Fatalf("PlacementsOnHost = %d, %v", len(on), err)
	}
	for _, p := range ps {
		if p.ID == "f" && p.HostID != nil {
			t.Fatalf("placement f host = %v, want nil", *p.HostID)
		}
		if p.ID == "a" && (p.HostID == nil || *p.HostID != host) {
			t.Fatalf("placement a host = %v", p.HostID)
		}
	}
}

func TestStoreTenantUpsertAndLock(t *testing.T) {
	s, _ := scratchStore(t)
	ctx := t.Context()
	if _, err := s.GetTenant(ctx, "org1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTenant missing err = %v", err)
	}
	lim := Limits{VCPUs: 4, MemoryMiB: 8192, VMs: 10, ConcurrentBuilds: 1}
	tn := &Tenant{OrgID: "org1", Status: "enabled", Limits: &lim}
	if err := s.UpsertTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	if tn.Revision != 1 {
		t.Fatalf("revision = %d, want 1", tn.Revision)
	}
	stale := *tn
	now := time.Now().UTC().Truncate(time.Microsecond)
	tn.SuspendReasons = []SuspendReason{ReasonPastDue, ReasonSpendingLimit}
	tn.SuspendedAt = &now
	tn.Limits = nil
	if err := s.UpsertTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	if tn.Revision != 2 {
		t.Fatalf("revision = %d, want 2", tn.Revision)
	}
	if err := s.UpsertTenant(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale upsert err = %v, want ErrConflict", err)
	}
	got, err := s.GetTenant(ctx, "org1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits != nil || len(got.SuspendReasons) != 2 || got.SuspendReasons[0] != ReasonPastDue || got.SuspendedAt == nil || !got.SuspendedAt.Equal(now) {
		t.Fatalf("GetTenant = %+v", got)
	}
	if err = s.UpsertTenant(ctx, &Tenant{OrgID: "org2", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListTenants(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListTenants = %v, %v", all, err)
	}

	called := false
	if err = s.WithTenantLock(ctx, "org1", func(driverTx) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("lock enabled: %v called=%v", err, called)
	}
	called = false
	if err = s.WithTenantLock(ctx, "org2", func(driverTx) error { called = true; return nil }); !errors.Is(err, ErrNotEnabled) || called {
		t.Fatalf("lock disabled err = %v called=%v", err, called)
	}
	if err = s.WithTenantLock(ctx, "ghost", func(driverTx) error { return nil }); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("lock missing err = %v", err)
	}
	boom := errors.New("boom")
	if err = s.WithTenantLock(ctx, "org1", func(driverTx) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("lock fn err = %v", err)
	}
}

func TestStoreWithPlatformWrite(t *testing.T) {
	s, pg := scratchStore(t)
	ctx := t.Context()
	const ins = `INSERT INTO loomplane_cloud_profiles(id,org_id,name,platform_owned) VALUES('p1','org1','Platform',true)`
	_, err := pg.Exec(ctx, ins)
	if err == nil {
		t.Fatal("projection write without the setting succeeded")
	}
	var wrote bool
	err = s.WithPlatformWrite(ctx, func(tx driverTx) error {
		var on string
		if err := tx.QueryRow(ctx, `SELECT current_setting('`+platformWriteSetting+`', true)`).Scan(&on); err != nil {
			return err
		}
		wrote = on == "on"
		return nil
	})
	if err != nil || !wrote {
		t.Fatalf("WithPlatformWrite: %v on=%v", err, wrote)
	}
	// The setting is local to the transaction.
	var after string
	if err = pg.QueryRow(ctx, `SELECT coalesce(current_setting('`+platformWriteSetting+`', true),'')`).Scan(&after); err != nil || after == "on" {
		t.Fatalf("setting leaked: %q %v", after, err)
	}
}
