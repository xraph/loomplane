//go:build cloud

package platform

import (
	"context"
	"testing"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

// A cloud build passes the guard on a database whose cloud groups it
// knows. The groups a cloud build knows are migrations.All(), which ends
// with the edition's own, so this adds the fake group to that list the way
// editionGroups will.
func TestGuard_CloudGroupsAreKnown(t *testing.T) {
	dsn := guardDatabase(t)
	known := append(migrations.All(), fakeCloudGroup())
	if err := ops.Migrate(context.Background(), dsn, known); err != nil {
		t.Fatal(err)
	}
	if err := guardCheck(dsn, known); err != nil {
		t.Fatalf("guard refused a database whose cloud groups the build knows: %v", err)
	}
}
