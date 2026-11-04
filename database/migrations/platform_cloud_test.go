//go:build integration

package migrations

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/migrate"
)

// platformCloudScratch migrates every earlier migration, then returns the
// connection and executor with platform_cloud still to run.
func platformCloudScratch(t *testing.T) (*pgx.Conn, migrate.Executor) {
	t.Helper()
	conn, ex := scratch(t)
	last := migration(t, "platform_cloud").Version
	earlier := migrate.NewGroup(GroupName)
	for _, step := range Group().Migrations() {
		if step.Version < last {
			earlier.MustRegister(step)
		}
	}
	if _, err := migrate.NewOrchestrator(ex, earlier).Migrate(t.Context()); err != nil {
		t.Fatalf("up earlier: %v", err)
	}
	return conn, ex
}

func requireSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != want {
		t.Fatalf("error %v, want SQLSTATE %s", err, want)
	}
}

func platformFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, status, created_by, created_at, updated_at)
			VALUES ('proj_p', 'org_p', 'P', 'p', 'ready', 'usr', now(), now())`,
		`INSERT INTO loomplane_cloud_profiles (id, org_id, project_id, name, revision, created_at, updated_at)
			VALUES ('cloud_user', 'org_p', 'proj_p', 'User', 1, now(), now())`,
	)
}

func TestPlatformCloudMigrationAllowsPlatformTargetsOnlyWithFlag(t *testing.T) {
	conn, ex := platformCloudScratch(t)
	up(t, ex, "platform_cloud")
	// A second run changes nothing.
	up(t, ex, "platform_cloud")
	platformFixture(t, conn)
	ctx := context.Background()

	const profile = `INSERT INTO loomplane_cloud_profiles (id, org_id, project_id, name, revision, platform_owned, created_at, updated_at)
		VALUES ('cloud_plat', 'org_p', '', 'Platform', 1, true, now(), now())`
	_, err := conn.Exec(ctx, profile)
	requireSQLState(t, err, "42501")

	// Marking a user profile platform-owned is refused too.
	_, err = conn.Exec(ctx, `UPDATE loomplane_cloud_profiles SET platform_owned=true, revision=2 WHERE id='cloud_user'`)
	requireSQLState(t, err, "42501")

	// An ordinary profile and target are untouched by the guard.
	exec(t, conn,
		`UPDATE loomplane_cloud_profiles SET name='Renamed', revision=2 WHERE id='cloud_user'`,
	)

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`SET LOCAL loomplane.platform_write='on'`,
		profile,
		`INSERT INTO loomplane_cloud_targets (id, org_id, project_id, cloud_id, name, display_name, provider, connection_id, runtime, region, revision, created_at, updated_at)
			VALUES ('tgt_plat', 'org_p', '', 'cloud_plat', 'platform', 'Platform', 'platform', NULL, 'platform', 'eu', 1, now(), now())`,
	} {
		if _, err = tx.Exec(ctx, s); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// A managed target cannot be added to a platform profile, and a user
	// target cannot be moved onto one, without the flag. The fixture target
	// skips the connection's foreign key, which is not under test.
	exec(t, conn, `SET session_replication_role = replica`,
		`INSERT INTO loomplane_cloud_targets (id, org_id, project_id, cloud_id, name, display_name, provider, connection_id, runtime, region, revision, created_at, updated_at)
			VALUES ('tgt_user', 'org_p', 'proj_p', 'cloud_user', 'mine', 'Mine', 'aws', 'conn_x', 'managed', 'eu', 1, now(), now())`,
		`SET session_replication_role = origin`)
	_, err = conn.Exec(ctx, `INSERT INTO loomplane_cloud_targets (id, org_id, project_id, cloud_id, name, display_name, provider, connection_id, runtime, region, revision, created_at, updated_at)
		VALUES ('tgt_hang', 'org_p', 'proj_p', 'cloud_plat', 'hang', 'Hang', 'aws', 'conn_x', 'managed', 'eu', 1, now(), now())`)
	requireSQLState(t, err, "42501")
	_, err = conn.Exec(ctx, `UPDATE loomplane_cloud_targets SET cloud_id='cloud_plat', revision=2 WHERE id='tgt_user'`)
	requireSQLState(t, err, "42501")

	// With the flag, bumping a platform target's revision by one succeeds.
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(ctx, `SET LOCAL loomplane.platform_write='on'`)
	if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_targets SET display_name='Platform 2', revision=2 WHERE id='tgt_plat'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// A platform target with a connection violates the CHECK.
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `SET LOCAL loomplane.platform_write='on'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_targets (id, org_id, project_id, cloud_id, name, display_name, provider, connection_id, runtime, region, revision, created_at, updated_at)
		VALUES ('tgt_bad', 'org_p', '', 'cloud_plat', 'bad', 'Bad', 'platform', 'conn_x', 'platform', 'eu', 1, now(), now())`)
	requireSQLState(t, err, "23514")
	_ = tx.Rollback(ctx)

	// Writes to the projection without the flag fail.
	_, err = conn.Exec(ctx, `UPDATE loomplane_cloud_profiles SET name='Hacked', revision=2 WHERE id='cloud_plat'`)
	requireSQLState(t, err, "42501")
	_, err = conn.Exec(ctx, `UPDATE loomplane_cloud_targets SET display_name='Hacked', revision=2 WHERE id='tgt_plat'`)
	requireSQLState(t, err, "42501")
	_, err = conn.Exec(ctx, `DELETE FROM loomplane_cloud_targets WHERE id='tgt_plat'`)
	requireSQLState(t, err, "42501")
	_, err = conn.Exec(ctx, `DELETE FROM loomplane_cloud_profiles WHERE id='cloud_plat'`)
	requireSQLState(t, err, "42501")
	_, err = conn.Exec(ctx, `INSERT INTO loomplane_cloud_targets (id, org_id, project_id, cloud_id, name, display_name, provider, connection_id, runtime, region, revision, created_at, updated_at)
		VALUES ('tgt_two', 'org_p', '', 'cloud_plat', 'two', 'Two', 'platform', NULL, 'platform', 'eu', 1, now(), now())`)
	requireSQLState(t, err, "42501")

	// With the flag the update goes through, and the revision trigger still runs.
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(ctx, `SET LOCAL loomplane.platform_write='on'`)
	_, err = tx.Exec(ctx, `UPDATE loomplane_cloud_profiles SET name='Platform 2', revision=5 WHERE id='cloud_plat'`)
	requireSQLState(t, err, "23514")
	_ = tx.Rollback(ctx)

}

func TestPlatformCloudMigrationDown(t *testing.T) {
	conn, ex := platformCloudScratch(t)
	before := strs(t, conn, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='loomplane_target_runtime'`)
	up(t, ex, "platform_cloud")
	during := strs(t, conn, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='loomplane_target_runtime'`)
	if len(during) != 1 || !strings.Contains(during[0], "platform") {
		t.Fatalf("constraint after up %q", during)
	}
	down(t, ex, "platform_cloud")

	for _, table := range []string{"loomplane_platform_regions", "loomplane_platform_hosts", "loomplane_platform_tenants", "loomplane_platform_placements", "loomplane_platform_egress"} {
		if got := strs(t, conn, `SELECT COALESCE(to_regclass('`+table+`')::text, '')`); len(got) != 1 || got[0] != "" {
			t.Fatalf("table %s survived down: %q", table, got)
		}
	}
	if got := strs(t, conn, `SELECT column_name FROM information_schema.columns WHERE table_name='loomplane_cloud_profiles' AND column_name='platform_owned'`); len(got) != 0 {
		t.Fatalf("column survived down: %q", got)
	}
	if got := strs(t, conn, `SELECT tgname FROM pg_trigger WHERE tgname LIKE 'loomplane_cloud_%platform_guard%'`); len(got) != 0 {
		t.Fatalf("triggers survived down: %q", got)
	}
	if got := strs(t, conn, `SELECT proname FROM pg_proc WHERE proname='loomplane_platform_projection_guard'`); len(got) != 0 {
		t.Fatalf("function survived down: %q", got)
	}
	after := strs(t, conn, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='loomplane_target_runtime'`)
	if !slices.Equal(before, after) {
		t.Fatalf("constraint %q, want %q", after, before)
	}
	// And up again works.
	up(t, ex, "platform_cloud")
}

