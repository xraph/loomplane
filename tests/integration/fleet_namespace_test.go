//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

// Installed PostgreSQL reservation, replay and epoch regressions live with the
// namespace adapter so they can exercise its exact journal reconciliation.
func TestInstanceNamespaceRefusesCallerOpenedAuthority(t *testing.T) {
	type legacyReservation interface {
		ReserveInstanceNamespaceOperation(context.Context, cloudinfra.InstanceNamespaceRecord, *cloudinfra.ClusterOperation, func(context.Context, driver.Tx) error) (*cloudinfra.ClusterOperation, error)
	}
	if _, ok := any(cloudinfra.NewPgStore(nil)).(legacyReservation); ok {
		t.Fatal("store still exposes caller-opened namespace authorization")
	}
	var adapter *cloudinfra.InstanceNamespaceTransactions
	if _, err := adapter.Reserve(t.Context(), cloudinfra.InstanceNamespaceRecord{}, &cloudinfra.ClusterOperation{}); err == nil {
		t.Fatal("namespace accepted without installed authority")
	}
}
