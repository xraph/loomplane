package apikeys

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/key"
	"github.com/xraph/keysmith/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

func TestKeysNeverActAsTheAutomation(t *testing.T) {
	engine, err := keysmith.NewEngine(keysmith.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(engine, "app", nil, nil)
	if _, err = svc.Create(t.Context(), security.SystemPrincipal("org"), NewKey{Name: "ci", Access: Write}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("the automation made a key: %v", err)
	}
	// A key that names the automation as its maker, however it got there,
	// authenticates as no one.
	expires := time.Now().Add(time.Hour)
	res, err := engine.CreateKey(svc.tenant(t.Context(), "org"), &keysmith.CreateKeyInput{
		Name: "forged", Prefix: Prefix, Environment: key.EnvLive, TenantID: "org",
		CreatedBy: security.SystemSubject("org"), ExpiresAt: &expires,
		Metadata: map[string]any{"access": string(Write)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p, authErr := svc.Authenticate(t.Context(), res.RawKey); !errors.Is(authErr, ErrRejected) {
		t.Fatalf("a key resolved to %+v, %v", p, authErr)
	}
}
