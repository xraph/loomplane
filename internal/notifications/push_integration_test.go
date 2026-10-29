//go:build integration

package notifications

import (
	"encoding/base64"
	"testing"

	"github.com/xraph/grove"
	"github.com/xraph/herald/credential"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

func pushProviders(t *testing.T, svc *Service) []string {
	t.Helper()
	key, err := security.DeriveKey(svc.cfg.SecretKey, "loomplane/herald credentials v1")
	if err != nil {
		t.Fatal(err)
	}
	box, err := credential.NewCipher(credential.Key{ID: "lp1", Bytes: key})
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.mailStore.ListAllProviders(t.Context(), svc.cfg.Identity.AppID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range list {
		if p.Name == "loomplane-push" {
			if p.Driver != "sealed" || p.Channel != "push" {
				t.Fatalf("provider: %#v", p)
			}
			seed, err := box.Decrypt(p.ID.String(), "signing_key", p.Credentials["signing_key"])
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, seed)
		}
	}
	return out
}

func pushService(t *testing.T, db *grove.DB, secret []byte, migrate bool) *Service {
	t.Helper()
	cfg := config.Config{SecretKey: base64.StdEncoding.EncodeToString(secret), Identity: config.IdentityConfig{AppID: "app"}, Push: config.PushConfig{Enabled: true, GatewayURL: "https://push.example.com"}}
	svc, err := New(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if migrate {
		if err = svc.mailStore.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestPreparePushUpsertsOneProvider(t *testing.T) {
	db := notificationDatabase(t)
	svc := pushService(t, db, make([]byte, 32), true)
	if err := svc.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	list, err := svc.mailStore.ListAllProviders(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range list {
		if p.Name == "loomplane-push" {
			n++
			if p.Driver != "sealed" || p.Settings["gateway_url"] != "https://push.example.com" || !p.Enabled {
				t.Fatalf("provider: %#v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d push providers", n)
	}
}

func TestRotatedSecretMovesKeyIDAndProvider(t *testing.T) {
	db := notificationDatabase(t)
	before, err := pushService(t, db, make([]byte, 32), true).PushConfig(t.Context(), security.Principal{UserID: "user-a"})
	if err != nil || before.KeyID != "8029410f9a15b4ff" {
		t.Fatalf("before: %#v %v", before, err)
	}
	other := make([]byte, 32)
	other[0] = 7
	rotated := pushService(t, db, other, false)
	after, err := rotated.PushConfig(t.Context(), security.Principal{UserID: "user-a"})
	if err != nil || after.KeyID == before.KeyID {
		t.Fatalf("after: %#v %v", after, err)
	}
	seeds := pushProviders(t, rotated)
	if len(seeds) != 1 {
		t.Fatalf("%d push providers", len(seeds))
	}
	seed, err := serverKey(rotated.cfg.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0] != base64.StdEncoding.EncodeToString(seed.Seed()) {
		t.Fatal("provider kept the old signing key")
	}
}
