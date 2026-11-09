package identity

import (
	"context"
	"errors"

	"github.com/xraph/authsome/id"
	"github.com/xraph/authsome/plugins/waitlist"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/notifications"
)

// approvalMailer emails people when their waitlist entry is approved. It wraps
// the store because the console's approve command writes through the store
// without firing a hook, while the REST route does fire one. The store is the
// one place both paths share.
type approvalMailer struct {
	waitlist.Store
	send func(context.Context, string) error
	log  forge.Logger
}

func (m approvalMailer) UpdateEntryStatus(ctx context.Context, entryID id.WaitlistID, status waitlist.WaitlistStatus, note string) error {
	if err := m.Store.UpdateEntryStatus(ctx, entryID, status, note); err != nil {
		return err
	}
	if status != waitlist.StatusApproved {
		return nil
	}
	// The approval is saved. Mail trouble must not undo it or fail the request.
	entry, err := m.GetEntry(ctx, entryID)
	if err != nil {
		m.log.Warn("waitlist approval mail skipped: entry lookup failed", forge.F("error", err.Error()))
		return nil
	}
	if err = m.send(ctx, entry.Email); err != nil && !errors.Is(err, notifications.ErrMailDisabled) {
		m.log.Warn("waitlist approval mail failed", forge.F("error", err.Error()))
	}
	return nil
}
