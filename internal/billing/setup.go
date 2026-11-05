//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The dashboard pages Stripe sends the owner back to after the card form.
const (
	setupReturnPath = "/settings/billing?card=added"
	setupCancelPath = "/settings/billing"
)

// ErrNoSuchMember is SetupSession's error when the workspace or the person
// asking can't be found.
var ErrNoSuchMember = errors.New("billing: the workspace or the person adding the card isn't there")

// SetupSession opens a Stripe Checkout page in setup mode where the owner
// saves a card, and returns its URL. The workspace's Stripe customer is
// made first when it has none, named after the workspace and billed to
// the owner's email. The card is recorded when Stripe's webhook says it
// was saved, never on the way back from the page.
func (s *Service) SetupSession(ctx context.Context, orgID, userID string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	if orgID == "" || userID == "" {
		return "", errors.New("billing: a card needs a workspace and the owner adding it")
	}
	if s.payments == nil {
		return "", errors.New("billing: nothing to save a card with")
	}
	var name, email string
	err := s.pg.QueryRow(ctx, `SELECT o.name, u.email FROM authsome_organizations o, authsome_users u
		WHERE o.id = $1 AND o.app_id = $3 AND u.id = $2 AND u.app_id = $3 AND u.deleted_at IS NULL`,
		orgID, userID, s.appID).Scan(&name, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoSuchMember
	}
	if err != nil {
		return "", fmt.Errorf("billing: read who is adding a card to %s: %w", orgID, err)
	}
	if _, err = s.payments.EnsureCustomer(ctx, orgID, email, name); err != nil {
		return "", fmt.Errorf("billing: make the Stripe customer of %s: %w", orgID, err)
	}
	url, err := s.payments.CreateSetupSession(ctx, orgID, s.cfg.PublicURL+setupReturnPath, s.cfg.PublicURL+setupCancelPath)
	if err != nil {
		return "", fmt.Errorf("billing: open the card form of %s: %w", orgID, err)
	}
	return url, nil
}
