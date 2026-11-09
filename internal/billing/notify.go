//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/xraph/loomplane/internal/notifications"
)

// Notice kinds billing sends. Like platform Cloud's, they aren't webhook
// events.
const (
	NoticeCredit80      = "billing.credit_80"
	NoticeCredit100     = "billing.credit_100"
	NoticeLimit80       = "billing.limit_80"
	NoticeLimit100      = "billing.limit_100"
	NoticeRestored      = "billing.restored"
	NoticePaymentFailed = "billing.payment_failed"
)

// noticePath is the dashboard page every billing notice opens.
const noticePath = "/settings/billing"

// creditWarnCents is the base usage that sends the 80% notice: 80% of the
// included credit.
const creditWarnCents = baseCreditCents * 4 / 5

// enqueue is how a Service sends a notice unless a test swaps it: to the
// workspace's owners and admins, through the notification outbox.
//
// The outbox makes a notice once per kind, subject and recipient across
// every workspace, and one person can be an admin of two workspaces, so
// the subject is put under the workspace's id here. Billing's subjects
// (credit_80:2026-11 and the rest) are the same in every workspace.
func (s *Service) enqueue(ctx context.Context, n notifications.Notice) error {
	if s.pg == nil {
		return errors.New("billing: notices need Loomplane's Postgres database")
	}
	admins, err := notifications.Admins(ctx, s.pg, n.OrgID)
	if err != nil {
		return fmt.Errorf("billing: list the admins of %s: %w", n.OrgID, err)
	}
	n.Recipients = admins
	n.SubjectID = n.OrgID + ":" + n.SubjectID
	return notifications.Enqueue(ctx, s.pg, n)
}

// sendNotice queues one notice to the workspace's admins. A notice that can't be
// queued is logged and never undoes the change it tells of.
func (s *Service) sendNotice(ctx context.Context, orgID, kind, subject, title, body string) {
	if err := s.queueNotice(ctx, orgID, kind, subject, title, body); err != nil {
		slog.ErrorContext(ctx, "billing could not queue a notice", "org", orgID, "kind", kind, "error", err)
	}
}

// queueNotice queues one notice and says whether it was queued, for a
// caller that must try again when it wasn't. The subject makes a notice
// queued twice reach the admins once.
func (s *Service) queueNotice(ctx context.Context, orgID, kind, subject, title, body string) error {
	if s.notify == nil {
		return nil
	}
	return s.notify(ctx, notifications.Notice{Kind: kind, OrgID: orgID, SubjectID: subject,
		Title: title, Body: body, Path: noticePath})
}

// periodKey is a month as notice subjects name it: 2026-11.
func periodKey(month time.Time) string { return month.UTC().Format("2006-01") }

// usd formats cents as dollars: 400 is "$4.00".
func usd(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s$%d.%02d", sign, cents/100, cents%100)
}

// notices sends what the month's spend calls for, as of one evaluation.
// Each one's subject makes it once a month (once a limit, for limit_80).
// While the limit is reached the credit notices wait: telling someone that
// usage goes on the next invoice would be wrong while it is stopped.
func (s *Service) notices(ctx context.Context, orgID string, month time.Time, limit *int64, sp PeriodSpend, reached bool) {
	if reached {
		return
	}
	switch {
	case sp.BaseUsageCents >= baseCreditCents:
		s.sendNotice(ctx, orgID, NoticeCredit100, "credit_100:"+periodKey(month),
			"This month's included usage is used",
			"Usage from now until the month ends is added to next month's invoice.")
	case sp.BaseUsageCents >= creditWarnCents:
		s.sendNotice(ctx, orgID, NoticeCredit80, "credit_80:"+periodKey(month),
			"80% of this month's included usage is used",
			fmt.Sprintf("This workspace has used %s of the %s of usage included this month. Usage past %s is added to next month's invoice.",
				usd(creditWarnCents), usd(baseCreditCents), usd(baseCreditCents)))
	}
	// A zero limit has no 80%: the credit notices speak for it.
	if limit != nil && *limit > 0 && sp.OverageCents > 0 && sp.OverageCents*5 >= *limit*4 {
		s.sendNotice(ctx, orgID, NoticeLimit80, fmt.Sprintf("limit_80:%s:%d", periodKey(month), *limit),
			"80% of the spending limit is used",
			fmt.Sprintf("This workspace has spent %s of its %s monthly spending limit on usage past what's included.",
				usd(sp.OverageCents), usd(*limit)))
	}
}

// sendLimited tells the admins the limit is reached. epoch is the limited
// transition's own number, so a second one in a month is told too.
func (s *Service) sendLimited(ctx context.Context, orgID string, month time.Time, epoch int64) {
	s.sendNotice(ctx, orgID, NoticeLimit100, fmt.Sprintf("limit_100:%s:%d", periodKey(month), epoch),
		"Spending limit reached",
		"Loomplane Cloud work in this workspace is stopped and AI on Loomplane's model keys is paused until the month ends or an owner raises the limit in Settings > Billing. Nothing on your own providers is touched.")
}

// sendRestored tells a workspace with no platform tenant that its limit
// is cleared. Platform Cloud tells everyone else when their work restarts.
func (s *Service) sendRestored(ctx context.Context, orgID string, epoch int64) {
	s.sendNotice(ctx, orgID, NoticeRestored, fmt.Sprintf("restored:%d", epoch),
		"Spending limit cleared",
		"AI on Loomplane's model keys is back on.")
}
