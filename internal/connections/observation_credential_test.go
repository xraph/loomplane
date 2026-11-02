package connections

import (
	"context"
	"errors"
	"testing"
	"time"
)

type observationResolver struct {
	regular, observed int
	expiry            time.Time
}

func (r *observationResolver) ResolveManagedCredential(context.Context, Connection) (ManagedCredential, error) {
	r.regular++
	return ManagedCredential{}, errors.New("maintenance must not run")
}
func (r *observationResolver) ResolveManagedCredentialForObservation(_ context.Context, c Connection) (ManagedCredential, error) {
	r.observed++
	return ManagedCredential{Kubeconfig: []byte("runtime"), ExpiresAt: r.expiry, TrustRevision: c.Generation}, nil
}
func TestPlacementGroupObservationDoesNotMaintainCredentials(t *testing.T) {
	s, store, _, _ := inClusterRig(t, false)
	c := Connection{ID: "managed", OrgID: orgPrincipal.OrgID, Cloud: Kubernetes, Method: MethodManagedKubernetes, Generation: 7, Status: StatusReady}
	store.rows[c.ID] = c
	r := &observationResolver{expiry: time.Now().Add(5 * time.Minute)}
	s.ManagedCredentials = r
	if _, raw, err := s.ObservationCredential(t.Context(), c.OrgID, c.ID); err != nil || string(raw) != "runtime" {
		t.Fatalf("observation failed: %v", err)
	}
	r.expiry = time.Now().Add(30 * time.Second)
	if _, _, err := s.ObservationCredential(t.Context(), c.OrgID, c.ID); !errors.Is(err, ErrCredentialRefreshNeeded) {
		t.Fatalf("expired token accepted: %v", err)
	}
	if r.regular != 0 || r.observed != 2 {
		t.Fatalf("maintenance ran: %+v", r)
	}
	s.ManagedCredentials = managedResolverFunc(func(context.Context, Connection) (ManagedCredential, error) {
		t.Fatal("fallback maintenance called")
		return ManagedCredential{}, nil
	})
	if _, _, err := s.ObservationCredential(t.Context(), c.OrgID, c.ID); !errors.Is(err, ErrCredentialRefreshNeeded) {
		t.Fatal("unsupported observer did not fail closed")
	}
}
