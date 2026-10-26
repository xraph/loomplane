package fabriqloom

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
)

// KeyState is where a consuming service's Fabriq key is in its life.
type KeyState string

// A key is issued pending (by a rotation) or active (by a release), and a
// service has at most one of each. The release that promotes a pending key,
// or issues a new one, retires the active one; it stays valid in Fabriq
// until the release has written the service's binding, and is revoked then.
const (
	KeyPending  KeyState = "pending"
	KeyActive   KeyState = "active"
	KeyRetiring KeyState = "retiring"
	KeyRevoked  KeyState = "revoked"
)

var (
	// ErrConflict is a second pending or active key for one service.
	ErrConflict = errors.New("the service already has a key in that state")
	// ErrStateChanged is a key that isn't in any of the states a change
	// expected, or isn't there.
	ErrStateChanged = errors.New("the key's state changed")
)

// KeyRecord is what Loomplane keeps of a Fabriq key it issued to a
// consuming service. ID is Fabriq's key id; the plaintext is in Vault under
// VaultKey.
type KeyRecord struct {
	ID, OrgID, ProjectID, EnvironmentID, Resource, Service, Tenant, Prefix string
	State                                                                  KeyState
	CreatedAt, UpdatedAt                                                   time.Time
	RevokedAt                                                              *time.Time
}

// KeyStore keeps KeyRecords. Lists are newest first.
type KeyStore interface {
	// LockService serializes remote key issuance and local bookkeeping across
	// releases, rotations and revocations. The caller must release the lock.
	LockService(ctx context.Context, orgID, environmentID, resource, service string) (release func(), err error)
	// Insert records a key. A second pending or active key for the same
	// (organization, environment, resource, service) is ErrConflict.
	Insert(ctx context.Context, r *KeyRecord) error
	// SetState moves a key from one of the states from to to. A key in
	// none of them is ErrStateChanged; one that would be a second pending
	// or active key is ErrConflict.
	SetState(ctx context.Context, orgID, id string, from []KeyState, to KeyState) error
	ForService(ctx context.Context, orgID, environmentID, resource, service string) ([]*KeyRecord, error)
	ForResource(ctx context.Context, orgID, environmentID, resource string) ([]*KeyRecord, error)
}

// keyLabelPrefix starts the label of every key Loomplane issues.
const keyLabelPrefix = "loomplane:"

// KeyLabel is the label of the keys Loomplane issues to service. Keys with
// any other label are never touched.
func KeyLabel(service string) string { return keyLabelPrefix + service }

// VaultKey is where a key's plaintext lives in ResourceSecrets.
func VaultKey(orgID, environmentID, resource, id string) string {
	return deployments.ResourceKey(orgID, environmentID, resource, "fabriq-key:"+id)
}
