package currentauthority

import (
	"context"
	"reflect"
	"strings"

	authid "github.com/xraph/authsome/id"

	"github.com/xraph/loomplane/internal/security"
)

// captureOrdinaryProfile reads the unpublished candidate after common SQL
// qualification. The installer keeps ownership of its pools and publication.
func captureOrdinaryProfile(c *Coordinator, installation *builtInInstallation, authorizer *security.Authorizer, requireEmail bool) (*ordinaryProfile, error) {
	if c == nil || installation == nil || installation.main == nil || installation.backend == nil || installation.engine == nil || installation.owner == 0 || strings.TrimSpace(installation.schema) == "" {
		return nil, ErrUnavailable
	}
	if _, err := authid.ParseAppID(installation.appID); err != nil {
		return nil, ErrUnavailable
	}
	router, ok := installation.engine.Store().(*storeRouter)
	if !ok || router == nil || router.owner != c || router.ordinary == nil {
		return nil, ErrUnavailable
	}
	ordinary := reflect.ValueOf(router.ordinary)
	switch ordinary.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if ordinary.IsNil() {
			return nil, ErrUnavailable
		}
	}
	if !authorizer.MatchesEngineStore(installation.engine, router) {
		return nil, ErrUnavailable
	}
	if _, err := captureEngineProfile(installation.engine, router); err != nil {
		return nil, err
	}
	return &ordinaryProfile{
		authorizer:    authorizer,
		appID:         installation.appID,
		requireEmail:  requireEmail,
		migrationRole: installation.owner,
	}, nil
}

// OrdinaryAuthority exposes the ordinary entry point through startup and shutdown.
// You can retain this handle before installation; work stays unavailable until then.
type OrdinaryAuthority struct{ coordinator *Coordinator }

// OrdinaryAuthority returns a dependency handle bound to this coordinator.
func (c *Coordinator) OrdinaryAuthority() *OrdinaryAuthority {
	return &OrdinaryAuthority{coordinator: c}
}

// RunOrdinary executes an ordinary operation through the owned coordinator.
func (o *OrdinaryAuthority) RunOrdinary(ctx context.Context, binding Binding, work func(context.Context, *Attempt) error) error {
	if o == nil || o.coordinator == nil {
		return ErrUnavailable
	}
	return o.coordinator.Run(ctx, binding, work)
}
