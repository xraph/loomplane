package notifications

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/xraph/herald/driver"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

func TestServerKeyIsDerivedFromTheSecret(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString(make([]byte, 32))
	svc := &Service{cfg: config.Config{SecretKey: secret, Push: config.PushConfig{Enabled: true, GatewayURL: "https://push.loomplane.com"}}}
	got, err := svc.PushConfig(context.Background(), security.Principal{UserID: "user-a"})
	if err != nil {
		t.Fatal(err)
	}
	want := PushConfig{Enabled: true, GatewayURL: "https://push.loomplane.com", ServerKey: "/2xOZSU8Z1GL1CFVa5xp22GnWVgFMmbOD4P/WgWw23Y=", KeyID: "8029410f9a15b4ff"}
	if got != want {
		t.Fatalf("push config: got %#v, want %#v", got, want)
	}
	key, err := serverKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	if id := KeyID(key.Public().(ed25519.PublicKey)); id != want.KeyID {
		t.Fatalf("key id: %s", id)
	}
}

func TestPushConfigWhenOff(t *testing.T) {
	svc := &Service{cfg: config.Config{SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Push: config.PushConfig{GatewayURL: "https://push.loomplane.com"}}}
	got, err := svc.PushConfig(context.Background(), security.Principal{UserID: "user-a"})
	if err != nil {
		t.Fatal(err)
	}
	if got != (PushConfig{Enabled: false}) {
		t.Fatalf("off config leaks values: %#v", got)
	}
	if svc.PushEnabled() {
		t.Fatal("push reported enabled")
	}
}

func TestPushConfigNeedsASignedInPerson(t *testing.T) {
	svc := &Service{cfg: config.Config{SecretKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), Push: config.PushConfig{Enabled: true}}}
	if _, err := svc.PushConfig(context.Background(), security.Principal{}); err == nil {
		t.Fatal("anonymous caller got the config")
	}
}

func TestValidatePushRejectsUnknownDataKeys(t *testing.T) {
	ok := Notice{Push: &Push{Class: ClassUpdate, Category: CategoryUpdate, Urgency: driver.UrgencyPassive, Data: map[string]string{"revision": "r", "taken_by": "u"}}}
	if err := validatePush(ok); err != nil {
		t.Fatalf("known keys: %v", err)
	}
	for _, key := range []string{"server", "org", "path", "inbox_id"} {
		bad := Notice{Push: &Push{Class: ClassUpdate, Category: CategoryUpdate, Urgency: driver.UrgencyPassive, Data: map[string]string{key: "x"}}}
		if err := validatePush(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", key, err)
		}
	}
}
