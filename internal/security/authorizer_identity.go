package security

import (
	"reflect"

	"github.com/xraph/warden"
)

// MatchesEngineStore reports whether this Authorizer uses your exact engine and
// store. It reads their identity only and never checks permissions or repairs roles.
func (a *Authorizer) MatchesEngineStore(engine *warden.Engine, expected Store) bool {
	if a == nil || engine == nil {
		return false
	}
	actual, ok := a.engine.(*warden.Engine)
	if !ok || actual != engine {
		return false
	}
	for _, candidate := range []Store{a.store, expected} {
		value := reflect.ValueOf(candidate)
		if !value.IsValid() || !value.Comparable() {
			return false
		}
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			if value.IsNil() {
				return false
			}
		}
	}
	return a.store == expected
}
