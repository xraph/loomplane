//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ledger"
	"github.com/xraph/ledger/coupon"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/types"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/pglock"
)

// MaxCreditCents is the most one manual credit can be: $1,000.00.
const MaxCreditCents = 100_000

// MaxCreditNote is the longest note a credit carries, in characters.
const MaxCreditNote = 500

// creditPrefix is the type prefix of a credit's id. The credit's coupon
// has the credit's id as its code, so a coupon whose code has this prefix
// is a credit's.
const creditPrefix = "lpc"

// GrantCredit creates a one-use amount coupon for cents and applies it to
// the workspace's cloud subscription. It discounts the next invoice
// generated (base included, never below zero) and is deleted after it.
//
// Everything happens under the workspace's credits lock, which the month
// close holds while it lists the credits an invoice uses and generates it:
// the coupon is applied first and the credit's row recorded after, so the
// close never lists a credit whose coupon isn't applied, and never meets a
// coupon applied with no row unless a grant failed part way. That coupon
// is deleted instead of discounting anything (dropOrphanCredits). The
// coupon's code is the credit's own id, never anything the caller sent.
//
// A workspace with no cloud subscription, or one that has ended, is
// ErrNeedsPayment: there is no invoice to credit. by names who granted it.
func (s *Service) GrantCredit(ctx context.Context, orgID string, cents int64, note, by string) error {
	if err := s.ready(); err != nil {
		return err
	}
	note, by = strings.TrimSpace(note), strings.TrimSpace(by)
	switch {
	case orgID == "":
		return errors.New("billing: a credit needs a workspace")
	case cents <= 0 || cents > MaxCreditCents:
		return fmt.Errorf("billing: a credit is from 1 to %d cents", MaxCreditCents)
	case by == "":
		return errors.New("billing: a credit needs to say who granted it")
	case utf8.RuneCountInString(note) > MaxCreditNote:
		return fmt.Errorf("billing: a credit's note is at most %d characters", MaxCreditNote)
	}
	return pglock.Do(ctx, s.pg, creditsLock+orgID, func(ctx context.Context) error {
		sid, err := s.creditableSubscription(ctx, orgID)
		if err != nil {
			return err
		}
		if dropErr := s.dropOrphanCredits(ctx, orgID, sid); dropErr != nil {
			return dropErr
		}
		tid, err := typeid.Generate(creditPrefix)
		if err != nil {
			return fmt.Errorf("billing: make a credit id: %w", err)
		}
		creditID := tid.String()
		c := &coupon.Coupon{
			Code: creditID, Name: "Credit", Type: coupon.CouponTypeAmount, Amount: types.USD(cents),
			Currency: currency, AppID: s.appID, MaxRedemptions: 1,
		}
		if err = s.l.CreateCoupon(ctx, c); err != nil {
			return fmt.Errorf("billing: make the coupon of credit %s for %s: %w", creditID, orgID, err)
		}
		if _, err = s.l.ApplyCoupon(ctx, sid, creditID); err != nil {
			s.dropCoupon(ctx, orgID, c.ID)
			return fmt.Errorf("billing: apply credit %s to %s: %w", creditID, orgID, err)
		}
		if _, err = s.pg.Exec(ctx, `INSERT INTO loomplane_billing_credits
			(id, org_id, subscription_id, coupon_id, cents, note, granted_by, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			creditID, orgID, sid.String(), c.ID.String(), cents, note, by, s.now().UTC()); err != nil {
			// Applied with no row, the coupon would discount every invoice
			// from now on: nothing would ever delete it.
			s.dropCoupon(ctx, orgID, c.ID)
			return fmt.Errorf("billing: record credit %s for %s: %w", creditID, orgID, err)
		}
		return nil
	})
}

// creditableSubscription is the workspace's cloud subscription when it is
// still running, or ErrNeedsPayment.
func (s *Service) creditableSubscription(ctx context.Context, orgID string) (id.SubscriptionID, error) {
	var subID string
	err := s.pg.QueryRow(ctx, `SELECT cloud_subscription_id FROM loomplane_billing_accounts WHERE org_id = $1`, orgID).Scan(&subID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && subID == "") {
		return id.SubscriptionID{}, ErrNeedsPayment
	}
	if err != nil {
		return id.SubscriptionID{}, fmt.Errorf("billing: read the cloud subscription of %s: %w", orgID, err)
	}
	live, err := s.subscriptionLive(ctx, subID)
	if err != nil {
		return id.SubscriptionID{}, err
	}
	if !live {
		return id.SubscriptionID{}, ErrNeedsPayment
	}
	sid, err := id.ParseSubscriptionID(subID)
	if err != nil {
		return id.SubscriptionID{}, fmt.Errorf("billing: read subscription id %q: %w", subID, err)
	}
	return sid, nil
}

// dropOrphanCredits deletes each credit coupon applied to the subscription
// that has no credit row: a grant that failed, or stopped, between applying
// it and recording it. The caller holds the workspace's credits lock, so no
// grant is between those steps now. Left alone, such a coupon would
// discount every invoice the subscription has.
func (s *Service) dropOrphanCredits(ctx context.Context, orgID string, sid id.SubscriptionID) error {
	applied, err := s.l.ListAppliedCoupons(ctx, sid)
	if err != nil {
		return fmt.Errorf("billing: list the coupons applied to %s: %w", sid, err)
	}
	for _, c := range applied {
		if !strings.HasPrefix(c.Code, creditPrefix+"_") {
			continue
		}
		var recorded bool
		if err = s.pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM loomplane_billing_credits WHERE coupon_id = $1)`,
			c.ID.String()).Scan(&recorded); err != nil {
			return fmt.Errorf("billing: read whether coupon %s is a recorded credit: %w", c.Code, err)
		}
		if recorded {
			continue
		}
		slog.ErrorContext(ctx, "billing: a credit's coupon was applied with no credit recorded; deleting it so it discounts nothing",
			"org", orgID, "subscription", sid.String(), "coupon", c.Code)
		if err = s.l.DeleteCoupon(ctx, c.ID); err != nil && !errors.Is(err, ledger.ErrCouponNotFound) {
			return fmt.Errorf("billing: delete the unrecorded credit coupon %s: %w", c.Code, err)
		}
	}
	return nil
}

// dropCoupon deletes a coupon a grant made and couldn't finish with. A
// delete that fails is logged; the next grant or close finds the coupon
// with no row and deletes it then.
func (s *Service) dropCoupon(ctx context.Context, orgID string, couponID id.CouponID) {
	if err := s.l.DeleteCoupon(context.WithoutCancel(ctx), couponID); err != nil && !errors.Is(err, ledger.ErrCouponNotFound) {
		slog.ErrorContext(ctx, "billing: deleting the coupon of a credit that failed didn't work; the next close deletes it",
			"org", orgID, "coupon", couponID.String(), "error", err)
	}
}
