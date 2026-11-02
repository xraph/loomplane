package identity

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/store/memory"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

type fleetStreamOwner struct{}

func (fleetStreamOwner) Allowed(_ context.Context, p security.Principal, _ security.Permission) (bool, error) {
	return p.UserID == "owner", nil
}

func TestFleetStreamRevalidatesServiceCredential(t *testing.T) {
	engine, err := keysmith.NewEngine(keysmith.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	keys := apikeys.New(engine, "app", fleetStreamOwner{}, nil)
	owner := security.Principal{UserID: "owner", OrgID: "org"}
	created, err := keys.CreateFleetCredential(t.Context(), owner, security.FleetScope{ProjectID: "project", FleetID: "fleet"}, apikeys.NewKey{Name: "control", Access: apikeys.Read})
	if err != nil {
		t.Fatal(err)
	}
	// No authsome membership store: a service has no member identity to inherit.
	e := &Extension{keys: keys}
	request := httptest.NewRequestWithContext(t.Context(), "GET", "/events", nil)
	request.Header.Set("Authorization", "Bearer "+created.Secret)
	principal, err := e.revalidateStream(t.Context(), request)
	if err != nil || !principal.IsFleet() || !principal.ReadOnly || principal.Fleet.FleetID != "fleet" {
		t.Fatalf("stream principal: %+v %v", principal, err)
	}
	if err = keys.Revoke(t.Context(), owner, principal.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.revalidateStream(t.Context(), request); err == nil {
		t.Fatal("revoked service stream stayed authorized")
	}
}
