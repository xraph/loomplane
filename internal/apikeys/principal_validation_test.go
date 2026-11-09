package apikeys

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"

	"github.com/xraph/loomplane/internal/security"
)

func TestSavedKeyRecordSuppliedTime(t *testing.T) {
	at := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := at.Add(time.Second)
	original := key.Key{ID: id.NewKeyID(), AppID: "app", TenantID: "org", CreatedBy: "person", State: key.StateActive, ExpiresAt: &expires, Metadata: map[string]any{"access": "write"}}
	p := security.Principal{UserID: "person", OrgID: "org", KeyID: original.ID.String()}
	tests := []struct {
		name   string
		change func(*key.Key)
		at     time.Time
		valid  bool
	}{
		{"historical supplied instant", nil, at, true},
		{"expiry equality", nil, expires, false},
		{"after expiry", nil, expires.Add(time.Nanosecond), false},
		{"zero instant", nil, time.Time{}, false},
		{"app", func(k *key.Key) { k.AppID = "other" }, at, false},
		{"tenant", func(k *key.Key) { k.TenantID = "other" }, at, false},
		{"creator", func(k *key.Key) { k.CreatedBy = "other" }, at, false},
		{"id", func(k *key.Key) { k.ID = id.NewKeyID() }, at, false},
		{"state", func(k *key.Key) { k.State = key.StateSuspended }, at, false},
		{"revocation", func(k *key.Key) { k.RevokedAt = &at }, at, false},
		{"policy", func(k *key.Key) { v := id.NewPolicyID(); k.PolicyID = &v }, at, false},
		{"no expiry", func(k *key.Key) { k.ExpiresAt = nil }, at, false},
		{"read", func(k *key.Key) { k.Metadata = map[string]any{"access": "read"} }, at, false},
		{"missing access", func(k *key.Key) { k.Metadata = nil }, at, false},
		{"kind even nil", func(k *key.Key) { k.Metadata = map[string]any{"access": "write", "kind": nil} }, at, false},
		{"scope hydration is not evidence", func(k *key.Key) { k.Scopes = []string{"must-read-persisted-links"} }, at, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := original
			if tt.change != nil {
				tt.change(&k)
			}
			before := k
			err := ValidateSavedKeyRecordAt(p, "app", &k, tt.at)
			if (err == nil) != tt.valid {
				t.Fatalf("error=%v valid=%v", err, tt.valid)
			}
			if !tt.valid && !errors.Is(err, ErrRejected) {
				t.Fatalf("error=%v", err)
			}
			if !reflect.DeepEqual(k, before) {
				t.Fatal("record mutated")
			}
		})
	}
	if err := ValidateSavedKeyRecordAt(p, "app", nil, at); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
	for _, change := range []func(*security.Principal){
		func(p *security.Principal) { p.ReadOnly = true }, func(p *security.Principal) { p.Kind = security.KindAgent },
		func(p *security.Principal) { p.OnBehalfOf = &security.Principal{} }, func(p *security.Principal) { p.Fleet = &security.FleetScope{} },
		func(p *security.Principal) { p.KeyID = "bad" }, func(p *security.Principal) { p.OrgID = "" }, func(p *security.Principal) { p.UserID = "" },
	} {
		q := p
		change(&q)
		if err := ValidateSavedKeyRecordAt(q, "app", &original, at); !errors.Is(err, ErrRejected) {
			t.Fatalf("accepted %+v", q)
		}
	}
}

func TestSavedKeyRecordRejectsSyntheticOrdinaryActor(t *testing.T) {
	at := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := at.Add(time.Hour)
	for _, user := range []string{"loomplane:system:org", "loomplane:other", "agent:org", security.FleetSubject("saved-key")} {
		t.Run(user, func(t *testing.T) {
			k := &key.Key{ID: id.NewKeyID(), AppID: "app", TenantID: "org", CreatedBy: user, State: key.StateActive, ExpiresAt: &expires, Metadata: map[string]any{"access": "write"}}
			p := security.Principal{UserID: user, OrgID: "org", KeyID: k.ID.String()}
			if err := ValidateSavedKeyRecordAt(p, "app", k, at); !errors.Is(err, ErrRejected) {
				t.Fatalf("synthetic actor with matching creator accepted: %v", err)
			}
		})
	}
}

func TestSavedKeyRecordAcceptsOrdinaryTypedIdentity(t *testing.T) {
	at := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := at.Add(time.Hour)
	user := "ausr_01h455vb4pex5vsknk084sn02q"
	k := &key.Key{ID: id.NewKeyID(), AppID: "app", TenantID: "org", CreatedBy: user, State: key.StateActive, ExpiresAt: &expires, Metadata: map[string]any{"access": "write"}}
	p := security.Principal{UserID: user, OrgID: "org", KeyID: k.ID.String()}
	if err := ValidateSavedKeyRecordAt(p, "app", k, at); err != nil {
		t.Fatal(err)
	}
}
