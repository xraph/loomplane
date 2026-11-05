//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/xraph/forge"
	utilslog "github.com/xraph/go-utils/log"
	"github.com/xraph/keysmith"
	"github.com/xraph/relay"
	"github.com/xraph/relay/endpoint"
	relaypg "github.com/xraph/relay/store/postgres"
	"github.com/xraph/vault/secret"
	vaultstore "github.com/xraph/vault/store"
	"github.com/xraph/warden"

	"github.com/xraph/loomplane/internal/checkpoints"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
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

// Identity starts Warden itself, so the container must not start it again
// when it starts the values it holds. A second Start runs a second hourly
// maintenance loop, and Stop only cancels the last one.
func TestWardenStartsOnce(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	logs, ok := utilslog.NewTestLogger().(*utilslog.TestLogger)
	if !ok {
		t.Fatal("go-utils' test logger is no longer a *TestLogger")
	}
	app, err := platform.New(cfg, forge.WithAppLogger(logs))
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = app.Start(lifetime); err != nil {
		t.Fatalf("start app: %v", err)
	}
	ctx, stopCancel := context.WithTimeout(context.Background(), startTimeout)
	defer stopCancel()
	if err = app.Stop(ctx); err != nil {
		t.Fatalf("stop app: %v", err)
	}

	starts := 0
	for _, entry := range logs.GetLogs() {
		if entry.Message == "warden: engine starting" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("warden started %d times, want once", starts)
	}
}
