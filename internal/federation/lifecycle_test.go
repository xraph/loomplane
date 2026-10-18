package federation

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestDestinationLifecycleScopeAndGeneration(t *testing.T) {
	svc, resources, store, p, in := fixture(t)
	first, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Name = "secondary"
	second, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	foreign := *first
	foreign.ID, foreign.EnvironmentID = "foreign", "other-env"
	store.rows[foreign.ID] = foreign
	page, err := svc.ListDestinations(t.Context(), p, ListInput{ProjectID: "project", EnvironmentID: "env", Limit: 1})
	if err != nil || len(page.Destinations) != 1 || page.Next == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	next, err := svc.ListDestinations(t.Context(), p, ListInput{ProjectID: "project", EnvironmentID: "env", Limit: 1, After: page.Next})
	if err != nil || len(next.Destinations) != 1 || next.Next != "" || next.Destinations[0].ID == page.Destinations[0].ID {
		t.Fatalf("next page: %+v %v", next, err)
	}
	if _, err = svc.ListDestinations(t.Context(), security.Principal{OrgID: "foreign"}, ListInput{ProjectID: "project", EnvironmentID: "env"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign list: %v", err)
	}
	if _, err = svc.RenameDestination(t.Context(), p, first.ID, "renamed", first.Generation); err != nil {
		t.Fatal(err)
	}
	if err = svc.UnregisterDestination(t.Context(), p, first.ID, first.Generation); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale unregister: %v", err)
	}
	if err = svc.UnregisterDestination(t.Context(), security.Principal{OrgID: "foreign"}, first.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign unregister: %v", err)
	}
	// Unregister is local bookkeeping and must work even after credential checks expire.
	stale := time.Now().Add(-time.Hour)
	resources.conn.CheckedAt = &stale
	if err = svc.UnregisterDestination(t.Context(), p, first.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(t.Context(), p.OrgID, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("destination retained after unregister")
	}
	if _, err = store.Get(t.Context(), p.OrgID, second.ID); err != nil {
		t.Fatal("unregister removed another destination")
	}
	if got := svc.authz.(*authorizer).calls; got[len(got)-1] != security.DestinationManage {
		t.Fatal("unregister skipped management authorization")
	}
}

func TestDestinationLifecycleDenial(t *testing.T) {
	svc, _, store, p, in := fixture(t)
	d, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{true, false} {
		svc.enabled = !disabled
		svc.authz = &authorizer{deny: !disabled}
		want := ErrDisabled
		if !disabled {
			want = security.ErrForbidden
		}
		if _, err = svc.ListDestinations(t.Context(), p, ListInput{ProjectID: "project", EnvironmentID: "env"}); !errors.Is(err, want) {
			t.Fatal(err)
		}
		if _, err = svc.RenameDestination(t.Context(), p, d.ID, "renamed", 1); !errors.Is(err, want) {
			t.Fatal(err)
		}
		if err = svc.UnregisterDestination(t.Context(), p, d.ID, 1); !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	if got := store.rows[d.ID]; got.Generation != 1 || got.Name != in.Name {
		t.Fatal("unauthorized mutation")
	}
}

func TestDestinationReenrollmentUsesNewIdentity(t *testing.T) {
	svc, resources, _, p, in := fixture(t)
	old, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	resources.conn.Details["namespace_uid"] = "replacement"
	if _, refreshErr := svc.RefreshCapabilities(t.Context(), p, old.ID); !errors.Is(refreshErr, ErrIdentityChanged) {
		t.Fatal(refreshErr)
	}
	if unregisterErr := svc.UnregisterDestination(t.Context(), p, old.ID, 2); unregisterErr != nil {
		t.Fatal(unregisterErr)
	}
	replacement, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == old.ID || replacement.Identity.Revision() == old.Identity.Revision() || replacement.Quarantined {
		t.Fatal("replacement reused old authority")
	}
	if replacement.Capabilities.Supports(domain.WorkloadAttestation, replacement.Identity.Revision(), time.Now()) {
		t.Fatal("re-registration invented workload authority")
	}
}

func TestDestinationListInvalidatesCapabilities(t *testing.T) {
	svc, resources, store, p, in := fixture(t)
	d, err := svc.CreateDestination(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.rows[d.ID]
	saved.Capabilities.Results[domain.WorkloadAttestation] = domain.CapabilityResult{State: domain.Supported}
	store.rows[d.ID] = saved
	resources.conn.Details["namespace_uid"] = "replacement"
	page, err := svc.ListDestinations(t.Context(), p, ListInput{ProjectID: in.ProjectID, EnvironmentID: in.EnvironmentID})
	if err != nil || len(page.Destinations) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	if page.Destinations[0].Capabilities.Revision != "" {
		t.Fatal("list retained changed identity authority")
	}
	for _, limit := range []int{-1, 101} {
		if _, err := svc.ListDestinations(t.Context(), p, ListInput{ProjectID: in.ProjectID, EnvironmentID: in.EnvironmentID, Limit: limit}); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
}
