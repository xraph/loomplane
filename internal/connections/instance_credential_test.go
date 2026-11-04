package connections

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInstanceGenericCredentialEntryPointsRefused(t *testing.T) {
	for _, markers := range []map[string]string{
		{"managed_instance_id": "instance"},
		{"managed_environment_id": "environment"},
		{"managed_instance_id": "instance", "managed_environment_id": "environment"},
	} {
		for _, method := range []string{MethodManagedKubernetes, MethodToken, MethodInCluster} {
			t.Run(method+markers["managed_instance_id"]+markers["managed_environment_id"], func(t *testing.T) {
				s, store, secrets, _ := inClusterRig(t, false)
				secretReads := &instanceBoundarySecrets{memSecrets: secrets}
				s.secrets = secretReads
				c := Connection{ID: "isolated", OrgID: orgPrincipal.OrgID, Cloud: Kubernetes, Method: method, Generation: 1, Status: StatusReady, Details: markers}
				store.rows[c.ID] = c
				secrets.kept[c.OrgID+"/"+c.ID] = []byte("must-not-escape")
				r := &instanceBoundaryResolver{}
				s.ManagedCredentials = r
				if _, raw, err := s.Credential(t.Context(), c.OrgID, c.ID); !errors.Is(err, ErrChanged) || len(raw) != 0 {
					t.Fatal("generic credential exposed instance scope", err)
				}
				if _, raw, err := s.ObservationCredential(t.Context(), c.OrgID, c.ID); !errors.Is(err, ErrChanged) || len(raw) != 0 {
					t.Fatal("generic observation exposed instance scope", err)
				}
				if raw, err := s.managedCredential(t.Context(), &c); !errors.Is(err, ErrChanged) || len(raw) != 0 {
					t.Fatal("managed helper exposed instance scope", err)
				}
				if r.calls != 0 || secretReads.reads != 0 {
					t.Fatal("instance marker reached credential material")
				}
			})
		}
	}
}

type instanceBoundaryResolver struct {
	calls  int
	change func(Connection)
}

func (r *instanceBoundaryResolver) ResolveManagedCredential(_ context.Context, c Connection) (ManagedCredential, error) {
	r.calls++
	if r.change != nil {
		r.change(c)
	}
	return ManagedCredential{Kubeconfig: []byte("runtime"), ExpiresAt: time.Now().Add(time.Hour), TrustRevision: c.Generation}, nil
}
func (r *instanceBoundaryResolver) ResolveManagedCredentialForObservation(ctx context.Context, c Connection) (ManagedCredential, error) {
	return r.ResolveManagedCredential(ctx, c)
}

func TestInstanceBoundaryPreservesOrdinaryCredentials(t *testing.T) {
	s, store, _, _ := inClusterRig(t, false)
	c := Connection{ID: "ordinary", OrgID: orgPrincipal.OrgID, Cloud: Kubernetes, Method: MethodManagedKubernetes, Generation: 1, Status: StatusReady}
	store.rows[c.ID] = c
	r := &instanceBoundaryResolver{}
	s.ManagedCredentials = r
	if _, raw, err := s.Credential(t.Context(), c.OrgID, c.ID); err != nil || string(raw) != "runtime" {
		t.Fatal(err)
	}
	if _, raw, err := s.ObservationCredential(t.Context(), c.OrgID, c.ID); err != nil || string(raw) != "runtime" {
		t.Fatal(err)
	}
	if r.calls != 2 {
		t.Fatal("ordinary credential behavior changed", r.calls)
	}
}

func TestInstanceCredentialMarkerAppearsDuringResolution(t *testing.T) {
	for _, marker := range []string{"managed_instance_id", "managed_environment_id"} {
		for _, mode := range []string{"replace", "in-place"} {
			for _, entry := range []string{"credential", "observation"} {
				t.Run(entry+"/"+marker+"/"+mode, func(t *testing.T) {
					s, store, _, _ := inClusterRig(t, false)
					c := Connection{ID: "ordinary", OrgID: orgPrincipal.OrgID, Cloud: Kubernetes, Method: MethodManagedKubernetes, Generation: 1, Status: StatusReady, Details: map[string]string{"managed_attachment_id": "attachment"}}
					store.rows[c.ID] = c
					s.ManagedCredentials = &instanceBoundaryResolver{change: func(resolved Connection) {
						current := store.rows[c.ID]
						if mode == "replace" {
							current.Details = map[string]string{}
						} else {
							current.Details = resolved.Details
						}
						current.Details[marker] = "isolated"
						store.rows[c.ID] = current
					}}
					resolve := s.Credential
					if entry == "observation" {
						resolve = s.ObservationCredential
					}
					if _, raw, err := resolve(t.Context(), c.OrgID, c.ID); !errors.Is(err, ErrChanged) || len(raw) != 0 {
						t.Fatal("scope changed during credential resolution", err)
					}
				})
			}
		}
	}
}

type instanceBoundarySecrets struct {
	*memSecrets
	reads int
}

func (s *instanceBoundarySecrets) Reveal(ctx context.Context, org, id string) ([]byte, error) {
	s.reads++
	return s.memSecrets.Reveal(ctx, org, id)
}
