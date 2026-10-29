//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

func TestClusterBudgetReviewSurvivesRestart(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	now := time.Now().UTC()
	op := &cloudinfra.ClusterOperation{ID: "money-review", OrgID: "org", CloudID: "cloud", Kind: "create", SchemaVersion: 1, Status: "preview", Revision: 1, Digest: "review", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), Input: cloudinfra.ClusterInput{PriceDigest: "price"}, MoneyEstimate: &cloudinfra.MoneyEstimate{HourlyMicroUSD: 29, HorizonSeconds: 3600, HorizonMicroUSD: 29, Known: false, MissingComponents: []string{"storage"}, PriceDigest: "price", CheckedAt: now}}
	if err := store.CreateClusterPreview(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	connection := pgdriver.New()
	if err := connection.Open(t.Context(), db.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	restarted := cloudinfra.NewPgStore(connection)
	saved, err := restarted.GetClusterOperation(t.Context(), "org", "cloud", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MoneyEstimate == nil || saved.MoneyEstimate.HourlyMicroUSD != 29 || saved.MoneyEstimate.Known || len(saved.MoneyEstimate.MissingComponents) != 1 {
		t.Fatalf("lost estimate: %+v", saved.MoneyEstimate)
	}
	saved.Revision++
	saved.MoneyEstimate.HourlyMicroUSD = 0
	if err := restarted.SaveClusterCheckpoint(t.Context(), saved, 1); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("checkpoint rewrote reviewed price: %v", err)
	}
}
