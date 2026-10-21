package access

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/secureconfig"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrSigningKeys = errors.New("invalid federation signing key configuration")

const maxSigningKeyFile = 256 << 10
const maxSigningScopes = 256

// SigningKeyring holds explicit signing authority for each configured scope.
// Reloading requires a restart. Keys are never generated on the request path.
type SigningKeyring struct {
	signers map[domain.Scope]*SnapshotSigner
}

type signingKeyFile struct {
	Version int                `json:"version"`
	Keys    []signingKeyRecord `json:"keys"`
}
type signingKeyRecord struct {
	OrgID         string    `json:"org_id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID string    `json:"environment_id"`
	PrivateKey    string    `json:"private_key"`
	NotBefore     time.Time `json:"not_before"`
	NotAfter      time.Time `json:"not_after"`
}

// LoadSigningKeyring accepts only a bounded, private regular file. Errors never
// include its contents, including malformed key material or JSON field names.
func LoadSigningKeyring(path, org string) (*SigningKeyring, error) {
	var config signingKeyFile
	if !domain.ValidIdentitySegment(org) || secureconfig.ReadJSON(path, maxSigningKeyFile, true, &config) != nil || config.Version != 1 || len(config.Keys) == 0 || len(config.Keys) > maxSigningScopes {
		return nil, ErrSigningKeys
	}
	ring := &SigningKeyring{signers: make(map[domain.Scope]*SnapshotSigner, len(config.Keys))}
	now := time.Now()
	for _, record := range config.Keys {
		scope := domain.Scope{OrgID: record.OrgID, ProjectID: record.ProjectID, EnvironmentID: record.EnvironmentID}
		if scope.OrgID != org || ring.signers[scope] != nil {
			return nil, ErrSigningKeys
		}
		private, decodeErr := base64.StdEncoding.Strict().DecodeString(record.PrivateKey)
		if decodeErr != nil {
			return nil, ErrSigningKeys
		}
		signer, signErr := NewSnapshotSigner(scope, private, record.NotBefore, record.NotAfter)
		clear(private)
		if signErr != nil || !signer.key.active(now) {
			return nil, ErrSigningKeys
		}
		ring.signers[scope] = signer
	}
	return ring, nil
}

// Signer refuses unconfigured scopes and keys that expired after startup.
func (k *SigningKeyring) Signer(ctx context.Context, scope domain.Scope) (*SnapshotSigner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if k == nil {
		return nil, ErrSigningKeys
	}
	signer := k.signers[scope]
	if signer == nil || !signer.key.active(time.Now()) {
		return nil, ErrSigningKeys
	}
	return signer, nil
}

// Health reports expired configuration even when no agent currently requests it.
func (k *SigningKeyring) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k == nil || len(k.signers) == 0 {
		return ErrSigningKeys
	}
	for scope := range k.signers {
		if _, err := k.Signer(ctx, scope); err != nil {
			return err
		}
	}
	return nil
}
