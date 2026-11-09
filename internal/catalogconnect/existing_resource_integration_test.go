//go:build integration

package catalogconnect

import (
	"errors"
	"testing"
	"time"
)

func TestPgStoreProtectsExternalResourceEndpoint(t *testing.T) {
	store, pg := scratchStore(t)
	e := &Endpoint{ID: "endpoint_existing", OrgID: "org", Name: "external-database", Engine: "postgres", Host: "db.example", Port: 5432, TLS: "verify-full", CreatedBy: "admin", CreatedAt: time.Now(), Revision: 1}
	if err := store.Create(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(t.Context(), `INSERT INTO loomplane_bindings(id,org_id,config) VALUES('external-binding','org','{"existing_resource":{"resource_id":"inventory","endpoint_id":"endpoint_existing"}}')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), "org", e.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("active external credential was removable: %v", err)
	}
	if _, err := store.Get(t.Context(), "org", e.ID); err != nil {
		t.Fatal("active endpoint was deleted")
	}
	if _, err := pg.Exec(t.Context(), `DELETE FROM loomplane_bindings WHERE id='external-binding'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), "org", e.ID); err != nil {
		t.Fatal(err)
	}
}
