package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/xraph/authsome/account"
	"github.com/xraph/authsome/appclientconfig"
	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/plugin"
	"github.com/xraph/authsome/plugins/waitlist"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/pglock"
)

// signupPeekLimit is how much of a sign-up body refuseSignUps reads to find
// the email. A body longer than this goes to authsome unread, and the gate
// still checks it inside the engine.
const signupPeekLimit = 64 << 10

const (
	// signupCheckTimeout bounds one sign-up check, queueing included.
	signupCheckTimeout = 5 * time.Second
	// maxSignupCheckSlots caps signupCheckSlots on a large pool.
	maxSignupCheckSlots = 8
)

// signupRefusalError is a sign-up the gate turns away. It answers 403 in the
// same envelope as authsome's coded errors, so a client can branch on its
// type rather than its wording.
type signupRefusalError struct {
	kind    string
	message string
}

func (r *signupRefusalError) Error() string   { return r.message }
func (r *signupRefusalError) StatusCode() int { return http.StatusForbidden }
func (r *signupRefusalError) ResponseBody() any {
	return map[string]any{"error": r.message, "code": http.StatusForbidden, "type": r.kind}
}

var (
	// errSetupPending refuses every sign-up but setup's own while the
	// instance has no users.
	errSetupPending = &signupRefusalError{
		kind:    "setup_pending",
		message: "Loomplane isn't set up yet. Open it in a browser to create the first account.",
	}
	// errWaitlist refuses a sign-up that has neither an invitation nor an
	// approved waitlist entry while the waitlist is on.
	errWaitlist = &signupRefusalError{
		kind:    "waitlist_required",
		message: "Sign-up needs an invitation or an approved waitlist entry.",
	}
)

// signupGate decides who may sign up. Setup's own sign-up always may. Any
// other waits until setup is done, and while the waitlist is on it also
// needs an open invitation or an approved waitlist entry for its email.
//
// authsome's waitlist plugin has a gate of its own, but it has no way to let
// setup or invited people through, so Loomplane keeps that one off and
// decides here.
type signupGate struct {
	pending, waitlistOn func(context.Context) bool
	approved, invited   func(ctx context.Context, email string) (bool, error)
	// outsideSetup runs judge unless first-run setup is running, and
	// reports whether it ran. Setup can't start while judge runs. Setup's
	// user exists before setup has made its organization or stored the
	// waitlist, so a sign-up judged in that window would see users and no
	// waitlist, and outlive a setup that then rolls back.
	outsideSetup func(ctx context.Context, judge func(context.Context) error) (bool, error)
	// slots caps the checks that run at once. Each holds a connection for
	// the setup lock while judge looks things up on others, so without a
	// cap a burst of sign-ups could hold every connection in the pool and
	// leave every judge waiting for one.
	slots chan struct{}
	// timeout bounds a check, the wait for a slot included. A check that
	// runs out refuses.
	timeout time.Duration
}

var (
	_ plugin.Plugin       = (*signupGate)(nil)
	_ plugin.BeforeSignUp = (*signupGate)(nil)
)

// Name implements plugin.Plugin.
func (g *signupGate) Name() string { return "loomplane-signup-gate" }

// OnBeforeSignUp implements plugin.BeforeSignUp. authsome runs it for every
// sign-up, whichever route or caller it comes from.
func (g *signupGate) OnBeforeSignUp(ctx context.Context, req *account.SignUpRequest) error {
	return g.check(ctx, req.Email)
}

func (g *signupGate) check(ctx context.Context, email string) error {
	if isSetup(ctx) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	case <-ctx.Done():
		return fmt.Errorf("wait for a free sign-up check: %w", ctx.Err())
	}
	var verdict error
	ran, err := g.outsideSetup(ctx, func(ctx context.Context) error {
		verdict = g.judge(ctx, email)
		return nil
	})
	if err != nil {
		return fmt.Errorf("check for a running setup: %w", err)
	}
	if !ran {
		return errSetupPending
	}
	// Once judged, setup can't start again: either the instance had users,
	// which setup refuses, or this sign-up was refused.
	return verdict
}

func (g *signupGate) judge(ctx context.Context, email string) error {
	if g.pending(ctx) {
		return errSetupPending
	}
	if !g.waitlistOn(ctx) {
		return nil
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return errWaitlist
	}
	ok, err := g.invited(ctx, email)
	if err != nil {
		return fmt.Errorf("look up invitations: %w", err)
	}
	if ok {
		return nil
	}
	if ok, err = g.approved(ctx, email); err != nil {
		return fmt.Errorf("look up the waitlist: %w", err)
	}
	if ok {
		return nil
	}
	return errWaitlist
}

