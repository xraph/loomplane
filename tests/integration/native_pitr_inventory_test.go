//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recovery"
)

func TestNativePITRWindowPersistenceKeepsSnapshotHistorySeparate(t *testing.T) {
	f := nativePointHistoryFixture(t)
	store, ok := any(f.store).(recovery.NativePITRStore)
	if !ok {
		t.Fatal("native PITR has no independent durable window observation")
	}
	now := time.Now().UTC().Truncate(time.Second)
	snapshot := nativeIdentityPoint(t, now.Add(-24*time.Hour).Format(time.RFC3339Nano))
	if err := f.refresh(t.Context(), []recovery.NativePoint{snapshot}); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.GetNativeSource(t.Context(), "org", f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	window := recovery.NativePoint{ProviderID: "arn:aws:rds:us-east-1:123456789012:db:database", Name: "database", Kind: "pitr", Status: "available", Engine: "postgres", Version: "17.2", CreatedAt: now.Add(-30 * 24 * time.Hour).Add(123456789 * time.Nanosecond), Encrypted: true, KMSKey: "original-key", PITR: &recovery.NativePITRBounds{Protocol: 1, Earliest: now.Add(-7 * 24 * time.Hour), Latest: now.Add(-time.Minute)}}
	fetch := func(context.Context, *recovery.NativeSource) (*recovery.NativePoint, error) { return &window, nil }
	saved, err := store.RefreshNativePITR(t.Context(), "org", f.source.ID, fetch)
	if err != nil || saved.ID == "" || saved.SourceID != f.source.ID || !saved.CreatedAt.Equal(window.CreatedAt) || !saved.Present {
		t.Fatal("native PITR lost source identity or creation precision", saved, err)
	}
	window.PITR.Earliest = window.PITR.Earliest.Add(time.Hour)
	window.PITR.Latest = window.PITR.Latest.Add(time.Second)
	rolled, err := store.RefreshNativePITR(t.Context(), "org", f.source.ID, fetch)
	if err != nil || rolled.ID != saved.ID || !rolled.PITR.Latest.Equal(window.PITR.Latest) {
		t.Fatal("rolling provider bounds created a fake snapshot identity", rolled, err)
	}
	observed, err := f.store.GetNativeSource(t.Context(), "org", f.source.ID)
	if err != nil || observed.State != before.State || observed.Complete != before.Complete || !observed.ObservedAt.Equal(*before.ObservedAt) {
		t.Fatal("PITR observation changed snapshot scan completeness", observed, err)
	}
	inventory, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(inventory.Points) != 1 || inventory.Points[0].Kind == "pitr" {
		t.Fatal("PITR appeared as a snapshot", inventory, err)
	}
	if _, err = f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{Before: saved.ID}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("snapshot cursor accepted a hidden PITR window", err)
	}
	if err = f.refresh(t.Context(), []recovery.NativePoint{snapshot}); err != nil {
		t.Fatal(err)
	}
	retained, err := f.store.GetNativePoint(t.Context(), "org", f.source.ID, saved.ID)
	if err != nil || !retained.Present {
		t.Fatal("snapshot refresh marked a separately observed PITR window absent", retained, err)
	}
	_, entries, err := f.store.NativeRetentionSnapshot(t.Context(), "org", f.source.ID)
	if err != nil || len(entries) != 1 || entries[0].Point.Kind == "pitr" {
		t.Fatal("full-backup retention consumed PITR metadata", entries, err)
	}
	called := false
	if _, err = store.RefreshNativePITR(t.Context(), "foreign-org", f.source.ID, func(context.Context, *recovery.NativeSource) (*recovery.NativePoint, error) {
		called = true
		return &window, nil
	}); !errors.Is(err, recovery.ErrNotFound) || called {
		t.Fatal("PITR refresh crossed organization scope", called, err)
	}
}
