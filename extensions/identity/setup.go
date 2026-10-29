package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/authsome/account"
	authid "github.com/xraph/authsome/id"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/organization"
	"github.com/xraph/authsome/rbac"
	"github.com/xraph/authsome/session"
	authstore "github.com/xraph/authsome/store"
	"github.com/xraph/authsome/user"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/instance"
	"github.com/xraph/loomplane/internal/pglock"
)

const (
	// errSetupDone is the refusal once the instance has a user.
	errSetupDone = "setup has already been completed"
	// maxOrgName is the longest first organization name setup takes.
	maxOrgName = 64
	// setupRateLimit is how many setup attempts one address gets a minute.
	setupRateLimit = 5
	// setupTimeout bounds a setup, the wait for the lock included. Setup
	// runs to the end (or rolls back) even if the browser gives up.
	setupTimeout = time.Minute
	// fallbackSlug names an organization whose name has no letters or
	// digits to make a slug from.
	fallbackSlug = "organization"
)

// SetupStatus says whether the instance still waits for its first account.
type SetupStatus struct {
	Pending bool `json:"pending"`
}

// SetupRequest is the body of POST /api/v1/setup: the first account and
// its organization.
type SetupRequest struct {
	Name             string `json:"name"`
	Email            string `json:"email"`
	Password         string `json:"password"`
	OrganizationName string `json:"organization_name"`
}

// SetupResponse is what a finished setup returns. The session itself is in
// the cookie it sets.
type SetupResponse struct {
	OK bool `json:"ok"`
	// VerificationRequired means the account can't sign in again until its
	// email is verified.
	VerificationRequired bool `json:"verification_required"`
}

// setupOrgHook wraps the organization step of setup; create is the real one.
type setupOrgHook func(ctx context.Context, o *organization.Organization, create func(context.Context, *organization.Organization) error) error

type setupKey struct{}

// withSetup marks ctx as setup's own sign-up, which sign-up rules let
// through while the instance has no users.
func withSetup(ctx context.Context) context.Context {
	return context.WithValue(ctx, setupKey{}, true)
}

// isSetup reports whether ctx is setup's own sign-up.
func isSetup(ctx context.Context) bool {
	on, ok := ctx.Value(setupKey{}).(bool)
	return ok && on
}

func (e *Extension) setupRoutes(r forge.Router) error {
	if err := r.GET("/api/v1/setup", e.setupStatus,
		forge.WithName("setup.status"),
		forge.WithSummary("Whether this instance still waits for its first account"),
		forge.WithTags("Identity"),
		forge.WithResponseSchema(http.StatusOK, "Setup status", SetupStatus{}),
	); err != nil {
		return err
	}
	opts := []forge.RouteOption{
		forge.WithName("setup.complete"),
		forge.WithSummary("Create the first account, make it the platform owner, and create its organization"),
		forge.WithTags("Identity"),
		forge.WithRequestSchema(SetupRequest{}),
		forge.WithResponseSchema(http.StatusOK, "Setup is done and the session cookie is set", SetupResponse{}),
		forge.WithoutEntity(),
	}
	// The same shared limiter as authsome's own sign-in and sign-up, keyed
	// by this route and the caller's address.
	if limiter := e.engine.RateLimiter(); limiter != nil {
		cfg := e.engine.RateLimitMiddlewareConfig(setupRateLimit)
		cfg.Window = time.Minute
		opts = append(opts, forge.WithMiddleware(rateLimitScope, authmw.RateLimit(limiter, cfg)))
	}
	return r.POST("/api/v1/setup", e.completeSetup, opts...)
}

func (e *Extension) setupStatus(c forge.Context) error {
	return c.JSON(http.StatusOK, SetupStatus{Pending: e.setupPending(c.Request().Context())})
}

// setupPending reports whether Loomplane's app has no users yet. When the
// count fails it says no, so the dashboard falls back to sign-in rather
// than offering setup on an instance that may have an owner.
func (e *Extension) setupPending(ctx context.Context) bool {
	has, err := e.hasUsers(ctx)
	if err != nil {
		e.Logger().Error("count users for setup", forge.F("error", err.Error()))
		return false
	}
	return !has
}

func (e *Extension) hasUsers(ctx context.Context) (bool, error) {
	appID, err := authid.ParseAppID(e.cfg.Identity.AppID)
	if err != nil {
		return false, fmt.Errorf("identity.app_id: %w", err)
	}
	list, err := e.engine.AdminListUsers(ctx, &user.Query{AppID: appID, Limit: 1})
	if err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	return list.Total > 0, nil
}

