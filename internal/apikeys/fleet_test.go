package apikeys

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

type fleetKeyAdmin struct{}

func (fleetKeyAdmin) Allowed(_ context.Context, p security.Principal, permission security.Permission) (bool, error) {
	return p.UserID == "owner" && !p.ReadOnly && permission == security.OrganizationAdminister, nil
}

func TestFleetCredentialScopeAndRevocation(t *testing.T) {
	engine, err := keysmith.NewEngine(keysmith.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(engine, "app", fleetKeyAdmin{}, nil)
	owner := security.Principal{UserID: "owner", OrgID: "org"}
	scope := security.FleetScope{ProjectID: "project", FleetID: "fleet"}
	created, err := svc.CreateFleetCredential(t.Context(), owner, scope, NewKey{Name: "control", Access: Write})
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Authenticate(t.Context(), created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsFleet() || p.UserID == owner.UserID || p.Fleet == nil || *p.Fleet != scope || p.ReadOnly {
		t.Fatalf("wrong principal: %+v", p)
	}
	if err = svc.ValidatePrincipal(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	memberForgery := owner
	memberForgery.KeyID = p.KeyID
	if err = svc.ValidatePrincipal(t.Context(), memberForgery); !errors.Is(err, ErrRejected) {
		t.Fatalf("fleet key reused as member authority: %v", err)
	}
	forged := p
	other := scope
	other.FleetID = "other"
	forged.Fleet = &other
	if err = svc.ValidatePrincipal(t.Context(), forged); !errors.Is(err, ErrRejected) {
		t.Fatalf("accepted changed scope: %v", err)
	}
	if _, err = svc.CreateFleetCredential(t.Context(), p, scope, NewKey{Name: "escalation", Access: Write}); err == nil {
		t.Fatal("service credential created another key")
	}
	if err = svc.Revoke(t.Context(), owner, p.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Authenticate(t.Context(), created.Secret); !errors.Is(err, ErrRejected) {
		t.Fatalf("revoked key authenticated: %v", err)
	}
	if err = svc.ValidatePrincipal(t.Context(), p); !errors.Is(err, ErrRejected) {
		t.Fatalf("revoked saved authority accepted: %v", err)
	}
}
func TestFleetHumanReadOnlyCredentialDenied(t *testing.T) {
	engine, err := keysmith.NewEngine(keysmith.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(engine, "app", fleetKeyAdmin{}, nil)
	for _, p := range []security.Principal{{UserID: "owner", OrgID: "org", ReadOnly: true}, {UserID: "member", OrgID: "org"}, {UserID: "owner", OrgID: "org", KeyID: "key"}} {
		if _, err = svc.CreateFleetCredential(t.Context(), p, security.FleetScope{ProjectID: "project", FleetID: "fleet"}, NewKey{Name: "control", Access: Write}); err == nil {
			t.Fatalf("credential escalation: %+v", p)
		}
	}
}
