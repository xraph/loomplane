package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/config"
	cloudconn "github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// bitbucketSecrets keeps Bitbucket refresh tokens in Vault, and says
// repositories.ErrBitbucketSecretMissing for one that's gone.
type bitbucketSecrets struct {
	*cloudconn.VaultSecrets
}

var _ repositories.Secrets = bitbucketSecrets{}

// Reveal implements repositories.Secrets.
func (s bitbucketSecrets) Reveal(ctx context.Context, orgID, connectionID string) ([]byte, error) {
	raw, err := s.VaultSecrets.Reveal(ctx, orgID, connectionID)
	if errors.Is(err, cloudconn.ErrNotFound) {
		return nil, repositories.ErrBitbucketSecretMissing
	}
	return raw, err
}

// bitbucketConnections builds the organization Bitbucket connections, over
// the configured OAuth consumer or none. A Loomplane without one still
// builds them, so public repositories are read the same way.
func bitbucketConnections(cfg config.Config, db *grove.DB, authz repositories.Authorizer, audit repositories.BitbucketAuditor, log forge.Logger) (*repositories.BitbucketConnections, error) {
	stateKey, err := security.DeriveKey(cfg.SecretKey, repositories.BitbucketStateKeyLabel)
	if err != nil {
		return nil, err
	}
	vaultKey, err := security.DeriveKey(cfg.SecretKey, repositories.BitbucketVaultKeyLabel)
	if err != nil {
		return nil, err
	}
	enc, err := crypto.NewEncryptor(vaultKey)
	if err != nil {
		return nil, fmt.Errorf("vault encryptor: %w", err)
	}
	svc := secret.NewService(vaultpg.New(db), enc, secret.WithAppID(repositories.BitbucketVaultAppID))
	secrets := bitbucketSecrets{cloudconn.NewVaultSecretsFor(svc, repositories.BitbucketVaultAppID)}
	// An interface holding a nil *BitbucketOAuth isn't nil, so build it only
	// when there is a consumer.
	var api repositories.BitbucketAPI
	if cfg.Bitbucket.Configured() {
		api = repositories.NewBitbucketOAuth(cfg.Bitbucket)
	}
	conns := repositories.NewBitbucketConnections(repositories.NewPgStore(pgdriver.Unwrap(db)), api, secrets, authz, audit, stateKey)
	conns.SetWarn(func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) })
	return conns, nil
}

// bitbucketAuditor records Bitbucket workspace connections in Chronicle,
// in the organization's stream. It never sees a token.
type bitbucketAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

var _ repositories.BitbucketAuditor = bitbucketAuditor{}

// RecordBitbucket implements repositories.BitbucketAuditor.
func (a bitbucketAuditor) RecordBitbucket(ctx context.Context, action string, p security.Principal, c repositories.BitbucketConnection) {
	b := a.chronicle.Info(ctx, action, "bitbucket_workspace", c.ID).
		Category("integrations").AppID(a.appID).TenantID(p.OrgID).UserID(p.UserID).Outcome("success")
	if c.WorkspaceSlug != "" {
		b = b.Meta("workspace", c.WorkspaceSlug)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