// newSignupGate builds the gate on this extension's own lookups.
func (e *Extension) newSignupGate() *signupGate {
	return &signupGate{
		slots:      make(chan struct{}, signupCheckSlots(e.cfg.Database.PoolSize)),
		timeout:    signupCheckTimeout,
		pending:    e.signupPending,
		waitlistOn: e.waitlistOn,
		approved:   e.waitlistApproved,
		invited:    e.pendingInvitation,
		outsideSetup: func(ctx context.Context, judge func(context.Context) error) (bool, error) {
			return pglock.TryShared(ctx, e.pg, pglock.FirstRunSetup, judge)
		},
	}
}

// signupCheckSlots is how many sign-up checks may run at once on a pool of
// poolSize connections: a quarter of it, so the checks' lock connections
// and the lookups they wait on never take more than half, and at least
// one. A pool size of 0 is the driver's default, which is at least four.
func signupCheckSlots(poolSize int) int {
	if poolSize <= 0 {
		poolSize = 4
	}
	return min(max(poolSize/4, 1), maxSignupCheckSlots)
}

// signupPending is setupPending for the gate, which fails the other way:
// when the users can't be counted, sign-up is refused rather than let
// through on an instance that may still be waiting for setup.
func (e *Extension) signupPending(ctx context.Context) bool {
	has, err := e.hasUsers(ctx)
	if err != nil {
		e.Logger().Error("count users for sign-up", forge.F("error", err.Error()))
		return true
	}
	return !has
}

// waitlistApproved reports whether email has an approved entry on
// Loomplane's waitlist.
func (e *Extension) waitlistApproved(ctx context.Context, email string) (bool, error) {
	appID, err := authid.ParseAppID(e.cfg.Identity.AppID)
	if err != nil {
		return false, fmt.Errorf("identity.app_id: %w", err)
	}
	entry, err := e.waitlistStore.GetEntryByEmail(ctx, appID, strings.ToLower(email))
	if errors.Is(err, waitlist.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return entry.Status == waitlist.StatusApproved, nil
}

// refuseSignUps answers a sign-up the gate refuses with its 403 before
// authsome's handler runs. authsome turns any error from a BeforeSignUp
// hook into a 500, so the hook alone can't tell the browser why. The hook
// still makes the same check inside the engine, for every other way in.
func (e *Extension) refuseSignUps(next forge.Handler) forge.Handler {
	return func(c forge.Context) error {
		r := c.Request()
		if r.Method != http.MethodPost || path.Clean(r.URL.Path) != authsomeAPIPath+"/signup" {
			return next(c)
		}
		email, ok := peekEmail(r)
		if !ok {
			// authsome refuses a body without an email; the hook covers
			// anything it lets through.
			return next(c)
		}
		err := e.gate.check(r.Context(), email)
		var refusal *signupRefusalError
		if errors.As(err, &refusal) {
			return refusal
		}
		if err != nil {
			// A check that couldn't finish refuses too, and isn't run a
			// second time in the hook, which would double the load of a
			// burst that caused it.
			e.Logger().Warn("check a sign-up", forge.F("error", err.Error()))
			return forge.NewHTTPError(http.StatusServiceUnavailable, "sign-up is busy, try again in a moment")
		}
		return next(c)
	}
}

// peekEmail reads the email from a JSON sign-up body and puts the body back
// for the handler. It reports false when there's no email to read.
func peekEmail(r *http.Request) (string, bool) {
	if r.Body == nil || r.Body == http.NoBody {
		return "", false
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, signupPeekLimit))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	if err != nil {
		return "", false
	}
	var in struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(head, &in) != nil || strings.TrimSpace(in.Email) == "" {
		return "", false
	}
	return in.Email, true
}

// syncClientConfig sets the waitlist toggle in authsome's client config to
// what the gate enforces. authsome's sign-up and waitlist forms read that
// config, and with the waitlist plugin registered it would otherwise say
// the waitlist is on whatever Loomplane's setting is.
func (e *Extension) syncClientConfig(ctx context.Context) error {
	appID, err := authid.ParseAppID(e.cfg.Identity.AppID)
	if err != nil {
		return fmt.Errorf("identity.app_id: %w", err)
	}
	cfg, err := e.store.GetAppClientConfig(ctx, appID)
	if errors.Is(err, appclientconfig.ErrNotFound) {
		cfg, err = &appclientconfig.Config{AppID: appID}, nil
	}
	if err != nil {
		return fmt.Errorf("read authsome client config: %w", err)
	}
	on := e.waitlistOn(ctx)
	if cfg.WaitlistEnabled != nil && *cfg.WaitlistEnabled == on {
		return nil
	}
	cfg.WaitlistEnabled = &on
	if err := e.store.SetAppClientConfig(ctx, cfg); err != nil {
		return fmt.Errorf("write authsome client config: %w", err)
	}
	return nil
}
