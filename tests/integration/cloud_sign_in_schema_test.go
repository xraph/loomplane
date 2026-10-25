//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
