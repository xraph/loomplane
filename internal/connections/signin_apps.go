package connections

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// Provider is one cloud's sign-in: where to send someone, and the calls
// that turn a code into a grant, renew it, and ask whether the cloud
// still honors it. Probe renews nothing; a grant the cloud no longer
// honors is an error wrapping oauth.ErrRefused.
type Provider interface {
	AuthorizeURL(app oauth.App, r oauth.AuthorizeRequest) string
	Exchange(ctx context.Context, app oauth.App, r oauth.CodeRequest) (oauth.Grant, error)
	Refresh(ctx context.Context, app oauth.App, g oauth.Grant) (oauth.Grant, error)
	Probe(ctx context.Context, app oauth.App, g oauth.Grant) error
}

// Handout is a Provider whose cloud sessions renew themselves. Its
// deployments and checks get a credential built from the grant, such as
// a key that carries the refresh token, instead of an access token that
// would run out mid-deployment. settings are the connection's details.
type Handout interface {
	Handout(app oauth.App, g oauth.Grant, settings map[string]string) ([]byte, error)
}

// AppIndependent is a Provider whose grants keep working whatever app is
// configured, such as Vercel's, which never expire and carry no refresh
// token. Its connections stay usable when the app changes or goes.
type AppIndependent interface {
	GrantOutlivesApp() bool
}

// Where a sign-in app came from.
const (
	AppFromConfig   = "config"
	AppFromPlatform = "platform"
	AppFromNone     = "none"
)

