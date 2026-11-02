package cloudinfra

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type fleetRouteGuardStore struct{ DomainStore }

func (fleetRouteGuardStore) GuardRouteOwnership(context.Context, Route) error {
	return projects.ErrFleetManaged
}

func TestFleetSavedRouteEffectsDenied(t *testing.T) {
	st := fleetRouteGuardStore{}
	svc := &Service{Deps: Deps{Domains: st}}
	for _, kind := range []string{"apply", "remove", "release", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			err := svc.executeRoute(t.Context(), st, &RouteOperation{Kind: kind}, &IngressState{})
			if !errors.Is(err, projects.ErrFleetManaged) {
				t.Fatalf("saved route: %v", err)
			}
		})
	}
}
func TestFleetSavedEdgeEffectsDenied(t *testing.T) {
	svc := &Service{Deps: Deps{Domains: fleetRouteGuardStore{}}}
	_, _, _, err := svc.edgeAuthority(t.Context(), security.Principal{}, Route{})
	if !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("saved edge: %v", err)
	}
}
