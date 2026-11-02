package deployments

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/xraph/vault/secret"

	"github.com/xraph/loomplane/internal/recovery"
)

// NativeCopySecrets publishes a version under the same database fence used by
// native cleanup. Vault's encryption and version history remain unchanged.
type NativeCopySecrets interface {
	VersionedResourceSecrets
	PutNativeVersion(context.Context, recovery.NativeRestore, []byte) (int64, error)
}

func (v *VaultResourceSecrets) PutNativeVersion(ctx context.Context, r recovery.NativeRestore, value []byte) (int64, error) {
	if r.ID == "" || r.OrgID == "" || r.EnvironmentID == "" || r.Token == "" {
		return 0, recovery.ErrInvalid
	}
	return v.putVersion(ctx, nativeCopySecretKey(r), value, secret.WithMetadata(map[string]string{
		"native_restore_id": r.ID, "native_org_id": r.OrgID, "native_environment_id": r.EnvironmentID, "native_worker_token_hash": fmt.Sprintf("%x", sha256.Sum256([]byte(r.Token))),
	}))
}