func TestPlatformPlacementOnePerResource(t *testing.T) {
	conn, ex := platformCloudScratch(t)
	up(t, ex, "platform_cloud")
	ctx := context.Background()
	exec(t, conn, `INSERT INTO loomplane_platform_regions (id, name, display_name, status, revision, created_at, updated_at)
		VALUES ('reg_1', 'eu', 'Europe', 'active', 1, now(), now())`)
	insert := func(id, state string) error {
		_, err := conn.Exec(ctx, `INSERT INTO loomplane_platform_placements
			(id, org_id, project_id, environment_id, resource, kind, region_id, vcpus, memory_mib, disk_mib, state, restore_running, waiting_capacity, instance_id, generation, created_at, updated_at)
			VALUES ($1, 'org', 'proj', 'env', 'web', 'service', 'reg_1', 1, 512, 1024, $2, false, false, '', 1, now(), now())`, id, state)
		return err
	}
	if err := insert("plc_1", "running"); err != nil {
		t.Fatal(err)
	}
	requireSQLState(t, insert("plc_2", "reserved"), "23505")
	exec(t, conn, `UPDATE loomplane_platform_placements SET state='released' WHERE id='plc_1'`)
	if err := insert("plc_2", "reserved"); err != nil {
		t.Fatal(err)
	}
	if err := insert("plc_3", "released"); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformCloudMeterTablesUpAndDown(t *testing.T) {
	conn, ex := platformCloudScratch(t)
	up(t, ex, "platform_cloud", "platform_cloud_routes", "platform_cloud_meter")
	up(t, ex, "platform_cloud_meter") // idempotent
	exec(t, conn,
		`INSERT INTO loomplane_platform_meter_compute (placement_id,generation,hour,org_id,vcpus,memory_mib,minutes,last_minute) VALUES ('p',1,now(),'o',1,512,3,now())`,
		`INSERT INTO loomplane_platform_meter_egress (placement_id,hour,org_id,bytes) VALUES ('p',now(),'o',10)`)
	if got := strs(t, conn, `SELECT column_name FROM information_schema.columns WHERE table_name='loomplane_platform_placements' AND column_name='running_since'`); len(got) != 1 {
		t.Fatalf("running_since missing: %q", got)
	}
	down(t, ex, "platform_cloud_meter")
	if got := strs(t, conn, `SELECT column_name FROM information_schema.columns WHERE table_name='loomplane_platform_placements' AND column_name='running_since'`); len(got) != 0 {
		t.Fatalf("running_since survived down: %q", got)
	}
	for _, table := range []string{"loomplane_platform_meter_compute", "loomplane_platform_meter_egress"} {
		if got := strs(t, conn, `SELECT COALESCE(to_regclass('`+table+`')::text, '')`); len(got) != 1 || got[0] != "" {
			t.Fatalf("table %s survived down: %q", table, got)
		}
	}
	up(t, ex, "platform_cloud_meter")
}

// A resource has one current live placement and at most one live
// replacement, and the continuity migration goes down and up again.
func TestPlatformPlacementReplacementAndEgressCounters(t *testing.T) {
	conn, ex := platformCloudScratch(t)
	up(t, ex, "platform_cloud", "platform_cloud_routes", "platform_cloud_meter", "platform_cloud_cleanup", "platform_cloud_continuity")
	ctx := context.Background()
	exec(t, conn, `INSERT INTO loomplane_platform_regions (id, name, display_name, status, revision, created_at, updated_at)
		VALUES ('reg_1', 'eu', 'Europe', 'active', 1, now(), now())`)
	insert := func(id, state, replaces string) error {
		_, err := conn.Exec(ctx, `INSERT INTO loomplane_platform_placements
			(id, org_id, project_id, environment_id, resource, kind, region_id, vcpus, memory_mib, disk_mib, state, restore_running, waiting_capacity, instance_id, generation, replaces, created_at, updated_at)
			VALUES ($1, 'org', 'proj', 'env', 'web', 'service', 'reg_1', 1, 512, 1024, $2, false, false, '', 1, $3, now(), now())`, id, state, replaces)
		return err
	}
	if err := insert("plc_1", "running", ""); err != nil {
		t.Fatal(err)
	}
	requireSQLState(t, insert("plc_2", "reserved", ""), "23505")
	if err := insert("plc_2", "reserved", "plc_1"); err != nil {
		t.Fatal(err)
	}
	requireSQLState(t, insert("plc_3", "reserved", "plc_1"), "23505")
	exec(t, conn,
		`INSERT INTO loomplane_platform_egress_counters (org_id, instance_id, counter_id, last_bytes) VALUES ('org', 'inst', 'c1', 10)`)
	if got := strs(t, conn, `SELECT COALESCE(to_regclass('loomplane_platform_egress')::text, '')`); len(got) != 1 || got[0] != "" {
		t.Fatalf("the old egress table survived: %q", got)
	}

	down(t, ex, "platform_cloud_continuity")
	if got := strs(t, conn, `SELECT id||':'||state FROM loomplane_platform_placements ORDER BY id`); !slices.Equal(got, []string{"plc_1:running", "plc_2:released"}) {
		t.Fatalf("placements after down = %q", got)
	}
	if got := strs(t, conn, `SELECT COALESCE(to_regclass('loomplane_platform_egress')::text, '')`); len(got) != 1 || got[0] == "" {
		t.Fatalf("the old egress table is missing after down: %q", got)
	}
	up(t, ex, "platform_cloud_continuity")
}
