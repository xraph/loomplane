package cloudinfra

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"maps"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
)

type managedRegistrationSecrets struct {
	connections.Secrets
	values map[string][]byte
}

func (s *managedRegistrationSecrets) Reveal(_ context.Context, _, key string) ([]byte, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return nil, connections.ErrNotFound
}
func (s *managedRegistrationSecrets) Put(_ context.Context, _, key string, value []byte) error {
	s.values[key] = value
	return nil
}

type managedRegistrationConnections struct {
	connections.Store
	saved *connections.Connection
}

func (s *managedRegistrationConnections) CreateConnection(_ context.Context, c *connections.Connection) error {
	s.saved = c
	return nil
}

type managedRegistrationStore struct{ Store }

func (*managedRegistrationStore) SaveServer(context.Context, *Server) error { return nil }

type managedRegistrationRuntime struct{}

func (managedRegistrationRuntime) Inspect(context.Context, []byte, map[string]string, bool) (RuntimeHealth, error) {
	return RuntimeHealth{DockerID: "managed-daemon", Architecture: "amd64"}, nil
}

func TestManagedRuntimeRegistrationStoresCanonicalSettings(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	operation := &Operation{ID: "operation", OrgID: "org", ServerID: "server_fixture", PublicIPv4: []string{"192.0.2.10"}, HostKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Input: ServerInput{Kind: "create"}}
	secrets := &managedRegistrationSecrets{values: map[string][]byte{}}
	connectionsStore := &managedRegistrationConnections{}
	service := NewService(Deps{Store: &managedRegistrationStore{}, Secrets: secrets, RuntimeConnections: connectionsStore, Runtime: managedRegistrationRuntime{}, EnrollmentURL: "https://enroll.example.com"})
	if _, err = service.prepareBootstrap(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	managed := &Server{ID: operation.ServerID, OrgID: operation.OrgID}
	if err = service.readyRuntime(t.Context(), operation, managed); err != nil {
		t.Fatal(err)
	}
	stored := connectionsStore.saved
	if stored == nil {
		t.Fatal("runtime connection was not registered")
	}
	raw, err := secrets.Reveal(t.Context(), operation.OrgID, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	config, err := server.ParseConfig(raw, stored.Details)
	if err != nil {
		t.Fatal(err)
	}
	expected := maps.Clone(config.Details)
	expected["docker_id"] = "managed-daemon"
	expected["architecture"] = "amd64"
	if !maps.Equal(stored.Details, expected) {
		t.Fatal("registered runtime settings differ from the canonical settings used by runtime checks")
	}
}
