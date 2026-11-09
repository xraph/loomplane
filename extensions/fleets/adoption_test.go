package fleets

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/forge"

	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type adoptionReadStore struct {
	providerresources.Store
	calls   int
	org, id string
}

func (s *adoptionReadStore) Get(_ context.Context, org, id string) (*providerresources.ProviderResource, error) {
	s.calls++
	s.org, s.id = org, id
	return &providerresources.ProviderResource{ID: id, OrgID: org, Revision: 3, ObservedAt: time.Now().UTC()}, nil
}

type adoptionReadAuthorizer struct {
	principal  security.Principal
	ctx        context.Context
	permission security.Permission
	denied     bool
}

func (a *adoptionReadAuthorizer) Require(ctx context.Context, p security.Principal, permission security.Permission, _ string) error {
	a.principal, a.ctx, a.permission = p, ctx, permission
	if a.denied {
		return security.ErrForbidden
	}
	return nil
}

func TestFleetAdoptionInventoryResolvesLateAndPreservesCaller(t *testing.T) {
	c := forge.NewContainer()
	inventory := lazyAdoptionInventory{container: c}
	p := security.Principal{OrgID: "org", UserID: "operator", KeyID: "read-key", ReadOnly: true}
	scope := domain.FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}
	in := fleet.AdoptionInput{NamespaceUID: "caller-supplied-uid", Resources: []fleet.AdoptionResourceInput{{InventoryID: "resource"}}}
	if _, err := inventory.InspectAdoption(t.Context(), p, scope, in); !errors.Is(err, domain.ErrAdoptionInventory) {
		t.Fatalf("missing inventory: %v", err)
	}
	store := &adoptionReadStore{}
	authz := &adoptionReadAuthorizer{}
	if err := forge.ProvideValue(c, providerresources.NewService(providerresources.Deps{Store: store, Authorizer: authz})); err != nil {
		t.Fatal(err)
	}
	ctx := security.WithPrincipal(t.Context(), p)
	out, err := inventory.InspectAdoption(ctx, p, scope, in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(authz.principal, p) || authz.ctx != ctx || authz.permission != security.InfrastructureRead {
		t.Fatalf("caller changed: %+v permission=%s", authz.principal, authz.permission)
	}
	if store.calls != 1 || store.org != p.OrgID || store.id != "resource" || len(out.Resources) != 1 || out.Resources[0].Revision != 3 {
		t.Fatalf("inventory read: store=%+v output=%+v", store, out)
	}
	if out.Namespace != (domain.AdoptionNamespaceObservation{}) {
		t.Fatalf("unverified namespace became evidence: %+v", out.Namespace)
	}
	authz.denied = true
	if _, err = inventory.InspectAdoption(ctx, p, scope, in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked inventory access: %v", err)
	}
	if store.calls != 1 {
		t.Fatal("denied request reached inventory store")
	}
}

func TestFleetAdoptionInventoryRejectsNilService(t *testing.T) {
	c := forge.NewContainer()
	var svc *providerresources.Service
	if err := forge.ProvideValue(c, svc); err != nil {
		t.Fatal(err)
	}
	inventory := lazyAdoptionInventory{container: c}
	p := security.Principal{OrgID: "org", UserID: "operator"}
	if _, err := inventory.InspectAdoption(t.Context(), p, domain.FleetScope{OrgID: "org"}, fleet.AdoptionInput{}); !errors.Is(err, domain.ErrAdoptionInventory) {
		t.Fatalf("nil service: %v", err)
	}
}

func TestFleetAdoptionInventoryDeniesForeignScopeBeforeRead(t *testing.T) {
	c := forge.NewContainer()
	store := &adoptionReadStore{}
	authz := &adoptionReadAuthorizer{}
	if err := forge.ProvideValue(c, providerresources.NewService(providerresources.Deps{Store: store, Authorizer: authz})); err != nil {
		t.Fatal(err)
	}
	inventory := lazyAdoptionInventory{container: c}
	in := fleet.AdoptionInput{Resources: []fleet.AdoptionResourceInput{{InventoryID: "resource"}}}
	if _, err := inventory.InspectAdoption(t.Context(), security.Principal{OrgID: "other", UserID: "operator"}, domain.FleetScope{OrgID: "org"}, in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("foreign scope: %v", err)
	}
	if store.calls != 0 || authz.ctx != nil {
		t.Fatal("foreign scope reached inventory service")
	}
}
