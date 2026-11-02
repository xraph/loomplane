//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

func nativeResourcePage(t *testing.T, resource, before string) recovery.NativePageInput {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"resource": resource, "environment_id": "environment", "before": before, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	var page recovery.NativePageInput
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestNativeResourceScopeFiltersRetainedHistoriesAndCopiesBeforePagination(t *testing.T) {
	f, admission, request, digest := nativeAdmissionFixture(t)
	bindings := planning.NewPgStore(f.db)
	other := f.source.Binding
	other.ID, other.Name, other.PhysicalID, other.ExternalID = "analytics-binding", "analytics", "analytics-original", "source-project:us-central1:analytics"
	if err := bindings.PutBinding(t.Context(), &other); err != nil {
		t.Fatal(err)
	}
	other.PhysicalID = "analytics-replacement"
	if err := bindings.PutBinding(t.Context(), &other); err != nil {
		t.Fatal(err)
	}
	all, err := f.store.ListNativeSources(t.Context(), "org", "project", recovery.NativePageInput{})
	if err != nil {
		t.Fatal(err)
	}
	var otherSource recovery.NativeSource
	for _, source := range all.Sources {
		if source.PhysicalID == other.PhysicalID {
			otherSource = source
		}
	}
	if otherSource.ID == "" {
		t.Fatal("unrelated retained source missing")
	}
	otherFixture := f
	otherFixture.source = otherSource
	point := request.Point
	point.ID, point.SourceID, point.ProviderID, point.Name = "", "", "projects/source-project/backups/analytics", "analytics"
	if err = otherFixture.refresh(t.Context(), []recovery.NativePoint{point}); err != nil {
		t.Fatal(err)
	}
	inventory, err := f.store.ReadNativeInventory(t.Context(), "org", otherSource.ID, recovery.NativePageInput{})
	if err != nil || len(inventory.Points) != 1 {
		t.Fatal("other point fixture", err)
	}
	raw, err := json.Marshal(otherSource.Binding)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	otherDigest := hex.EncodeToString(hash[:])
	for i, id := range []string{"database-a", "database-b", "analytics-copy"} {
		record := *request
		record.ID, record.RequestID = id, "request-"+id
		record.CreatedAt = request.CreatedAt.Add(time.Duration(i) * time.Microsecond)
		record.ExpiresAt = record.CreatedAt.Add(time.Hour)
		selectedDigest := digest
		if id == "analytics-copy" {
			record.SourceID = otherSource.ID
			record.Point = inventory.Points[0]
			record.PointID = record.Point.ID
			selectedDigest = otherDigest
		}
		if _, err = admission.CreateReviewedNativeRestore(t.Context(), &record, selectedDigest); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{request.Source.ID, other.ID} {
		if err = bindings.DeleteBinding(t.Context(), "org", id); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.store.ListNativeSources(t.Context(), "org", "project", nativeResourcePage(t, "database", ""))
	if err != nil || len(first.Sources) != 1 || first.Sources[0].Name != "database" || first.NextBefore == "" {
		t.Fatalf("resource filtered after source pagination: %+v %v", first, err)
	}
	next, err := f.store.ListNativeSources(t.Context(), "org", "project", nativeResourcePage(t, "database", first.NextBefore))
	if err != nil || len(next.Sources) != 1 || next.Sources[0].Name != "database" || next.Sources[0].ID == first.Sources[0].ID || next.NextBefore != "" {
		t.Fatalf("resource cursor lost retained incarnation: %+v %v", next, err)
	}
	if _, err = f.store.ListNativeSources(t.Context(), "org", "project", nativeResourcePage(t, "analytics", first.NextBefore)); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("foreign-resource source cursor accepted", err)
	}
	if _, err = f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, nativeResourcePage(t, "analytics", "")); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("source inventory ignored resource assertion", err)
	}
	copies, err := f.store.ListNativeCopies(t.Context(), "org", "project", nativeResourcePage(t, "database", ""))
	if err != nil || len(copies.Copies) != 1 || copies.Copies[0].ID != "database-b" || copies.NextBefore != "database-b" {
		t.Fatalf("resource filtered after copy pagination: %+v %v", copies, err)
	}
	copies, err = f.store.ListNativeCopies(t.Context(), "org", "project", nativeResourcePage(t, "database", copies.NextBefore))
	if err != nil || len(copies.Copies) != 1 || copies.Copies[0].ID != "database-a" || copies.NextBefore != "" {
		t.Fatalf("copy resource cursor skipped a retained copy: %+v %v", copies, err)
	}
	if _, err = f.store.ListNativeCopies(t.Context(), "org", "project", nativeResourcePage(t, "analytics", "database-b")); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("foreign-resource copy cursor accepted", err)
	}
	for _, resource := range []string{"missing", "dat%"} {
		sources, listErr := f.store.ListNativeSources(t.Context(), "org", "project", nativeResourcePage(t, resource, ""))
		if listErr != nil || len(sources.Sources) != 0 {
			t.Fatal("source resource match is not exact", resource, listErr)
		}
		records, listErr := f.store.ListNativeCopies(t.Context(), "org", "project", nativeResourcePage(t, resource, ""))
		if listErr != nil || len(records.Copies) != 0 {
			t.Fatal("copy resource match is not exact", resource, listErr)
		}
	}
}
