// Package platformadmin checks and grants the platform roles: authsome's
// platform-owner and platform-admin on Loomplane's own authsome app. They
// open the instance console.
package platformadmin

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/authsome/id"
	"github.com/xraph/authsome/rbac"
	authstore "github.com/xraph/authsome/store"
	authpg "github.com/xraph/authsome/store/postgres"
	"github.com/xraph/authsome/user"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/warden"
	wardenpg "github.com/xraph/warden/store/postgres"
)

// ErrNoUser is returned when no user has the email.
var ErrNoUser = errors.New("no user with that email")

// UserFinder finds a user's id by email, comparing emails case-insensitively.
// It returns ErrNoUser when there is no such user.
type UserFinder interface {
	UserIDByEmail(ctx context.Context, email string) (string, error)
}

// Holds reports whether roles include a platform role in app appID.
func Holds(roles []*rbac.Role, appID string) bool {
	for _, r := range roles {
		if r.AppID == appID && (r.Slug == rbac.PlatformOwnerSlug || r.Slug == rbac.PlatformAdminSlug) {
			return true
		}
	}
	return false
}

// Grant gives the user with email the platform-owner role. It returns false,
// and changes nothing, when the user already holds a platform role.
func Grant(ctx context.Context, users UserFinder, roles rbac.Store, appID, email string) (granted bool, err error) {
	userID, err := users.UserIDByEmail(ctx, email)
	if err != nil {
		return false, err
	}
	held, err := roles.ListUserRolesForApp(ctx, appID, userID)
	if err != nil {
		return false, fmt.Errorf("list roles: %w", err)
	}
	if Holds(held, appID) {
		return false, nil
	}
	role, err := roles.GetRoleBySlug(ctx, appID, rbac.PlatformOwnerSlug)
	if err != nil {
		return false, fmt.Errorf("find the %s role (has Loomplane started against this database?): %w", rbac.PlatformOwnerSlug, err)
	}
	if err := roles.AssignUserRole(ctx, appID, &rbac.UserRole{UserID: userID, RoleID: role.ID}); err != nil {
		return false, fmt.Errorf("assign %s: %w", rbac.PlatformOwnerSlug, err)
	}
	return true, nil
}

// Open connects to the database and returns the user and role stores for
// Loomplane's authsome app. The roles are read and written through Warden,
// as authsome's own engine does. closeFn releases the connection.
func Open(ctx context.Context, dsn, appID string) (users UserFinder, roles rbac.Store, closeFn func(), err error) {
	aid, err := id.ParseAppID(appID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("identity.app_id: %w", err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		return nil, nil, nil, fmt.Errorf("open database: %w", err)
	}
	db, err := grove.Open(pg)
	if err != nil {
		_ = pg.Close()
		return nil, nil, nil, fmt.Errorf("open database: %w", err)
	}
	closeFn = func() { _ = db.Close() }
	eng, err := warden.NewEngine(warden.WithStore(wardenpg.New(db)))
	if err != nil {
		closeFn()
		return nil, nil, nil, fmt.Errorf("warden: %w", err)
	}
	store := authpg.New(db)
	env, err := store.GetDefaultEnvironment(ctx, aid)
	if err != nil {
		closeFn()
		return nil, nil, nil, fmt.Errorf("find the default environment (has Loomplane started against this database?): %w", err)
	}
	return &finder{store: store, appID: aid, envID: env.ID}, rbac.NewWardenStore(eng), closeFn, nil
}

type finder struct {
	store *authpg.Store
	appID id.AppID
	envID id.EnvironmentID
}

func (f *finder) UserIDByEmail(ctx context.Context, email string) (string, error) {
	u, err := f.store.GetUserByEmail(ctx, f.appID, f.envID, user.NormalizeEmail(email))
	if errors.Is(err, authstore.ErrNotFound) {
		return "", ErrNoUser
	}
	if err != nil {
		return "", fmt.Errorf("look up user: %w", err)
	}
	return u.ID.String(), nil
}
