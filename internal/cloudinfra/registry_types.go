package cloudinfra

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type RegistryDNS struct {
	ZoneID       string              `json:"zone_id"`
	ZoneRevision int64               `json:"zone_revision"`
	Desired      provider.DNSRecord  `json:"desired"`
	Previous     *provider.DNSRecord `json:"previous,omitempty"`
	Receipt      *provider.DNSRecord `json:"receipt,omitempty"`
	Attempted    bool                `json:"attempted"`
}

type RegistryBackup struct {
	ID           string             `json:"id"`
	CreatedAt    time.Time          `json:"created_at"`
	Object       backupfiles.Object `json:"object"`
	Hostname     string             `json:"hostname"`
	NamespaceUID string             `json:"namespace_uid"`
}

type RegistryAction struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Phase       string    `json:"phase"`
	StartedAt   time.Time `json:"started_at"`
	BackupID    string    `json:"backup_id,omitempty"`
	RemoveSetup bool      `json:"remove_setup,omitempty"`
	DeleteData  bool      `json:"delete_data,omitempty"`
}

type RegistryState struct {
	Authority    security.Principal `json:"authority"`
	OrgID        string             `json:"org_id"`
	ConnectionID string             `json:"connection_id"`
	Generation   int64              `json:"generation"`
	Identity     string             `json:"identity"`
	Hostname     string             `json:"hostname"`
	Status       string             `json:"status"`
	Message      string             `json:"message"`
	DNS          *RegistryDNS       `json:"dns,omitempty"`
	Action       *RegistryAction    `json:"action,omitempty"`
	Backups      []RegistryBackup   `json:"backups"`
	LastRotation time.Time          `json:"last_rotation"`
	LastCleanup  time.Time          `json:"last_cleanup"`
	LastUpgrade  time.Time          `json:"last_upgrade"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

type RegistryActionInput struct {
	Kind        string `json:"kind"`
	Generation  int64  `json:"generation"`
	Hostname    string `json:"hostname"`
	BackupID    string `json:"backup_id,omitempty"`
	RemoveSetup bool   `json:"remove_setup,omitempty"`
	DeleteData  bool   `json:"delete_data,omitempty"`
}

type RegistryStore interface {
	RegistryState(context.Context, string, string) (*RegistryState, error)
	SaveRegistryState(context.Context, *RegistryState) error
	RegistryCandidates(context.Context) ([]*connections.Connection, error)
	WithOperationLock(context.Context, string, func() error) error
	RegistryInUse(context.Context, string, string) (bool, error)
	RegistrySetupInUse(context.Context, *connections.Connection) (bool, error)
}

func (s *Service) registryStore() (RegistryStore, error) {
	st, ok := s.Store.(RegistryStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st, nil
}
