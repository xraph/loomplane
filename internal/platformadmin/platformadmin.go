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
	"github.com/xraph/chronicle"
	chronstore "github.com/xraph/chronicle/store"
	chronpg "github.com/xraph/chronicle/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/warden"
	wardenpg "github.com/xraph/warden/store/postgres"
)

// AuditTenant is the Chronicle tenant staff events are recorded under, in
// Loomplane's app. Staff are the holders of a platform role. Keeping their
// events apart means the stream holds nothing of a customer's, so it can be
// proven to someone outside, such as Hallproof, as it is.
const AuditTenant = "platform"

// CLIActor is the user ID an event recorded by the loomplane CLI carries.
// A command run on the server has no signed-in user; the operating system
// account and host it ran as go in the event's metadata.
const CLIActor = "cli"

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

// IsOwner reports whether roles include the platform-owner role in app
// appID. A platform admin is not an owner.
func IsOwner(roles []*rbac.Role, appID string) bool {
	for _, r := range roles {
		if r.AppID == appID && r.Slug == rbac.PlatformOwnerSlug {
			return true
		}
	}
	return false
}

// Grant gives the user with email the platform-owner role and returns
// their user ID. It returns false, and changes nothing, when the user
// already holds a platform role.
func Grant(ctx context.Context, users UserFinder, roles rbac.Store, appID, email string) (userID string, granted bool, err error) {
	userID, err = users.UserIDByEmail(ctx, email)
	if err != nil {
		return "", false, err
	}
	held, err := roles.ListUserRolesForApp(ctx, appID, userID)
	if err != nil {
		return userID, false, fmt.Errorf("list roles: %w", err)
	}
	if Holds(held, appID) {
		return userID, false, nil
	}
	role, err := roles.GetRoleBySlug(ctx, appID, rbac.PlatformOwnerSlug)
	if err != nil {
		return userID, false, fmt.Errorf("find the %s role (has Loomplane started against this database?): %w", rbac.PlatformOwnerSlug, err)
	}
	if err := roles.AssignUserRole(ctx, appID, &rbac.UserRole{UserID: userID, RoleID: role.ID}); err != nil {
		return userID, false, fmt.Errorf("assign %s: %w", rbac.PlatformOwnerSlug, err)
	}
	return userID, true, nil
}

// RecordGrant records in the platform stream that actor gave userID the
// platform-owner role. meta adds details about the actor, such as where a
// CLI command ran.
func RecordGrant(ctx context.Context, trail *chronicle.Chronicle, appID, actor, userID string, meta map[string]string) error {
	b := trail.Critical(ctx, "platform_admin.granted", "user", userID).
		Category("instance").AppID(appID).TenantID(AuditTenant).UserID(actor).Outcome("success").
		Meta("role", rbac.PlatformOwnerSlug)
	for k, v := range meta {
		b = b.Meta(k, v)
	}
	if err := b.Record(); err != nil {
		return fmt.Errorf("record the grant in the audit trail: %w", err)
	}
	return nil
}

// OpenAudit opens Loomplane's audit trail over the database, for a command
// that records an event without the running app. closeFn releases the
// connection.
func OpenAudit(ctx context.Context, dsn string) (trail *chronicle.Chronicle, closeFn func(), err error) {
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	db, err := grove.Open(pg)
	if err != nil {
		_ = pg.Close()
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	closeFn = func() { _ = db.Close() }
	if trail, err = chronicle.New(chronicle.WithStore(chronstore.NewAdapter(chronpg.New(db)))); err != nil {
		closeFn()
		return nil, nil, fmt.Errorf("chronicle: %w", err)
	}
	return trail, closeFn, nil
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
