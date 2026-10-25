package connections

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

// Provider is one cloud's sign-in: where to send someone, and the calls
// that turn a code into a grant and renew it.
type Provider interface {
	AuthorizeURL(app oauth.App, redirectURL, state string) string
	Exchange(ctx context.Context, app oauth.App, redirectURL, code string) (oauth.Grant, error)
	Refresh(ctx context.Context, app oauth.App, refreshToken string) (oauth.Grant, error)
}

// Where a sign-in app came from.
const (
	AppFromConfig       = "config"
	AppFromOrganization = "organization"
	AppFromNone         = "none"
)

// SignInApp says which app a cloud signs in with. It never carries a secret.
type SignInApp struct {
	Cloud     string     `json:"cloud"`
	Source    string     `json:"source"`
	ClientID  string     `json:"client_id,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// AppInput is an organization's own app for a cloud.
type AppInput struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// StoredApp is an organization's app as the database keeps it. The secret
// is in Vault.
type StoredApp struct {
	OrgID, Cloud, ClientID, UpdatedBy string
	CreatedAt, UpdatedAt              time.Time
}

// PendingSignIn is one sign-in someone has started and not finished.
type PendingSignIn struct {
	State, OrgID, UserID, Cloud, Name, ConnectionID string
	ExpiresAt, CreatedAt                            time.Time
}

// AppStore keeps sign-in apps and pending sign-ins.
type AppStore interface {
	GetApp(ctx context.Context, orgID, cloud string) (*StoredApp, error)
	PutApp(ctx context.Context, a *StoredApp) error
	DeleteApp(ctx context.Context, orgID, cloud string) error
	ListApps(ctx context.Context, orgID string) ([]*StoredApp, error)
	PutSignIn(ctx context.Context, p *PendingSignIn) error
	// TakeSignIn removes and returns a pending sign-in, so a state works
	// once. One already taken is ErrNotFound.
	TakeSignIn(ctx context.Context, state string) (*PendingSignIn, error)
}

// AppAuditor records a change to a sign-in app. The Auditor given to
// NewService may also implement it.
type AppAuditor interface {
	RecordApp(ctx context.Context, action string, p security.Principal, cloud, clientID string)
}

func (s *Service) recordApp(ctx context.Context, action string, p security.Principal, cloud, clientID string) {
	if a, ok := s.audit.(AppAuditor); ok {
		a.RecordApp(ctx, action, p, cloud, clientID)
	}
}

// SignInApps lists each cloud that can be signed in to and where its app
// comes from.
func (s *Service) SignInApps(ctx context.Context, p security.Principal) ([]SignInApp, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountRead, ""); err != nil {
		return nil, err
	}
	stored, err := s.Apps.ListApps(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	byCloud := map[string]*StoredApp{}
	for _, a := range stored {
		byCloud[a.Cloud] = a
	}
	out := []SignInApp{}
	for _, cloud := range slices.Sorted(maps.Keys(s.Providers)) {
		entry := SignInApp{Cloud: cloud, Source: AppFromNone}
		if cfg, ok := s.ConfigApps[cloud]; ok {
			entry.Source, entry.ClientID = AppFromConfig, cfg.ClientID
		} else if a, ok := byCloud[cloud]; ok {
			t := a.UpdatedAt.UTC()
			entry.Source, entry.ClientID, entry.UpdatedBy, entry.UpdatedAt = AppFromOrganization, a.ClientID, a.UpdatedBy, &t
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *Service) provider(cloud string) error {
	if _, ok := s.Providers[cloud]; !ok {
		return fmt.Errorf("%w: Loomplane has no sign-in for %q", ErrInvalid, cloud)
	}
	return nil
}

// SetSignInApp saves the organization's own app for a cloud. The secret
// goes to Vault and the client ID to the database.
func (s *Service) SetSignInApp(ctx context.Context, p security.Principal, cloud string, in AppInput) (*SignInApp, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	if err := s.provider(cloud); err != nil {
		return nil, err
	}
	if _, ok := s.ConfigApps[cloud]; ok {
		return nil, ErrAppLocked
	}
	clientID, secret := strings.TrimSpace(in.ClientID), strings.TrimSpace(in.ClientSecret)
	if clientID == "" || secret == "" {
		return nil, fmt.Errorf("%w: give the app's client ID and client secret", ErrInvalid)
	}
	// The secret first, like a connection's: a row without its secret
	// would look set up and fail every sign-in.
	if err := s.AppSecrets.Put(ctx, p.OrgID, cloud, []byte(secret)); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	app := &StoredApp{OrgID: p.OrgID, Cloud: cloud, ClientID: clientID, UpdatedBy: p.UserID, CreatedAt: now, UpdatedAt: now}
	if err := s.Apps.PutApp(ctx, app); err != nil {
		return nil, err
	}
	s.recordApp(ctx, "cloud_signin_app.set", p, cloud, clientID)
	return &SignInApp{Cloud: cloud, Source: AppFromOrganization, ClientID: clientID, UpdatedBy: p.UserID, UpdatedAt: &now}, nil
}

// DeleteSignInApp removes the organization's own app for a cloud.
func (s *Service) DeleteSignInApp(ctx context.Context, p security.Principal, cloud string) error {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return err
	}
	if err := s.provider(cloud); err != nil {
		return err
	}
	if _, ok := s.ConfigApps[cloud]; ok {
		return ErrAppLocked
	}
	stored, err := s.Apps.GetApp(ctx, p.OrgID, cloud)
	if err != nil {
		return err
	}
	if err := s.Apps.DeleteApp(ctx, p.OrgID, cloud); err != nil {
		return err
	}
	if err := s.AppSecrets.Delete(ctx, p.OrgID, cloud); err != nil {
		s.warn("remove the secret of a deleted sign-in app", err)
	}
	s.recordApp(ctx, "cloud_signin_app.delete", p, cloud, stored.ClientID)
	return nil
}

// app is the app to sign in to a cloud with: configuration's first, then
// the organization's.
func (s *Service) app(ctx context.Context, orgID, cloud string) (oauth.App, error) {
	if cfg, ok := s.ConfigApps[cloud]; ok {
		return cfg, nil
	}
	stored, err := s.Apps.GetApp(ctx, orgID, cloud)
	if err == ErrNotFound {
		return oauth.App{}, ErrNoApp
	}
	if err != nil {
		return oauth.App{}, err
	}
	secret, err := s.AppSecrets.Reveal(ctx, orgID, cloud)
	if err == ErrNotFound {
		return oauth.App{}, ErrNoApp
	}
	if err != nil {
		return oauth.App{}, err
	}
	return oauth.App{ClientID: stored.ClientID, ClientSecret: string(secret)}, nil
}
