//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	fed "github.com/xraph/loomplane/internal/federation"
)

func federationMigration(t *testing.T, version string) *migrate.Migration {
	t.Helper()
	for _, m := range migrations.Group().Migrations() {
		if m.Version == version {
			return m
		}
	}
	t.Fatal("migration missing", version)
	return nil
}
func TestFederationRetirementMigrationRecoversPartialDDL(t *testing.T) {
	db, _, _, _ := installationDatabase(t)
	if _, err := db.Exec(t.Context(), `ALTER TABLE loomplane_federation_launch_attempts DROP CONSTRAINT loomplane_federation_launch_attempts_state_check,DROP CONSTRAINT loomplane_federation_launch_attempts_check`); err != nil {
		t.Fatal(err)
	}
	m := federationMigration(t, "20261021000400")
	if err := m.Up(t.Context(), pgmigrate.New(db)); err != nil {
		t.Fatal("partial upgrade cannot resume", err)
	}
	if err := m.Up(t.Context(), pgmigrate.New(db)); err != nil {
		t.Fatal("upgrade cannot replay", err)
	}
}
func TestFederationRetirementDowngradeKeepsConstraints(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	id := uuid.NewString()
	in := fed.LaunchIntent{ID: id, EnrollmentID: uuid.NewString(), Namespace: "apps", PodUID: "pod-uid", ConfigMapName: "loomplane-agent-" + id, Digest: strings.Repeat("a", 64)}
	if _, err := store.ReserveLaunch(ctx, *d, in, "operator", allow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginLaunchRetirement(ctx, d.OrgID, id, 1, "operator", allow); err != nil {
		t.Fatal(err)
	}
	if err := federationMigration(t, "20261021000400").Down(ctx, pgmigrate.New(db)); err == nil {
		t.Fatal("retirement history discarded")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='loomplane_federation_launch_attempts'::regclass AND conname IN ('loomplane_federation_launch_attempts_state_check','loomplane_federation_launch_attempts_check')`).Scan(&count); err != nil || count != 2 {
		t.Fatal("refused downgrade removed constraints", err, count)
	}
}
func TestFederationPreparationDowngradeRetainsOwnership(t *testing.T) {
	db, store, d, _ := installationDatabase(t)
	ctx := t.Context()
	allow := func(context.Context) error { return nil }
	in := fed.PreparationIntent{ID: uuid.NewString(), Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, Image: "example.test/agent@sha256:" + strings.Repeat("a", 64)}
	if _, err := store.ReservePreparation(ctx, *d, in, "operator", allow); err != nil {
		t.Fatal(err)
	}
	if err := federationMigration(t, "20261022000100").Down(ctx, pgmigrate.New(db)); err == nil {
		t.Fatal("ownership journal discarded")
	}
	if _, err := store.GetPreparation(ctx, d.OrgID, in.ID); err != nil {
		t.Fatal("refused downgrade lost ownership", err)
	}
}
