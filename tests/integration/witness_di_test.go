//go:build integration

package integration

import (
	"testing"

	"github.com/xraph/forge"
	"github.com/xraph/keysmith"
	"github.com/xraph/relay"
	"github.com/xraph/relay/endpoint"
	relaypg "github.com/xraph/relay/store/postgres"
	"github.com/xraph/vault/secret"
	vaultstore "github.com/xraph/vault/store"
	"github.com/xraph/warden"

	"github.com/xraph/loomplane/internal/checkpoints"
	"github.com/xraph/loomplane/internal/config"
)

// Hallproof's witness finds each library's engine in DI by its type, so
// each one has to be there, with Relay's endpoint store unwrapped beside
// Relay. The Vault store also holds the checkpoint signing key the first
// start created.
func TestWitnessDependenciesAreProvided(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := r.app.(forge.App).Container()
	resolves[*warden.Engine](t, c, "warden")
	resolves[*keysmith.Engine](t, c, "keysmith")
	resolves[*relay.Relay](t, c, "relay")
	resolves[vaultstore.Store](t, c, "vault store")
	resolves[endpoint.Store](t, c, "relay endpoint store")

	// The endpoint store is Relay's Postgres store itself, not the wrapper
	// that decrypts signing secrets.
	if eps, err := forge.InjectType[endpoint.Store](c); err == nil {
		if _, raw := eps.(*relaypg.Store); !raw {
			t.Errorf("the relay endpoint store is %T, want the raw *postgres.Store", eps)
		}
	}

	store, err := forge.InjectType[vaultstore.Store](c)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := store.ListSecrets(t.Context(), checkpoints.VaultAppID, secret.ListOpts{})
	if err != nil || len(keys) != 1 || keys[0].Version != 1 {
		t.Fatalf("checkpoint key in Vault: %d secrets, %v; want one, at version 1", len(keys), err)
	}
}
