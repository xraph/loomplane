//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/plugin"
	"github.com/xraph/grove"
	"github.com/xraph/ledger"
)

// orgDeleteTimeout bounds the invoicing a workspace's deletion does. The
// deletion has happened by then, so the request being cancelled doesn't
// stop it.
const orgDeleteTimeout = 2 * time.Minute

// orgDeleteHook runs after authsome deletes a workspace. Identity registers
// it before authsome's subscription plugin, whose own hook cancels the
// workspace's subscriptions, because authsome runs those hooks in
// registration order and the overage has to be invoiced first.
type orgDeleteHook struct{ s *Service }

var (
	_ plugin.Plugin         = (*orgDeleteHook)(nil)
	_ plugin.AfterOrgDelete = (*orgDeleteHook)(nil)
)

// NewOrgDeleteHook is an authsome plugin: after a workspace is deleted it
// pushes the workspace's usage, invoices the current period of each of its
// subscriptions (usage to now), and finalizes them; the sync sends them. It
// runs before authsome's subscription plugin cancels them.
//
// It builds its own Service from db, l and appID. It needs no payments:
// the hourly sync job sends the invoices.
func NewOrgDeleteHook(db *grove.DB, l *ledger.Ledger, appID string) plugin.Plugin {
	return newOrgDeleteHook(New(Deps{DB: db, Ledger: l, AppID: appID}))
}

func newOrgDeleteHook(s *Service) *orgDeleteHook { return &orgDeleteHook{s: s} }

// Name names the hook in authsome's plugin registry.
func (*orgDeleteHook) Name() string { return "loomplane-billing-org-delete" }

// OnAfterOrgDelete invoices what the deleted workspace owes. authsome logs
// an error and carries on; the month close then invoices the canceled
// subscriptions' last period instead.
func (h *orgDeleteHook) OnAfterOrgDelete(ctx context.Context, orgID authid.OrgID) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), orgDeleteTimeout)
	defer cancel()
	org := orgID.String()
	if err := h.s.invoiceDeleted(ctx, org); err != nil {
		slog.ErrorContext(ctx, "billing: invoicing a deleted workspace failed; the month close invoices it",
			"org", org, "error", err)
		return err
	}
	return nil
}

// invoiceDeleted pushes the workspace's usage, rolls ledger's periods on
// and invoices the current period of each of its subscriptions, with usage
// to now. When some of its usage didn't reach ledger nothing is invoiced:
// the month close invoices the canceled subscriptions' period in full.
func (s *Service) invoiceDeleted(ctx context.Context, org string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if org == "" {
		return errors.New("billing: a deleted workspace needs an id")
	}
	accounts, err := s.billedAccounts(ctx, org)
	if err != nil || len(accounts) == 0 {
		return err // nothing was ever billed
	}
	_, pushErr := s.push(ctx, org, true)
	// A period that ended and hasn't rolled yet is last month's. Roll it
	// first, so the invoice is this month's; last month is its close's to
	// bill, canceled or not.
	if _, advErr := s.l.Advance(ctx, s.now().UTC()); advErr != nil {
		slog.WarnContext(ctx, "billing: ledger's clock didn't finish advancing before a deleted workspace was invoiced",
			"org", org, "error", advErr)
	}
	if pushErr != nil {
		return fmt.Errorf("billing: %s isn't invoiced on delete, its usage didn't all reach ledger: %w", org, pushErr)
	}
	a := accounts[0]
	// An add-on turned off whose month has ended has no current period to
	// pay for: its last month is the close's, billed for that month only.
	if a.aiEndsAt != nil && !a.aiEndsAt.After(s.now().UTC()) {
		a.aiSub = ""
	}
	return s.invoiceAccount(ctx, a, nil)
}
