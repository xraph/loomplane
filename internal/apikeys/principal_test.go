package apikeys

import (
	"testing"
	"time"

	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	"github.com/xraph/keysmith/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

func TestValidateSavedPrincipal(t *testing.T) {
	store := memory.New()
	engine, err := keysmith.NewEngine(keysmith.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(engine, "app", nil, nil)
	now := time.Now()
	expires := now.Add(time.Hour)
	k := &key.Key{ID: id.NewKeyID(), AppID: "app", TenantID: "org", CreatedBy: "user", State: key.StateActive, ExpiresAt: &expires, Metadata: map[string]any{"access": "write"}}
	if err = store.Keys().Create(t.Context(), k); err != nil {
		t.Fatal(err)
	}
	principal := security.Principal{OrgID: "org", UserID: "user", KeyID: k.ID.String()}
	if err = svc.ValidatePrincipal(t.Context(), principal); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*key.Key){func(k *key.Key) { k.State = key.StateRevoked }, func(k *key.Key) { k.ExpiresAt = &now }, func(k *key.Key) { k.CreatedBy = "another" }, func(k *key.Key) { k.Metadata = map[string]any{"access": "read"} }} {
		copyKey := *k
		mutate(&copyKey)
		if err = store.Keys().Update(t.Context(), &copyKey); err != nil {
			t.Fatal(err)
		}
		if err = svc.ValidatePrincipal(t.Context(), principal); err == nil {
			t.Fatal("invalid saved authority accepted")
		}
	}
	principal.ReadOnly = true
	if err = svc.ValidatePrincipal(t.Context(), principal); err == nil {
		t.Fatal("read-only authority accepted")
	}
}
