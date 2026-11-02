package apikeys

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/keysmith/store"

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

type unavailablePrincipalStore struct {
	store.Store
	cause error
}

func (s unavailablePrincipalStore) Keys() key.Store {
	return unavailablePrincipalKeys{Store: s.Store.Keys(), cause: s.cause}
}

type unavailablePrincipalKeys struct {
	key.Store
	cause error
}

func (s unavailablePrincipalKeys) Get(context.Context, id.KeyID) (*key.Key, error) {
	return nil, s.cause
}

func TestValidateSavedPrincipalStorageFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		cause := errors.New("backend password=private outage")
		if missing {
			cause = store.ErrKeyNotFound
		}
		engine, err := keysmith.NewEngine(keysmith.WithStore(unavailablePrincipalStore{Store: memory.New(), cause: cause}))
		if err != nil {
			t.Fatal(err)
		}
		err = New(engine, "app", nil, nil).ValidatePrincipal(t.Context(), security.Principal{OrgID: "org", UserID: "user", KeyID: id.NewKeyID().String()})
		if err == nil || errors.Is(err, ErrRejected) != missing || strings.Contains(err.Error(), "private") {
			t.Fatalf("storage failure classification missing=%v: %v", missing, err)
		}
	}
}
