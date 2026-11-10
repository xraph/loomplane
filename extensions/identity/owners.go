package identity

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/authsome/id"
	authstore "github.com/xraph/authsome/store"

	"github.com/xraph/loomplane/internal/platformadmin"
)

// PlatformOwnerEmails lists the email addresses of the instance's platform
// owners, read live from Warden. It is for mail that only the platform may
// see, such as a Platform Cloud host's certificate running out; nothing it
// returns belongs to a workspace.
type PlatformOwnerEmails func(ctx context.Context) ([]string, error)

func (e *Extension) platformOwnerEmails(ctx context.Context) ([]string, error) {
	return platformadmin.OwnerEmails(ctx, e.platformRoles, e.warden.Store(), e.ownerAccount, e.cfg.Identity.AppID, time.Now())
}

// ownerAccount reads a user of Loomplane's app. A user of another app, or
// one that was deleted, is no user.
func (e *Extension) ownerAccount(ctx context.Context, userID string) (platformadmin.OwnerAccount, error) {
	uid, err := id.ParseUserID(userID)
	if err != nil {
		return platformadmin.OwnerAccount{}, platformadmin.ErrNoUser
	}
	u, err := e.store.GetUser(ctx, uid)
	if errors.Is(err, authstore.ErrNotFound) {
		return platformadmin.OwnerAccount{}, platformadmin.ErrNoUser
	}
	if err != nil {
		return platformadmin.OwnerAccount{}, err
	}
	if u.AppID.String() != e.cfg.Identity.AppID {
		return platformadmin.OwnerAccount{}, platformadmin.ErrNoUser
	}
	return platformadmin.OwnerAccount{Email: u.Email, Banned: u.Banned}, nil
}
