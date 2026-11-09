package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestManagedClusterCredentialPrivate(t *testing.T) {
	c := ManagedCredential{Kubeconfig: []byte("runtime-secret"), ExpiresAt: time.Now()}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{string(raw), fmt.Sprintf("%v %+v %#v", c, c, c)} {
		if strings.Contains(out, "runtime-secret") {
			t.Fatal("credential escaped")
		}
	}
}

type managedResolverFunc func(context.Context, Connection) (ManagedCredential, error)

func (f managedResolverFunc) ResolveManagedCredential(ctx context.Context, c Connection) (ManagedCredential, error) {
	return f(ctx, c)
}
func TestManagedClusterRenewalPreservesGeneration(t *testing.T) {
	s, store, secrets, _ := inClusterRig(t, false)
	c := Connection{ID: "managed", OrgID: orgPrincipal.OrgID, Cloud: Kubernetes, Method: MethodManagedKubernetes, Generation: 7, Status: StatusReady, Details: map[string]string{"namespace_uid": "uid", "managed_attachment_id": "attachment", "managed_project_id": "project"}}
	store.rows[c.ID] = c
	secrets.kept[c.OrgID+"/"+c.ID] = []byte("bootstrap-must-not-escape")
	version := int64(1)
	s.ManagedCredentials = managedResolverFunc(func(context.Context, Connection) (ManagedCredential, error) {
		version++
		return ManagedCredential{Kubeconfig: []byte("runtime"), ExpiresAt: time.Now().Add(time.Hour), TrustRevision: 7, CredentialVersion: version}, nil
	})
	for range 2 {
		current, raw, err := s.Credential(t.Context(), c.OrgID, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "runtime" || current.Generation != 7 {
			t.Fatal("renewal changed placement or leaked bootstrap")
		}
	}
	if _, err := s.Replace(t.Context(), orgPrincipal, c.ID, ReplaceInput{Credential: "replace"}); !errors.Is(err, ErrInUse) {
		t.Fatalf("replace bypass: %v", err)
	}
	if err := s.Delete(t.Context(), orgPrincipal, c.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete bypass: %v", err)
	}
}
func TestManagedClusterParentReplacement(t *testing.T) {
	s, store, _, _ := inClusterRig(t, false)
	c := Connection{ID: "managed", OrgID: orgPrincipal.OrgID, Cloud: Kubernetes, Method: MethodManagedKubernetes, Generation: 1, Status: StatusReady}
	store.rows[c.ID] = c
	s.ManagedCredentials = managedResolverFunc(func(context.Context, Connection) (ManagedCredential, error) { return ManagedCredential{}, ErrChanged })
	if _, raw, err := s.Credential(t.Context(), c.OrgID, c.ID); !errors.Is(err, ErrChanged) || len(raw) != 0 {
		t.Fatal("revoked parent handed out credentials")
	}
	s.ManagedCredentials = managedResolverFunc(func(context.Context, Connection) (ManagedCredential, error) {
		changed := store.rows[c.ID]
		changed.Generation++
		store.rows[c.ID] = changed
		return ManagedCredential{Kubeconfig: []byte("runtime"), ExpiresAt: time.Now().Add(time.Hour), TrustRevision: 1}, nil
	})
	if _, raw, err := s.Credential(t.Context(), c.OrgID, c.ID); !errors.Is(err, ErrChanged) || len(raw) != 0 {
		t.Fatal("replacement race handed out credentials")
	}
}
func TestManagedClusterCrossProjectAttachment(t *testing.T) {
	c := &Connection{Method: MethodManagedKubernetes, Details: map[string]string{"managed_project_id": "one"}}
	if ManagedProjectAllowed(c, "two") || ManagedProjectAllowed(c, "") || !ManagedProjectAllowed(c, "one") {
		t.Fatal("project boundary failed")
	}
}
