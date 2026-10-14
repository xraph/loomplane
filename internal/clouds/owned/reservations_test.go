//go:build integration

package owned

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

func reservationDatabase(t *testing.T) *Reservations {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for reservation tests")
	}
	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_reservation_%d", time.Now().UnixNano())
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dsn = u.String()
	if err = ops.Migrate(t.Context(), dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	db := pgdriver.New()
	if err = db.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,status,created_by,created_at,updated_at) VALUES('conn_one','org_one','server','One','ready','usr_one',now(),now()),('conn_two','org_two','server','Two','ready','usr_two',now(),now())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewReservations(db)
}

func TestReservations_serializesDifferentConnectionsAndProjectsOnOneDaemon(t *testing.T) {
	r := reservationDatabase(t)
	first := Reservation{Scope: "docker/daemon-a", Port: 8080, Owner: "inst_one", OrgID: "org_one", ProjectID: "proj_one", EnvironmentID: "env_one", ConnectionID: "conn_one"}
	if err := r.Reserve(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := r.Reserve(t.Context(), first); err != nil {
		t.Fatalf("idempotent owner: %v", err)
	}
	other := first
	other.Owner = "inst_two"
	other.ConnectionID = "conn_two"
	other.OrgID = "org_two"
	other.ProjectID = "proj_two"
	other.EnvironmentID = "env_two"
	if err := r.Reserve(t.Context(), other); !errors.Is(err, ErrReserved) {
		t.Fatalf("cross-connection collision: %v", err)
	}
	if err := r.Release(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if err := r.Reserve(t.Context(), other); !errors.Is(err, ErrReserved) {
		t.Fatalf("foreign release removed owner: %v", err)
	}
	if err := r.Release(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := r.Reserve(t.Context(), other); err != nil {
		t.Fatalf("released port unavailable: %v", err)
	}
}
