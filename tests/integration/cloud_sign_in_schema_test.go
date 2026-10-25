//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

// Signing in adds a method to connections, an app per organization and
// cloud, a single-use pending sign-in, and a kind on the outbox.
func TestCloudSignInSchema(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := context.Background()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	now := time.Now().UTC()

	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_cloud_connections (id, org_id, cloud, name, status, created_by, created_at, updated_at)
		VALUES ('conn_1', 'org_1', 'gcp', 'main', 'connected', 'usr_1', $1, $1)`, now); err != nil {
		t.Fatal(err)
	}
	var method string
	if err = conn.QueryRow(ctx, `SELECT method FROM loomplane_cloud_connections WHERE id = 'conn_1'`).Scan(&method); err != nil {
		t.Fatal(err)
	}
	if method != "token" {
		t.Fatalf("method = %q, want token", method)
	}

	insertApp := `INSERT INTO loomplane_oauth_apps (org_id, cloud, client_id, updated_by) VALUES ('org_1', 'gcp', 'client', 'usr_1')`
	if _, err = conn.Exec(ctx, insertApp); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, insertApp)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second app for one org and cloud: err = %v, want a unique violation", err)
	}

	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_oauth_sign_ins (state, org_id, user_id, cloud, name, expires_at)
		VALUES ('state_1', 'org_1', 'usr_1', 'gcp', 'main', $1)`, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var connectionID string
	if err = conn.QueryRow(ctx, `SELECT connection_id FROM loomplane_oauth_sign_ins WHERE state = 'state_1'`).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	if connectionID != "" {
		t.Fatalf("connection_id = %q, want empty", connectionID)
	}

	var kindDefault string
	if err = conn.QueryRow(ctx, `SELECT column_default FROM information_schema.columns
		WHERE table_name = 'loomplane_notification_outbox' AND column_name = 'kind'`).Scan(&kindDefault); err != nil {
		t.Fatalf("outbox kind column: %v", err)
	}
	if kindDefault != "'deployment'::text" {
		t.Fatalf("outbox kind default = %q, want 'deployment'", kindDefault)
	}
}

// Webhook endpoints made before connection.sign_in_needed existed hear it
// after the upgrade. Endpoints Loomplane didn't make are left alone, and
// running the backfill again changes nothing.
func TestCloudSignInWebhookBackfill(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := context.Background()
	// On a new database Loomplane's group runs before Relay's makes
	// relay_endpoints, so the backfill must cope with the table missing.
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	var backfill *migrate.Migration
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "cloud_sign_in_webhooks" {
			backfill = m
		}
	}
	if backfill == nil || backfill.Version != "20261103000200" {
		t.Fatalf("cloud_sign_in_webhooks migration: %+v", backfill)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `INSERT INTO relay_endpoints (id, tenant_id, url, event_types) VALUES
		('ep_old', 'org_1', 'https://example.com/a', ARRAY['deployment.succeeded','deployment.failed']),
		('ep_new', 'org_1', 'https://example.com/b', ARRAY['deployment.succeeded','deployment.failed','connection.sign_in_needed']),
		('ep_other', 'org_1', 'https://example.com/c', ARRAY['invoice.*'])`); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pg.Close() }()
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = backfill.Up(ctx, exec); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string][]string{
		"ep_old":   {"deployment.succeeded", "deployment.failed", "connection.sign_in_needed"},
		"ep_new":   {"deployment.succeeded", "deployment.failed", "connection.sign_in_needed"},
		"ep_other": {"invoice.*"},
	}
	for id, types := range want {
		var got []string
		if err = conn.QueryRow(ctx, `SELECT event_types FROM relay_endpoints WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != strings.Join(types, ",") {
			t.Fatalf("%s hears %v, want %v", id, got, types)
		}
	}
}
