package security

import (
	"testing"

	"github.com/xraph/warden"
)

type identityStore struct{ Store }
type identityStoreWrapper struct{ Store }
type noncomparableIdentityStore struct {
	Store
	values []int
}
type nestedIdentityStore struct {
	Store
	value any
}
type identityEngineWrapper struct{ Engine }

func TestAuthorizerMatchesEngineStore(t *testing.T) {
	engine, otherEngine := &warden.Engine{}, &warden.Engine{}
	store, otherStore := &identityStore{}, &identityStore{}
	var absentEngine *warden.Engine
	var absentStore *identityStore
	var absentAuthorizer *Authorizer
	noncomparable := noncomparableIdentityStore{values: []int{1}}
	nested := nestedIdentityStore{value: []int{1}}
	for _, tc := range []struct {
		name       string
		authorizer *Authorizer
		engine     *warden.Engine
		store      Store
		want       bool
	}{
		{"exact", NewAuthorizer(engine, store, nil), engine, store, true},
		{"nil authorizer", absentAuthorizer, engine, store, false},
		{"nil expected engine", NewAuthorizer(engine, store, nil), nil, store, false},
		{"nil actual engine", NewAuthorizer(nil, store, nil), engine, store, false},
		{"typed nil actual engine", NewAuthorizer(absentEngine, store, nil), engine, store, false},
		{"different engine same store", NewAuthorizer(otherEngine, store, nil), engine, store, false},
		{"engine wrapper", NewAuthorizer(identityEngineWrapper{engine}, store, nil), engine, store, false},
		{"same engine different store", NewAuthorizer(engine, otherStore, nil), engine, store, false},
		{"store wrapper", NewAuthorizer(engine, &identityStoreWrapper{store}, nil), engine, store, false},
		{"nil actual store", NewAuthorizer(engine, nil, nil), engine, store, false},
		{"nil expected store", NewAuthorizer(engine, store, nil), engine, nil, false},
		{"typed nil actual store", NewAuthorizer(engine, absentStore, nil), engine, store, false},
		{"typed nil expected store", NewAuthorizer(engine, store, nil), engine, absentStore, false},
		{"same typed nil store", NewAuthorizer(engine, absentStore, nil), engine, absentStore, false},
		{"noncomparable actual", NewAuthorizer(engine, noncomparable, nil), engine, store, false},
		{"noncomparable expected", NewAuthorizer(engine, store, nil), engine, noncomparable, false},
		{"same noncomparable store", NewAuthorizer(engine, noncomparable, nil), engine, noncomparable, false},
		{"nested noncomparable interface", NewAuthorizer(engine, nested, nil), engine, nested, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.authorizer.MatchesEngineStore(tc.engine, tc.store); got != tc.want {
				t.Fatalf("association=%v, want %v", got, tc.want)
			}
		})
	}
}
