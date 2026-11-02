//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/projects"
)

func TestFleetLegacyRouteAdmissionDenied(t *testing.T) {
	f := newFleetFixture(t, 2)
	reserved, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("route"))
	if err != nil {
		t.Fatal(err)
	}
	st := cloudinfra.NewPgStore(f.db)
	route := cloudinfra.Route{ID: "route", OrgID: "org", ProjectID: "proj", Input: cloudinfra.RouteInput{EnvironmentID: reserved.Instance.EnvironmentID, Service: "svc:artifact/api"}}
	for _, kind := range []string{"apply", "remove", "verify", "release", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			admissionErr := st.AcceptRoute(t.Context(), &cloudinfra.RouteOperation{ID: "operation", Route: route, Kind: kind})
			if !errors.Is(admissionErr, projects.ErrFleetManaged) {
				t.Fatalf("managed route admission: %v", admissionErr)
			}
		})
	}
	if err = st.UpdateRouteTTL(t.Context(), &route, 60); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("managed TTL: %v", err)
	}
	if err = st.AcceptEdge(t.Context(), &route, &cloudinfra.EdgeState{}); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("managed edge: %v", err)
	}
}

func TestFleetHistoricalCleanupDenied(t *testing.T) {
	f := newFleetFixture(t, 2)
	reserved, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = cloudinfra.GuardLegacyCleanup(t.Context(), tx, "org", "proj", reserved.Instance.EnvironmentID, "svc:artifact/api", "")
	if !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("managed cleanup: %v", err)
	}
}

func TestFleetLegacyControllerWriteDenied(t *testing.T) {
	f := newFleetFixture(t, 2)
	reserved, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("controller"))
	if err != nil {
		t.Fatal(err)
	}
	st := kubernetes.NewControllerStore(f.db)
	err = st.CheckLegacyManagedWrite(t.Context(), kubernetes.ManagedRuntimeIdentity{Org: "org", Project: "proj", Environment: reserved.Instance.EnvironmentID})
	if !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy controller: %v", err)
	}
}

func TestFleetSavedRouteOwnershipRechecked(t *testing.T) {
	f := newFleetFixture(t, 2)
	reserved, err := f.store.ReserveInstance(t.Context(), f.scope, f.input("saved-route"))
	if err != nil {
		t.Fatal(err)
	}
	st := cloudinfra.NewPgStore(f.db)
	route := cloudinfra.Route{OrgID: "org", ProjectID: "wrong-project", Input: cloudinfra.RouteInput{EnvironmentID: reserved.Instance.EnvironmentID}}
	if err = st.GuardRouteOwnership(t.Context(), route); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("wrong project bypass: %v", err)
	}
	if _, err = st.BeginIngress(t.Context(), &cloudinfra.RouteOperation{Route: route, Kind: "remove"}); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("saved ingress: %v", err)
	}
	environments, err := f.projects.ListEnvironments(t.Context(), "org", "proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range environments {
		if env.InstanceID == "" {
			route.Input.EnvironmentID = env.ID
			if err = st.GuardRouteOwnership(t.Context(), route); err != nil {
				t.Fatalf("ordinary sibling: %v", err)
			}
		}
	}
}
