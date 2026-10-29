//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
)

func TestRegistryOperationsPersistAndFenceConnections(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := t.Context()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err := pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := cloudinfra.NewPgStore(pg)
	credentials := connections.NewPgStore(pg)
	now := time.Now().UTC()
	c := &connections.Connection{ID: "conn_registry_lifecycle", OrgID: "org", Cloud: connections.Kubernetes, Name: "Registry", Method: connections.MethodToken, Status: "ready", Details: map[string]string{"registry_hostname": "images.example.com", "cluster_ca": "ca", "endpoint": "https://cluster"}, CreatedAt: now, UpdatedAt: now}
	if err := credentials.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	state := &cloudinfra.RegistryState{OrgID: c.OrgID, ConnectionID: c.ID, Hostname: c.Details["registry_hostname"], Status: "running", Action: &cloudinfra.RegistryAction{ID: "operation", Kind: "backup", Phase: "backed_up"}}
	if err := store.SaveRegistryState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := credentials.DeleteConnection(ctx, c.OrgID, c.ID); !errors.Is(err, connections.ErrInUse) {
		t.Fatal("connection deleted during registry work", err)
	}
	if err := credentials.RegistryAccessAllowed(ctx, c.OrgID, c.ID); !errors.Is(err, connections.ErrNotReady) {
		t.Fatal("build admitted during maintenance", err)
	}
	saved, err := store.RegistryState(ctx, c.OrgID, c.ID)
	if err != nil || saved.Action.ID != "operation" || saved.Action.Phase != "backed_up" {
		t.Fatal("checkpoint lost", err)
	}
	if _, err = store.RegistryState(ctx, "another-org", c.ID); !errors.Is(err, cloudinfra.ErrNotFound) {
		t.Fatal("cross-organization state exposed", err)
	}
	candidates, err := store.RegistryCandidates(ctx)
	if err != nil || len(candidates) != 1 {
		t.Fatal("registry missing from recovery", err)
	}
	state.Status = "ready"
	state.Action = nil
	if err = store.SaveRegistryState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err = credentials.RegistryAccessAllowed(ctx, c.OrgID, c.ID); err != nil {
		t.Fatal(err)
	}
	state.Status = "removed"
	if err = store.SaveRegistryState(ctx, state); err != nil {
		t.Fatal(err)
	}
	candidates, err = store.RegistryCandidates(ctx)
	if err != nil || len(candidates) != 0 {
		t.Fatal("removed registry recovered", err)
	}
	if err = credentials.RegistryAccessAllowed(ctx, c.OrgID, c.ID); !errors.Is(err, connections.ErrNotReady) {
		t.Fatal("removed registry admitted", err)
	}
	if err = credentials.DeleteConnection(ctx, c.OrgID, c.ID); err != nil {
		t.Fatal("retired registry retained connection use", err)
	}
}