// SignInApp says which app a cloud signs in with. It never carries a secret.
type SignInApp struct {
	Cloud     string     `json:"cloud"`
	Source    string     `json:"source"`
	ClientID  string     `json:"client_id,omitempty"`
	Slug      string     `json:"slug,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	// Problem is why the cloud last refused the app, until it's saved
	// again or a sign-in with it works.
	Problem string `json:"problem,omitempty"`
	// Connections and Organizations count the sign-ins made with this app
	// that still work, and the organizations they belong to. They are 0
	// for a cloud whose grants outlive its app.
	Connections   int `json:"connections"`
	Organizations int `json:"organizations"`
}

// AppInput is the platform's app for a cloud. Slug is the app's
// name in its install address, which only Vercel needs.
type AppInput struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Slug         string `json:"slug,omitempty"`
}

// StoredApp is the platform's app for a cloud as the database keeps it.
// The secret is in Vault.
type StoredApp struct {
	Cloud, ClientID, UpdatedBy string
	// Slug is the app's name in its cloud's own address. Only Vercel's
	// has one.
	Slug                 string
	CreatedAt, UpdatedAt time.Time
}

// PendingSignIn is one sign-in someone has started and not finished.
type PendingSignIn struct {
	State, OrgID, UserID, Cloud, Name, ConnectionID string
	// Settings are the choices made before signing in, such as a region.
	Settings map[string]string
	// Verifier is the PKCE secret that proves the callback belongs to
	// the browser that started the sign-in.
	Verifier             string
	ExpiresAt, CreatedAt time.Time
}

// AppSecrets keeps the platform's sign-in app secrets, one per cloud.
type AppSecrets interface {
	Put(ctx context.Context, cloud string, secret []byte) error
	Reveal(ctx context.Context, cloud string) ([]byte, error)
	// Delete removes a secret. One already gone is not an error.
	Delete(ctx context.Context, cloud string) error
}

// AppStore keeps sign-in apps and pending sign-ins. An app belongs to the
// whole platform, one per cloud.
type AppStore interface {
	GetApp(ctx context.Context, cloud string) (*StoredApp, error)
	PutApp(ctx context.Context, a *StoredApp) error
	DeleteApp(ctx context.Context, cloud string) error
	ListApps(ctx context.Context) ([]*StoredApp, error)
	PutSignIn(ctx context.Context, p *PendingSignIn) error
	// TakeSignIn removes and returns a pending sign-in, so a state works
	// once. One already taken is ErrNotFound.
	TakeSignIn(ctx context.Context, state string) (*PendingSignIn, error)
	// SweepSignIns removes sign-ins that expired before the given time and
	// says how many.
	SweepSignIns(ctx context.Context, before time.Time) (int64, error)
	// SetAppProblem records why the platform's app for a cloud stopped
	// working, replacing any earlier message. A cloud with no app has
	// nothing to record on, so that is a no-op.
	SetAppProblem(ctx context.Context, cloud, message string, at time.Time) error
	ClearAppProblem(ctx context.Context, cloud string) error
	// SignedInWith counts the working sign-ins made with the app whose
	// client ID is clientID, and the organizations they belong to. It reads
	// across every organization.
	SignedInWith(ctx context.Context, cloud, clientID string) (connections, orgs int, err error)
	// AppProblems maps each cloud with a problem to its message.
	AppProblems(ctx context.Context) (map[string]string, error)
	// PutReservation holds a keyless connection's ID. An ID already held
	// is an error.
	PutReservation(ctx context.Context, r *Reservation) error
	// TakeReservation removes and returns orgID's reservation of id, so an
	// ID is used once. One already taken, or another organization's, is
	// ErrNotFound, and another organization's is left as it was.
	TakeReservation(ctx context.Context, orgID, id string) (*Reservation, error)
	// SweepReservations removes reservations that expired before the
	// given time and says how many.
	SweepReservations(ctx context.Context, before time.Time) (int64, error)
}

// AppAuditor records a change to a sign-in app. The Auditor given to
// NewService may also implement it.
type AppAuditor interface {
	RecordApp(ctx context.Context, action, userID, cloud, clientID string)
}

func (s *Service) recordApp(ctx context.Context, action, userID, cloud, clientID string) {
	if a, ok := s.audit.(AppAuditor); ok {
		a.RecordApp(ctx, action, userID, cloud, clientID)
	}
}

// SignInApps lists each cloud that can be signed in to and where its app
// comes from, with how many sign-ins work with it. Who may see it is the
// caller's to decide.
func (s *Service) SignInApps(ctx context.Context) ([]SignInApp, error) {
	return s.signInApps(ctx, true)
}

// signInApps is SignInApps, counting the sign-ins only when withCounts. The
// counts read across every organization, so only the platform's own list
// asks for them.
func (s *Service) signInApps(ctx context.Context, withCounts bool) ([]SignInApp, error) {
	stored, err := s.Apps.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	problems, err := s.Apps.AppProblems(ctx)
	if err != nil {
		return nil, err
	}
	byCloud := map[string]*StoredApp{}
	for _, a := range stored {
		byCloud[a.Cloud] = a
	}
	out := []SignInApp{}
	for _, cloud := range slices.Sorted(maps.Keys(s.Providers)) {
		entry := SignInApp{Cloud: cloud, Source: AppFromNone, Problem: problems[cloud]}
		if cfg, ok := s.ConfigApps[cloud]; ok {
			entry.Source, entry.ClientID, entry.Slug = AppFromConfig, cfg.ClientID, cfg.Slug
		} else if a, ok := byCloud[cloud]; ok {
			t := a.UpdatedAt.UTC()
			entry.Source, entry.ClientID, entry.Slug, entry.UpdatedBy, entry.UpdatedAt = AppFromPlatform, a.ClientID, a.Slug, a.UpdatedBy, &t
		}
		if withCounts && entry.ClientID != "" && !outlivesApp(s.Providers[cloud]) {
			if entry.Connections, entry.Organizations, err = s.Apps.SignedInWith(ctx, cloud, entry.ClientID); err != nil {
				return nil, err
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func outlivesApp(p Provider) bool {
	a, ok := p.(AppIndependent)
	return ok && a.GrantOutlivesApp()
}

// SignInClouds says which clouds can be signed in to: those with an app,
// from configuration or the platform.
func (s *Service) SignInClouds(ctx context.Context) (map[string]bool, error) {
	apps, err := s.signInApps(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(apps))
	for _, a := range apps {
		out[a.Cloud] = a.Source != AppFromNone
	}
	return out, nil
}

func (s *Service) provider(cloud string) error {
	if _, ok := s.Providers[cloud]; !ok {
		return fmt.Errorf("%w: Loomplane has no sign-in for %s", ErrInvalid, cloudTitle(cloud))
	}
	return nil
}

// SetSignInApp saves the platform's app for a cloud, on behalf of userID.
// The secret goes to Vault and the client ID to the database. Saving it
// clears any problem the cloud had with the app before. Whether userID may
// is the caller's to check.
func (s *Service) SetSignInApp(ctx context.Context, userID, cloud string, in AppInput) (*SignInApp, error) {
	if err := s.provider(cloud); err != nil {
		return nil, err
	}
	if _, ok := s.ConfigApps[cloud]; ok {
		return nil, ErrAppLocked
	}
	clientID, secret, slug := strings.TrimSpace(in.ClientID), strings.TrimSpace(in.ClientSecret), strings.TrimSpace(in.Slug)
	if clientID == "" || secret == "" {
		return nil, fmt.Errorf("%w: give the app's client ID and client secret", ErrInvalid)
	}
	if cloud != Vercel {
		slug = ""
	} else if slug == "" {
		return nil, fmt.Errorf("%w: give the Vercel integration's slug, the name in its vercel.com/integrations address", ErrInvalid)
	}
	// The secret first, like a connection's: a row without its secret
	// would look set up and fail every sign-in.
	if err := s.AppSecrets.Put(ctx, cloud, []byte(secret)); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	app := &StoredApp{Cloud: cloud, ClientID: clientID, Slug: slug, UpdatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if err := s.Apps.PutApp(ctx, app); err != nil {
		return nil, err
	}
	// The app is saved either way, so a problem left behind only shows a
	// stale message until the next sign-in clears it.
	if err := s.Apps.ClearAppProblem(ctx, cloud); err != nil {
		s.warn("clear the problem of a saved sign-in app", err)
	}
	s.recordApp(ctx, "cloud_signin_app.set", userID, cloud, clientID)
	return &SignInApp{Cloud: cloud, Source: AppFromPlatform, ClientID: clientID, Slug: slug, UpdatedBy: userID, UpdatedAt: &now}, nil
}

// DeleteSignInApp removes the platform's app for a cloud, and any problem
// the cloud had with it. Whether userID may is the caller's to check.
func (s *Service) DeleteSignInApp(ctx context.Context, userID, cloud string) error {
	if err := s.provider(cloud); err != nil {
		return err
	}
	if _, ok := s.ConfigApps[cloud]; ok {
		return ErrAppLocked
	}
	stored, err := s.Apps.GetApp(ctx, cloud)
	if err != nil {
		return err
	}
	if err := s.Apps.DeleteApp(ctx, cloud); err != nil {
		return err
	}
	if err := s.AppSecrets.Delete(ctx, cloud); err != nil {
		s.warn("remove the secret of a deleted sign-in app", err)
	}
	// The app is gone either way, so a problem left behind only shows a
	// stale message until the next app is saved.
	if err := s.Apps.ClearAppProblem(ctx, cloud); err != nil {
		s.warn("clear the problem of a deleted sign-in app", err)
	}
	s.recordApp(ctx, "cloud_signin_app.delete", userID, cloud, stored.ClientID)
	return nil
}

// app is the app to sign in to a cloud with: configuration's first, then
// the platform's.
func (s *Service) app(ctx context.Context, cloud string) (oauth.App, error) {
	if cfg, ok := s.ConfigApps[cloud]; ok {
		return cfg, nil
	}
	stored, err := s.Apps.GetApp(ctx, cloud)
	if errors.Is(err, ErrNotFound) {
		return oauth.App{}, ErrNoApp
	}
	if err != nil {
		return oauth.App{}, err
	}
	secret, err := s.AppSecrets.Reveal(ctx, cloud)
	if errors.Is(err, ErrNotFound) {
		return oauth.App{}, ErrNoApp
	}
	if err != nil {
		return oauth.App{}, err
	}
	return oauth.App{ClientID: stored.ClientID, ClientSecret: string(secret), Slug: stored.Slug}, nil
}

// connectionApp is the app to renew or hand out c's sign-in with. A grant
// only works with the client that issued it, so a connection signed in with
// another app, or with none on record, is ErrAppChanged. A cloud whose
// grants outlive its app gets the current app, or an empty one.
func (s *Service) connectionApp(ctx context.Context, c *Connection) (oauth.App, error) {
	app, err := s.app(ctx, c.Cloud)
	if outlivesApp(s.Providers[c.Cloud]) {
		if errors.Is(err, ErrNoApp) {
			return oauth.App{}, nil
		}
		return app, err
	}
	if err != nil {
		return oauth.App{}, err
	}
	if c.OAuthClientID != app.ClientID {
		return oauth.App{}, ErrAppChanged
	}
	return app, nil
}

// appRejected records that cloud refused the sign-in app clientID, for the
// settings page to show, and returns the message saying so. It records
// only if clientID is the platform's current app, so a refusal for an app
// since replaced blames nothing. The record is kept even if the caller
// gives up.
func (s *Service) appRejected(ctx context.Context, cloud, clientID string) string {
	why := fmt.Sprintf("Loomplane's %s sign-in app was refused. Whoever runs Loomplane needs to check its client ID and secret.", cloudTitle(cloud))
	// A config app has no row of its own, and a platform row under it is
	// not the app being refused, so nothing is recorded.
	if _, ok := s.ConfigApps[cloud]; ok {
		return why
	}
	current, err := s.Apps.GetApp(ctx, cloud)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.warn("read the sign-in app being refused", err)
		}
		return why
	}
	if current.ClientID != clientID {
		return why
	}
	if err := s.Apps.SetAppProblem(context.WithoutCancel(ctx), cloud, why, s.now().UTC()); err != nil {
		s.warn("record the problem with a sign-in app", err)
	}
	return why
}