// waitlistOn reports whether sign-up needs an invitation or an approved
// waitlist entry. The config value wins when it's set; otherwise the
// instance's stored setting decides. A setting that can't be read counts
// as on, so a database hiccup never opens sign-up.
func (e *Extension) waitlistOn(ctx context.Context) bool {
	on, stored, err := e.instance.Waitlist(ctx)
	if err != nil {
		e.Logger().Error("read the waitlist setting", forge.F("error", err.Error()))
		on, stored = true, true
	}
	return instance.EffectiveWaitlist(e.cfg.Identity.Waitlist, on, stored)
}

// validSetup trims the form and checks it, returning the parsed email
// address.
func validSetup(in *SetupRequest) (string, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.OrganizationName = strings.TrimSpace(in.OrganizationName)
	switch {
	case in.Name == "":
		return "", errors.New("your name is required")
	case strings.TrimSpace(in.Email) == "":
		return "", errors.New("your email is required")
	case in.Password == "":
		return "", errors.New("a password is required")
	case in.OrganizationName == "":
		return "", errors.New("the organization's name is required")
	case utf8.RuneCountInString(in.OrganizationName) > maxOrgName:
		return "", fmt.Errorf("the organization's name can be at most %d characters", maxOrgName)
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(in.Email))
	if err != nil {
		return "", errors.New("that isn't an email address")
	}
	return addr.Address, nil
}

// splitName splits a full name at its first space, the way authsome's own
// setup does.
func splitName(name string) (first, last string) {
	first, last, _ = strings.Cut(strings.TrimSpace(name), " ")
	return first, strings.TrimSpace(last)
}

// orgSlug makes an organization's slug from its name: lowercase letters
// and digits, with a single dash for each run of anything else.
func orgSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	if b.Len() == 0 {
		return fallbackSlug
	}
	return b.String()
}

// completeSetup creates the instance's first account. It succeeds once: the
// user count is checked again under an advisory lock, so of two setups that
// race, the second finds the first one's user and is refused.
func (e *Extension) completeSetup(c forge.Context) error {
	var in SetupRequest
	if err := c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON setup form")
	}
	email, err := validSetup(&in)
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	// Checked before the lock too, so a set-up instance answers at once.
	if !e.setupPending(c.Request().Context()) {
		return forge.NewHTTPError(http.StatusForbidden, errSetupDone)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), setupTimeout)
	defer cancel()
	var out SetupResponse
	var sess *session.Session
	err = pglock.Do(ctx, e.pg, pglock.FirstRunSetup, func(ctx context.Context) error {
		has, cerr := e.hasUsers(ctx)
		if cerr != nil {
			return cerr
		}
		if has {
			return forge.NewHTTPError(http.StatusForbidden, errSetupDone)
		}
		r := c.Request()
		var serr error
		sess, out, serr = e.setUp(ctx, &account.SignUpRequest{
			Email: email, Password: in.Password,
			IPAddress: authmw.ClientIP(r), UserAgent: r.UserAgent(),
		}, in.Name, in.OrganizationName)
		return serr
	})
	if err != nil && sess != nil && out.OK {
		// Setup finished and only letting go of the lock failed. Postgres
		// drops the lock with the connection, so this is no reason to
		// leave the new owner signed out.
		e.Logger().Error("release the setup lock", forge.F("error", err.Error()))
		err = nil
	}
	if err != nil {
		return err
	}
	e.cookieSetter()(c, sess.Token, e.sessionMaxAge())
	return c.JSON(http.StatusOK, out)
}

// setUp does setup's writes. Once the user exists, any failure removes
// everything made so far, so the instance is back to waiting for setup
// rather than holding an account nobody can finish.
func (e *Extension) setUp(ctx context.Context, req *account.SignUpRequest, name, orgName string) (*session.Session, SetupResponse, error) {
	appID, err := authid.ParseAppID(e.cfg.Identity.AppID)
	if err != nil {
		return nil, SetupResponse{}, fmt.Errorf("identity.app_id: %w", err)
	}
	req.AppID = appID
	req.FirstName, req.LastName = splitName(name)
	u, sess, err := e.engine.SignUp(withSetup(ctx), req)
	if err != nil {
		if errors.Is(err, account.ErrWeakPassword) {
			return nil, SetupResponse{}, forge.NewHTTPError(http.StatusBadRequest, strings.TrimPrefix(err.Error(), "account: "))
		}
		// Sign-up can fail after it has stored the user, when it opens the
		// session. Remove such a user too. The lock is held and the app had
		// no users, so a user with this email can only be the one just made.
		if left, lerr := e.store.GetUserByEmail(ctx, appID, authid.Nil, req.Email); lerr == nil {
			e.undoSetup(ctx, setupMade{user: left.ID})
		}
		return nil, SetupResponse{}, fmt.Errorf("sign up the first account: %w", err)
	}
	made := setupMade{user: u.ID}
	if err = e.finishSetup(ctx, appID, u, sess, orgName, &made); err != nil {
		e.undoSetup(ctx, made)
		return nil, SetupResponse{}, err
	}
	if err := e.audit.Info(ctx, "instance.setup", "user", u.ID.String()).
		Category("instance").AppID(e.cfg.Identity.AppID).UserID(u.ID.String()).Record(); err != nil {
		e.Logger().Error("record audit event", forge.F("action", "instance.setup"), forge.F("error", err.Error()))
	}
	return sess, SetupResponse{
		OK:                   true,
		VerificationRequired: e.cfg.Identity.RequireEmailVerification && !u.EmailVerified,
	}, nil
}

