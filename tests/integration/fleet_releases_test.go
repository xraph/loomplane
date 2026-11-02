//go:build integration

package integration

import (
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
)

func fleetReleaseDocument() []byte {
	return []byte(`schema_version: 1
manifest:
  version: 1
  services:
    api:
      kind: http-service
      build: {image: registry.example.test/api@sha256:` + strings.Repeat("a", 64) + `}
profiles:
  small:
    isolation: namespace
    services:
      api: {cpu_millis: 500, memory_mib: 256, min_replicas: 1, max_replicas: 2}
`)
}
func TestFleetReleaseImmutable(t *testing.T) {
	f := newFleetFixture(t, 2)
	p := security.Principal{UserID: "usr", OrgID: "org"}
	raw := fleetReleaseDocument()
	release, err := f.service.PublishRelease(t.Context(), p, f.scope.FleetID, raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.service.PublishRelease(t.Context(), p, f.scope.FleetID, raw)
	if err != nil || again.ID != release.ID {
		t.Fatalf("publication replay: %+v %v", again, err)
	}
	for i := range raw {
		raw[i] = 0
	}
	service := release.Template.Manifest.Services["api"]
	service.Build.Image = "changed"
	loaded, err := f.store.GetRelease(t.Context(), f.scope, release.ID)
	if err != nil || loaded.Template.Manifest.Services["api"].Build.Image == "changed" {
		t.Fatalf("mutated release: %+v %v", loaded, err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_releases SET digest='changed' WHERE id=$1`, release.ID); err == nil {
		t.Fatal("database allowed release update")
	}
	if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_fleet_releases WHERE id=$1`, release.ID); err == nil {
		t.Fatal("database allowed release deletion")
	}
	scope := f.scope
	scope.OrgID = "other"
	if _, err = f.store.GetRelease(t.Context(), scope, release.ID); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("foreign release: %v", err)
	}
	p.ReadOnly = true
	if _, err = f.service.PublishRelease(t.Context(), p, f.scope.FleetID, fleetReleaseDocument()); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("readonly publish: %v", err)
	}
}
