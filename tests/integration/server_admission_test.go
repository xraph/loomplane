//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

func TestServerAdmissionAllowsProgressReferences(t *testing.T) {
	dsn := freshDatabase(t)
	if err := ops.Migrate(t.Context(), dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err = conn.Exec(t.Context(), `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('conn_guard','o','server','test','daemon','ready','u',now(),now())`); err != nil {
		t.Fatal(err)
	}
	admission, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Rollback(t.Context())
	if _, err = admission.Exec(t.Context(), `SELECT 1 FROM loomplane_cloud_connections WHERE id='conn_guard' FOR SHARE`); err != nil {
		t.Fatal(err)
	}
	progress, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close(t.Context())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err = progress.Exec(ctx, `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('o','conn_guard','binding','test')`); err != nil {
		t.Fatalf("saving deployment progress deadlocks its own admission lock: %v", err)
	}
	if err = admission.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	use, err := progress.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer use.Rollback(t.Context())
	if _, err = use.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('o','conn_guard','binding','second')`); err != nil {
		t.Fatal(err)
	}
	retire, stop := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer stop()
	if _, err = conn.Exec(retire, `UPDATE loomplane_cloud_connections SET status='deleting' WHERE id='conn_guard'`); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reference admission no longer serializes retirement: %v", err)
	}
	if err = use.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = progress.Exec(t.Context(), `DELETE FROM loomplane_cloud_connections WHERE id='conn_guard'`); err == nil {
		t.Fatal("live connection use no longer prevents deletion")
	}
}
