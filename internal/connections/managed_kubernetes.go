package connections

import (
	"context"
	"fmt"
	"io"
	"time"
)

const MethodManagedKubernetes = "managed_kubernetes"

// ManagedCredential carries runtime material only inside the process.
type ManagedCredential struct {
	Kubeconfig        []byte    `json:"-"`
	ExpiresAt         time.Time `json:"-"`
	TrustRevision     int64     `json:"-"`
	CredentialVersion int64     `json:"-"`
}

func (ManagedCredential) String() string     { return "[managed Kubernetes credential]" }
func (c ManagedCredential) GoString() string { return c.String() }
func (c ManagedCredential) Format(s fmt.State, _ rune) {
	if _, writeErr := io.WriteString(s, c.String()); writeErr != nil {
		return
	}
}

type ManagedCredentialResolver interface {
	ResolveManagedCredential(context.Context, Connection) (ManagedCredential, error)
}

func (s *Service) managedCredential(ctx context.Context, c *Connection) ([]byte, error) {
	if s.ManagedCredentials == nil || c.Cloud != Kubernetes {
		return nil, ErrNotReady
	}
	credential, err := s.ManagedCredentials.ResolveManagedCredential(ctx, *c)
	if err != nil {
		return nil, err
	}
	if len(credential.Kubeconfig) == 0 || !credential.ExpiresAt.After(s.now().Add(time.Minute)) || credential.TrustRevision != c.Generation {
		return nil, ErrNotReady
	}
	return credential.Kubeconfig, nil
}

// ManagedProjectAllowed keeps a managed namespace inside its approved project.
func ManagedProjectAllowed(c *Connection, project string) bool {
	return c != nil && (c.Method != MethodManagedKubernetes && c.Details["managed_attachment_id"] == "" || project != "" && c.Details["managed_project_id"] == project)
}
