package identity

import (
	"context"
	"fmt"

	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/organization"
)

// pendingInvitation reports whether an organization in Loomplane's app has
// invited email and the invitation is still open: pending and not expired.
// An invited person may sign up while the waitlist is on.
func (e *Extension) pendingInvitation(ctx context.Context, email string) (bool, error) {
	appID, err := authid.ParseAppID(e.cfg.Identity.AppID)
	if err != nil {
		return false, fmt.Errorf("identity.app_id: %w", err)
	}
	var open bool
	err = e.pg.QueryRow(ctx, `SELECT EXISTS (
	SELECT 1 FROM authsome_invitations i
	JOIN authsome_organizations o ON o.id = i.org_id
	WHERE o.app_id = $1 AND lower(i.email) = lower($2) AND i.status = $3 AND i.expires_at > now()
)`, appID.String(), email, string(organization.InvitationPending)).Scan(&open)
	if err != nil {
		return false, err
	}
	return open, nil
}
