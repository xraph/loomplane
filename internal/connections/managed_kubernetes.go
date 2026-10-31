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
	Kubeconfig         []byte    `json:"-"`
	ExpiresAt          time.Time `json:"-"`
	TrustRevision      int64     `json:"-"`
	CredentialVersion  int64     `json:"-"`
	AttachmentRevision int64     `json:"-"`
	AccessRevision     int64     `json:"-"`
	HPA                bool      `json:"-"`
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

// CleanupCredential only accepts a ready managed namespace connection. Cleanup
// does not require a registry pull grant.
func (s *Service) CleanupCredential(ctx context.Context, org, id, project, attachment string, generation int64) (*Connection, ManagedCredential, error) {
	var empty ManagedCredential
	c, err := s.store.GetConnection(ctx, org, id)
	if err != nil {
		return nil, empty, err
	}
	valid := func(c *Connection) bool {
		return c != nil && c.OrgID == org && c.ID == id && c.Cloud == Kubernetes && c.Method == MethodManagedKubernetes && c.Status == StatusReady && c.Message != replacementInProgressMessage && c.Generation == generation && c.Details["managed_attachment_id"] == attachment && ManagedProjectAllowed(c, project)
	}
	if !valid(c) || s.ManagedCredentials == nil {
		return nil, empty, ErrChanged
	}
	cred, err := s.ManagedCredentials.ResolveManagedCredential(ctx, *c)
	if err != nil {
		return nil, empty, err
	}
	if len(cred.Kubeconfig) == 0 || !cred.ExpiresAt.After(s.now().Add(time.Minute)) || cred.TrustRevision != generation {
		return nil, empty, ErrNotReady
	}
	current, err := s.store.GetConnection(ctx, org, id)
	if err != nil {
		return nil, empty, err
	}
	if !valid(current) {
		return nil, empty, ErrChanged
	}
	return current, cred, nil
}
