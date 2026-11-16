package discovery

import (
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// platformApps are the repository apps saved for the platform from the
// instance console: the GitHub App, gitlab.com's application and Bitbucket's
// consumer, each used only where configuration doesn't set one. Each
// process reads them through its own cache, so a save reaches the API at
// once and the worker within half a minute.
func platformApps(cfg config.Config, db *grove.DB, log forge.Logger) (*repositories.PlatformApps, error) {
	key, err := security.DeriveKey(cfg.SecretKey, repositories.PlatformAppsVaultKeyLabel)
	if err != nil {
		return nil, err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return nil, fmt.Errorf("vault encryptor: %w", err)
	}
	svc := secret.NewService(vaultpg.New(db), enc, secret.WithAppID(repositories.PlatformAppsVaultAppID))
	apps := repositories.NewPlatformApps(repositories.NewVaultPlatformApps(svc))
	if log != nil {
		apps.SetLogf(func(format string, args ...any) { log.Warn(fmt.Sprintf(format, args...)) })
	}
	return apps, nil
}

// PlatformApps builds the platform's repository apps over db, for another
// extension that needs to read or change them, such as the instance console.
func PlatformApps(cfg config.Config, db *grove.DB, log forge.Logger) (*repositories.PlatformApps, error) {
	return platformApps(cfg, db, log)
}
