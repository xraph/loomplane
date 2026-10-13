package connections

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"reflect"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/clouds/server"
)

func infrastructureKey(t *testing.T) ([]byte, ssh.PublicKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block), signer.PublicKey()
}

func TestInfrastructureChecker_credentialRotationPreservesIdentity(t *testing.T) {
	first, pub := infrastructureKey(t)
	second, _ := infrastructureKey(t)
	settings := map[string]string{"host": "server.example.com", "user": "deploy", "host_key": string(ssh.MarshalAuthorizedKey(pub))}
	a, err := (ServerChecker{}).Parse(first, settings)
	if err != nil {
		t.Fatal(err)
	}
	b, err := (ServerChecker{}).Parse(second, settings)
	if err != nil {
		t.Fatal(err)
	}
	if a.Account != b.Account || !reflect.DeepEqual(a.Details, b.Details) {
		t.Fatalf("key rotation changed placement: %+v %+v", a, b)
	}
	registrySettings := map[string]string{"endpoint": "https://registry.example.com", "repository": "team"}
	a, err = (RegistryChecker{}).Parse([]byte(`{"username":"deploy","password":"first"}`), registrySettings)
	if err != nil {
		t.Fatal(err)
	}
	b, err = (RegistryChecker{}).Parse([]byte(`{"username":"deploy","password":"second"}`), registrySettings)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("registry rotation changed placement: %+v %+v", a, b)
	}
}

func TestInfrastructureChecker_refusalsAndMalformedCredentials(t *testing.T) {
	for _, err := range []error{server.ErrRejected, kubernetes.ErrRejected, registry.ErrRejected} {
		if !rejected(err) {
			t.Fatalf("refusal classified as outage: %v", err)
		}
	}
	for _, checker := range []Checker{ServerChecker{}, KubernetesChecker{}, RegistryChecker{}} {
		if _, err := checker.Parse([]byte("not a credential"), nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid credential: %v", err)
		}
	}
}
