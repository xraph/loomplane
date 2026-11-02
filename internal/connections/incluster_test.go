package connections

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
)

// clusterChecker is a Kubernetes checker that keeps the credentials it was
// given, and refuses a pasted grant as the real one does.
type clusterChecker struct {
	KubernetesChecker
	got [][]byte
}

func (*clusterChecker) Parse([]byte, map[string]string) (Check, error) {
	return Check{Account: kubernetes.InClusterContext + "/apps", Details: map[string]string{"namespace": "apps", "namespace_uid": "uid-apps"}}, nil
}

func (c *clusterChecker) Check(_ context.Context, raw []byte, settings map[string]string) (Check, error) {
	c.got = append(c.got, append([]byte(nil), raw...))
	return c.Parse(raw, settings)
}

func inClusterRig(t *testing.T, on bool) (*Service, *memStore, *memSecrets, *clusterChecker) {
	t.Helper()
	if on {
		kubernetes.UseInCluster(&kubernetes.InCluster{Host: "https://10.96.0.1:443", Namespace: "loomplane"})
		t.Cleanup(func() { kubernetes.UseInCluster(nil) })
	} else {
		kubernetes.UseInCluster(nil)
	}
	store, secrets, checker := &memStore{rows: map[string]Connection{}}, &memSecrets{kept: map[string][]byte{}}, &clusterChecker{}
	return NewService(store, secrets, map[string]Checker{Kubernetes: checker}, identityAuthz{}, nil, nil), store, secrets, checker
}

var orgPrincipal = security.Principal{OrgID: "org_1", UserID: "user_1"}

func TestCreateInCluster_refusedUnlessTheOperatorTurnedItOn(t *testing.T) {
	s, store, _, checker := inClusterRig(t, false)
	_, err := s.CreateInCluster(t.Context(), orgPrincipal, InClusterInput{Name: "This cluster", Settings: map[string]string{"namespace": "apps"}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "doesn't connect the cluster it runs in") {
		t.Fatalf("got %v", err)
	}
	if len(store.rows) != 0 || len(checker.got) != 0 {
		t.Fatal("an in-cluster connection was checked or kept with the feature off")
	}
}

func TestCreateInCluster_keepsNothingInVault(t *testing.T) {
	s, store, secrets, checker := inClusterRig(t, true)
	c, err := s.CreateInCluster(t.Context(), orgPrincipal, InClusterInput{Name: "This cluster", Settings: map[string]string{"namespace": "apps"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != MethodInCluster || c.Cloud != Kubernetes || c.Status != StatusReady || c.SignedInBy != "" {
		t.Fatalf("wrong connection: %+v", c)
	}
	if secrets.puts != 0 || len(secrets.kept) != 0 {
		t.Fatal("an in-cluster connection wrote to Vault")
	}
	if len(checker.got) != 1 || !kubernetes.IsInClusterCredential(checker.got[0]) {
		t.Fatalf("the check didn't get the in-cluster credential: %q", checker.got)
	}
	if _, ok := store.rows[c.ID]; !ok {
		t.Fatal("connection not stored")
	}
}

func TestInCluster_credentialAndRecheckNeverReadVault(t *testing.T) {
	s, _, secrets, checker := inClusterRig(t, true)
	c, err := s.CreateInCluster(t.Context(), orgPrincipal, InClusterInput{Name: "This cluster", Settings: map[string]string{"namespace": "apps"}})
	if err != nil {
		t.Fatal(err)
	}
	// Anything Vault might hold under the ID is never what's handed out.
	secrets.kept["org_1/"+c.ID] = []byte("stale kubeconfig")
	_, cred, err := s.Credential(t.Context(), "org_1", c.ID)
	if err != nil || !kubernetes.IsInClusterCredential(cred) {
		t.Fatalf("credential %q, %v", cred, err)
	}
	if _, err := s.Recheck(t.Context(), orgPrincipal, c.ID); err != nil {
		t.Fatal(err)
	}
	if last := checker.got[len(checker.got)-1]; !kubernetes.IsInClusterCredential(last) {
		t.Fatalf("recheck used %q", last)
	}
}

func TestInCluster_pastedCredentialIsRefused(t *testing.T) {
	s, store, secrets, checker := inClusterRig(t, true)
	_, err := s.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Kubernetes, Name: "Borrowed", Credential: string(kubernetes.InClusterCredential()), Settings: map[string]string{"namespace": "apps"}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "self-contained kubeconfig") {
		t.Fatalf("pasted in-cluster credential: %v", err)
	}
	if len(store.rows) != 0 || secrets.puts != 0 || len(checker.got) != 0 {
		t.Fatal("a pasted in-cluster credential got as far as a check")
	}
	c, err := s.CreateInCluster(t.Context(), orgPrincipal, InClusterInput{Name: "This cluster", Settings: map[string]string{"namespace": "apps"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(t.Context(), orgPrincipal, c.ID, ReplaceInput{Credential: string(kubernetes.InClusterCredential())}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("replacing with the in-cluster credential: %v", err)
	}
}
