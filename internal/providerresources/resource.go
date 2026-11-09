// Package providerresources records provider inventory without taking ownership
// of the databases, caches or buckets it discovers. Credentials stay in Vault.
package providerresources

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

var (
	ErrNotFound   = errors.New("provider resource not found")
	ErrChanged    = errors.New("the provider resource or account changed; sync and review it again")
	ErrInvalid    = errors.New("invalid provider resource")
	ErrDiscovery  = errors.New("resource discovery failed; check the account permissions and retry")
	ErrUnverified = errors.New("the resource identity or connection needs verification")
	// ErrSyncRunning is what a background sync answers when the account is
	// already being synced: it leaves that sync alone rather than fence it.
	ErrSyncRunning = errors.New("a sync of this account is already running")
)

// ProviderResource contains public observations. Its endpoint ID references a
// Vault-backed catalog endpoint and never carries its access secret.
type ProviderResource struct {
	APIAccountID          string                     `json:"api_account_id,omitempty"`
	AttachmentNote        string                     `json:"attachment_note,omitempty"`
	IdentityScope         string                     `json:"identity_scope"`
	BucketOwner           string                     `json:"bucket_owner,omitempty"`
	OwnershipState        string                     `json:"ownership_state"`
	Consumers             []ProviderResourceConsumer `json:"consumers"`
	CredentialMethods     []string                   `json:"credential_methods"`
	EndpointProofRequired bool                       `json:"endpoint_proof_required"`
	ID                    string                     `json:"id"`
	OrgID                 string                     `json:"org_id"`
	Provider              string                     `json:"provider"`
	ConnectionIdentity    string                     `json:"connection_identity"`
	AccountIdentity       string                     `json:"account_identity"`
	ConnectionID          string                     `json:"connection_id"`
	ConnectionGeneration  int64                      `json:"connection_generation"`
	ExternalID            string                     `json:"external_id"`
	Incarnation           string                     `json:"incarnation"`
	Scope                 string                     `json:"scope"`
	Name                  string                     `json:"name"`
	Kind                  string                     `json:"kind"`
	Version               string                     `json:"version"`
	Region                string                     `json:"region"`
	Status                string                     `json:"status"`
	Host                  string                     `json:"host"`
	Database              string                     `json:"database"`
	Port                  int                        `json:"port"`
	Ownership             string                     `json:"ownership"`
	EndpointID            string                     `json:"endpoint_id"`
	EndpointRevision      int                        `json:"endpoint_revision"`
	Revision              int64                      `json:"revision"`
	ObservedAt            time.Time                  `json:"observed_at"`
	ObservationState      string                     `json:"observation_state"`
	ConnectionState       string                     `json:"connection_state"`
}

// ProviderResourceSync retains coverage and safe outcome facts for one attempt.
type ProviderResourceSync struct {
	RefreshOptIn         bool       `json:"refresh_opt_in"`
	ConnectionIdentity   string     `json:"connection_identity"`
	RequestedBy          string     `json:"requested_by"`
	RequestedKeyID       string     `json:"requested_key_id,omitempty"`
	ID                   string     `json:"id"`
	OrgID                string     `json:"org_id"`
	ConnectionID         string     `json:"connection_id"`
	Provider             string     `json:"provider"`
	AccountIdentity      string     `json:"account_identity"`
	ConnectionGeneration int64      `json:"connection_generation"`
	Sequence             int64      `json:"sequence"`
	Scope                string     `json:"scope"`
	Status               string     `json:"status"`
	Error                string     `json:"error"`
	StartedAt            time.Time  `json:"started_at"`
	FinishedAt           *time.Time `json:"finished_at"`
}

type ProviderResourceList struct {
	Resources          []*ProviderResource     `json:"resources"`
	Syncs              []*ProviderResourceSync `json:"syncs"`
	SupportedProviders []string                `json:"supported_providers"`
}

// ProviderResourceConnectInput carries the write-only application connection.
type ProviderResourceConnectInput struct {
	ProviderProjectID   string                       `json:"provider_project_id,omitempty"`
	ProviderEnvironment string                       `json:"provider_environment,omitempty"`
	APIAccess           *catalogconnect.APIAccess    `json:"api_access,omitempty"`
	DSN                 string                       `json:"dsn,omitempty"`
	BucketAccess        *catalogconnect.BucketAccess `json:"bucket_access,omitempty"`
}

// ConnectInput keeps the service call contract short.
type ConnectInput = ProviderResourceConnectInput

// AccountState is updated under an account-wide transaction lock. Sequence
// fences every access path to the same verified account, including duplicates.
type AccountState struct {
	Sequence  int64
	Resources []*ProviderResource
	Syncs     []*ProviderResourceSync
}

type Store interface {
	List(context.Context, string) (*ProviderResourceList, error)
	Get(context.Context, string, string) (*ProviderResource, error)
	// Update checks the optional connection generation under the same transaction
	// that publishes observations. Returning an error rolls back all changes.
	Update(context.Context, string, string, string, string, int64, func(*AccountState) error) error
}

type Connections interface {
	Credential(context.Context, string, string) (*connections.Connection, []byte, error)
	All(context.Context, string) ([]*connections.Connection, error)
}

type Reader interface {
	Supports(string) bool
	Inventory(context.Context, *connections.Connection, []byte) (provider.ResourceInventory, error)
}

type Endpoints interface {
	Create(context.Context, security.Principal, catalogconnect.CreateInput) (*catalogconnect.Endpoint, error)
	Delete(context.Context, security.Principal, string) error
	Get(context.Context, string, string) (*catalogconnect.Endpoint, error)
	Resolve(context.Context, string, string) (*catalogconnect.Endpoint, string, error)
}

type Authorizer interface {
	Require(context.Context, security.Principal, security.Permission, string) error
}

type Deps struct {
	Store              Store
	Connections        Connections
	Reader             Reader
	Endpoints          Endpoints
	Authorizer         Authorizer
	Organizations      func(context.Context) ([]string, error)
	SupportedProviders []string
	Usage              func(context.Context, string) ([]ResourceBinding, error)
}

// ResourceBinding is a secret-free projection of a persisted deployment binding.
// OwnershipVerified is set only after its lifecycle receipt has been checked.
type ResourceBinding struct {
	Revision            int64
	EndpointID          string
	EndpointRevision    int
	ID                  string
	ProjectID           string
	EnvironmentID       string
	Name                string
	Provider            string
	ConnectionID        string
	AccountIdentity     string
	ExternalID          string
	Incarnation         string
	InventoryResourceID string
	OwnershipVerified   bool
}

// ProviderResourceConsumer identifies a deployment's recorded use of a resource.
type ProviderResourceConsumer struct {
	ID            string `json:"id"`
	BindingID     string `json:"binding_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Name          string `json:"name"`
}