// setupMade is what a setup has created so far, for undoSetup.
type setupMade struct {
	user  authid.UserID
	role  *rbac.Role
	orgID authid.OrgID
}

// finishSetup does what follows sign-up: the platform role, the
// organization (which the session then acts in) and the stored waitlist.
func (e *Extension) finishSetup(ctx context.Context, appID authid.AppID, u *user.User, sess *session.Session, orgName string, made *setupMade) error {
	role, err := e.engine.GetRoleBySlug(ctx, appID, rbac.PlatformOwnerSlug)
	if err != nil {
		return fmt.Errorf("find the %s role: %w", rbac.PlatformOwnerSlug, err)
	}
	if err = e.engine.AssignUserRole(ctx, appID, &rbac.UserRole{UserID: u.ID.String(), RoleID: role.ID}); err != nil {
		return fmt.Errorf("make the first account %s: %w", rbac.PlatformOwnerSlug, err)
	}
	made.role = role
	o := &organization.Organization{
		ID: authid.NewOrgID(), AppID: appID, EnvID: u.EnvID,
		Name: orgName, Slug: orgSlug(orgName), CreatedBy: u.ID,
	}
	// Recorded before the attempt: a create that fails part way can still
	// leave the organization behind.
	made.orgID = o.ID
	if e.createOrgHook != nil {
		err = e.createOrgHook(ctx, o, e.orgs.CreateOrganization)
	} else {
		err = e.orgs.CreateOrganization(ctx, o)
	}
	if err != nil {
		return fmt.Errorf("create the first organization: %w", err)
	}
	if _, err = e.engine.SwitchActiveOrg(ctx, sess.ID, o.ID); err != nil {
		return fmt.Errorf("select the first organization: %w", err)
	}
	// A new instance starts closed; the config value can still open it.
	return e.instance.SetWaitlist(ctx, true)
}

// undoSetup removes what a failed setup made. Each step is tried even if
// one before it fails; failures are logged, since the caller is already
// returning the error that started this.
func (e *Extension) undoSetup(ctx context.Context, made setupMade) {
	logErr := func(step string, err error) {
		if err != nil {
			e.Logger().Error("undo a failed setup", forge.F("step", step), forge.F("user_id", made.user.String()), forge.F("error", err.Error()))
		}
	}
	if !made.orgID.IsNil() {
		if _, err := e.orgs.GetOrganization(ctx, made.orgID); err == nil {
			logErr("delete organization", e.orgs.DeleteOrganization(ctx, made.orgID))
		} else if !errors.Is(err, authstore.ErrNotFound) {
			logErr("find organization", err)
		}
	}
	appID, err := authid.ParseAppID(e.cfg.Identity.AppID)
	if err != nil {
		logErr("parse app id", err)
		return
	}
	if made.role != nil {
		// Through Warden, where the engine put it: the engine's own
		// unassign wants an authsome role ID, and Warden's IDs aren't.
		logErr("unassign "+rbac.PlatformOwnerSlug, e.platformRoles.UnassignUserRole(ctx, appID.String(), made.user.String(), made.role.ID))
	}
	// Ends the user's sessions and marks the user deleted, which is what
	// takes the instance back to pending.
	logErr("delete user", e.engine.AdminDeleteUser(ctx, made.user, made.user))
	// authsome's delete leaves the user's address claimed, and a second
	// setup with the same email would then fail. Release it.
	_, err = e.pg.Exec(ctx, `UPDATE authsome_user_emails SET deleted_at = now(), updated_at = now()
WHERE user_id = $1 AND deleted_at IS NULL`, made.user.String())
	logErr("release email", err)
}

// sessionMaxAge is the session cookie's lifetime, as authsome's sign-in
// sets it: the session token's lifetime, an hour when unset.
func (e *Extension) sessionMaxAge() int {
	if ttl := int(e.engine.Config().Session.TokenTTL.Seconds()); ttl > 0 {
		return ttl
	}
	return int(time.Hour.Seconds())
}
