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
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// bitbucketSecrets keeps Bitbucket refresh tokens in Vault, and says
// repositories.ErrBitbucketSecretMissing for one that's gone. Secrets is
// Vault's, a *cloudconn.VaultSecrets.
type bitbucketSecrets struct {
	repositories.Secrets
}

var _ repositories.Secrets = bitbucketSecrets{}

// Reveal implements repositories.Secrets.
func (s bitbucketSecrets) Reveal(ctx context.Context, orgID, connectionID string) ([]byte, error) {
	raw, err := s.Secrets.Reveal(ctx, orgID, connectionID)
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
		api = bitbucketOAuth(cfg.Bitbucket)
	}
	conns := repositories.NewBitbucketConnections(repositories.NewPgStore(pgdriver.Unwrap(db)), api, secrets, authz, audit, stateKey)
	conns.SetWarn(func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) })
	return conns, nil
}

// bitbucketWebhooks builds the push webhooks on Bitbucket repositories.
// Their secrets come from a key derived for nothing else.
func bitbucketWebhooks(cfg config.Config, pg *pgdriver.PgDB, conns *repositories.BitbucketConnections, authz discovery.Authorizer,
	svc *discovery.Service, audit discovery.HookAuditor, log forge.Logger,
) (*discovery.BitbucketWebhooks, error) {
	key, err := security.DeriveKey(cfg.SecretKey, discovery.BitbucketWebhookKeyLabel)
	if err != nil {
		return nil, err
	}
	w := &discovery.BitbucketWebhooks{
		Key:           key,
		PublicURL:     cfg.PublicURL,
		Hooks:         repositories.NewPgStore(pg),
		Conns:         conns,
		Audit:         audit,
		Authz:         authz,
		Sources:       discovery.NewPgStore(pg).SourcesByRepository,
		Start:         svc.StartFromBitbucketPush,
		DefaultBranch: repositories.NewBitbucket(cfg.Bitbucket.APIURL, cfg.Bitbucket.WebURL).DefaultBranch,
		Warn:          func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	}
	// An interface holding a nil *BitbucketOAuth isn't nil, so it's set only
	// when there is a consumer.
	if cfg.Bitbucket.Configured() {
		w.API = bitbucketOAuth(cfg.Bitbucket)
	}
	return w, nil
}

// bitbucketHookAuditor records push webhooks being added and removed, in
// the organization's stream. It's given no secret.
type bitbucketHookAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

var _ discovery.HookAuditor = bitbucketHookAuditor{}

// RecordHook implements discovery.HookAuditor. Who acted is the signed-in
// member when there is one; a disconnect's cleanup has one too.
func (a bitbucketHookAuditor) RecordHook(ctx context.Context, action, orgID, repository, hookID string) {
	b := a.chronicle.Info(ctx, action, "bitbucket_webhook", hookID).
		Category("integrations").AppID(a.appID).TenantID(orgID).Outcome("success").Meta("repository", repository)
	if p, ok := security.PrincipalFrom(ctx); ok && p.UserID != "" {
		b = b.UserID(p.UserID)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
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

// bitbucketOAuth is the client for the configured Bitbucket consumer.
func bitbucketOAuth(b config.BitbucketConfig) *repositories.BitbucketOAuth {
	return repositories.NewBitbucketOAuth(repositories.BitbucketOAuthConfig{
		APIURL: b.APIURL, WebURL: b.WebURL, ClientID: b.ClientID, ClientSecret: b.ClientSecret,
	})
}
