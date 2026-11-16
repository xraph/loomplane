package connections

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// The health of a signed-in connection, as the instance console shows it.
// The connection's own status gives most of them; AppChanged is a sign-in
// whose app has since been replaced or removed, so its grant can't be
// renewed and someone has to sign in again.
const (
	SignInHealthy    = "healthy"
	SignInIncomplete = "incomplete"
	SignInFailed     = "failed"
	SignInNeeded     = "sign_in_needed"
	SignInAppChanged = "app_changed"
)

// SignInHealths lists the healths, worst first.
var SignInHealths = []string{SignInNeeded, SignInAppChanged, SignInFailed, SignInIncomplete, SignInHealthy}

// SignInCounts is how many signed-in connections are in each health.
type SignInCounts struct {
	Total        int `json:"total"`
	Healthy      int `json:"healthy"`
	Incomplete   int `json:"incomplete"`
	Failed       int `json:"failed"`
	SignInNeeded int `json:"signInNeeded"`
	AppChanged   int `json:"appChanged"`
}

// Add counts n connections of health h.
func (c *SignInCounts) Add(h string, n int) {
	c.Total += n
	switch h {
	case SignInHealthy:
		c.Healthy += n
	case SignInIncomplete:
		c.Incomplete += n
	case SignInFailed:
		c.Failed += n
	case SignInNeeded:
		c.SignInNeeded += n
	case SignInAppChanged:
		c.AppChanged += n
	}
}

// Merge adds o's counts to c.
func (c *SignInCounts) Merge(o SignInCounts) {
	c.Total += o.Total
	c.Healthy += o.Healthy
	c.Incomplete += o.Incomplete
	c.Failed += o.Failed
	c.SignInNeeded += o.SignInNeeded
	c.AppChanged += o.AppChanged
}

// Of is how many connections have health h, or the problems ("problems").
func (c SignInCounts) Of(h string) int {
	switch h {
	case "":
		return c.Total
	case "problems":
		return c.SignInNeeded + c.AppChanged + c.Failed
	case SignInHealthy:
		return c.Healthy
	case SignInIncomplete:
		return c.Incomplete
	case SignInFailed:
		return c.Failed
	case SignInNeeded:
		return c.SignInNeeded
	case SignInAppChanged:
		return c.AppChanged
	}
	return 0
}

// SignedInConnection is one signed-in connection, in any organization. It
// carries nothing secret.
type SignedInConnection struct {
	ID         string `json:"id"`
	OrgID      string `json:"orgId"`
	OrgName    string `json:"orgName,omitempty"`
	Cloud      string `json:"cloud"`
	Name       string `json:"name"`
	Account    string `json:"account,omitempty"`
	Health     string `json:"health"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	SignedInBy string `json:"signedInBy,omitempty"`
	// SignedInByEmail is who signed in, when their account still exists.
	SignedInByEmail string     `json:"signedInByEmail,omitempty"`
	CheckedAt       *time.Time `json:"checkedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// SignedInFilter narrows a listing of signed-in connections. Health is one
// of SignInHealths, "problems" for the ones someone has to act on, or empty
// for all.
type SignedInFilter struct {
	Health string `json:"health"`
	Cloud  string `json:"cloud"`
	Search string `json:"search"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// SignedInPage is one page of signed-in connections. Counts covers the
// cloud and search filters but not the health one, so a health filter can
// say what each choice holds.
type SignedInPage struct {
	Items  []SignedInConnection `json:"items"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
	Counts SignInCounts         `json:"counts"`
}

// SignInHealthStore reads signed-in connections across every organization.
// current maps each cloud whose grants depend on its app to that app's
// client ID, empty when it has none; a connection signed in with another
// client is AppChanged.
type SignInHealthStore interface {
	SignInHealthCounts(ctx context.Context, current map[string]string) (map[string]SignInCounts, error)
	SignedInConnections(ctx context.Context, current map[string]string, f SignedInFilter) (SignedInPage, error)
}

// ErrNoHealth is a store that can't read signed-in connections' health.
var ErrNoHealth = errors.New("this server can't read the health of signed-in connections")

// SignInHealth counts each cloud's signed-in connections by health, across
// every organization. Who may see it is the caller's to decide.
func (s *Service) SignInHealth(ctx context.Context) (map[string]SignInCounts, error) {
	h, ok := s.Apps.(SignInHealthStore)
	if !ok {
		return nil, ErrNoHealth
	}
	current, err := s.currentClients(ctx)
	if err != nil {
		return nil, err
	}
	return h.SignInHealthCounts(ctx, current)
}

// SignedIn lists signed-in connections across every organization, worst
// first. Who may see it is the caller's to decide.
func (s *Service) SignedIn(ctx context.Context, f SignedInFilter) (SignedInPage, error) {
	h, ok := s.Apps.(SignInHealthStore)
	if !ok {
		return SignedInPage{}, ErrNoHealth
	}
	f.Health, f.Cloud, f.Search = strings.TrimSpace(f.Health), strings.TrimSpace(f.Cloud), strings.TrimSpace(f.Search)
	if f.Health != "" && f.Health != "problems" && !slices.Contains(SignInHealths, f.Health) {
		return SignedInPage{}, fmt.Errorf("%w: pick a health from the list", ErrInvalid)
	}
	switch {
	case f.Limit <= 0:
		f.Limit = 25
	case f.Limit > 100:
		f.Limit = 100
	}
	f.Offset = max(f.Offset, 0)
	current, err := s.currentClients(ctx)
	if err != nil {
		return SignedInPage{}, err
	}
	return h.SignedInConnections(ctx, current, f)
}

// currentClients maps each cloud whose grants depend on its app to the
// client ID of the app it signs in with now, empty when it has none.
func (s *Service) currentClients(ctx context.Context) (map[string]string, error) {
	apps, err := s.signInApps(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(apps))
	for _, a := range apps {
		if !outlivesApp(s.Providers[a.Cloud]) {
			out[a.Cloud] = a.ClientID
		}
	}
	return out, nil
}

// What checking a sign-in app with its cloud found.
const (
	// AppCheckAccepted: the cloud took the client and refused only the
	// made-up code.
	AppCheckAccepted = "accepted"
	// AppCheckRejected: the cloud doesn't know the client ID, or the
	// secret is wrong.
	AppCheckRejected = "rejected"
	// AppCheckUnreachable: the cloud's token endpoint couldn't be reached.
	AppCheckUnreachable = "unreachable"
	// AppCheckInconclusive: the cloud answered, but not in a way that says
	// either way.
	AppCheckInconclusive = "inconclusive"
)

// AppCheckInput is a sign-in app to check. With no client ID it's the app
// the cloud signs in with now; with one it's those values before they're
// saved, with the saved secret when the secret is blank and the client ID
// is the saved one.
type AppCheckInput struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// AppCheck is what a check found. Detail is the cloud's answer, with the
// secret taken out.
type AppCheck struct {
	Result    string    `json:"result"`
	Summary   string    `json:"summary"`
	Detail    string    `json:"detail,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// checkCode is the code every check redeems. It's long and plainly fake, so
// no cloud can have issued it.
const checkCode = "loomplane-console-app-check-not-a-real-code"

// checkVerifier is a PKCE verifier of the length RFC 7636 asks for, so a
// cloud that takes PKCE doesn't refuse the request for lacking one first.
const checkVerifier = "loomplane-console-app-check-verifier-0123456789abcdef"

// checkSettings are the sign-in choices a check needs: Azure's token
// endpoint is per tenant, and "organizations" takes any work account.
var checkSettings = map[string]map[string]string{
	Azure: {"tenant_id": "organizations"},
}

// appCheckTimeout bounds one check.
const appCheckTimeout = 15 * time.Second

// CheckSignInApp asks cloud's token endpoint whether it knows a sign-in
// app, by redeeming a code that can't exist. Clouds authenticate the
// client before they look at the code (RFC 6749 section 5.2), so the
// refusal says which of the two was wrong. Nothing is created at the
// cloud and nothing is recorded here. Whether the caller may is theirs to
// check.
func (s *Service) CheckSignInApp(ctx context.Context, cloud string, in AppCheckInput) (*AppCheck, error) {
	if err := s.provider(cloud); err != nil {
		return nil, err
	}
	var app oauth.App
	clientID := strings.TrimSpace(in.ClientID)
	if clientID == "" {
		current, err := s.app(ctx, cloud)
		if errors.Is(err, ErrNoApp) {
			return nil, fmt.Errorf("%w: %s has no sign-in app yet; enter one to check it", ErrInvalid, cloudTitle(cloud))
		}
		if err != nil {
			return nil, err
		}
		app = current
	} else {
		if _, ok := s.ConfigApps[cloud]; ok {
			return nil, ErrAppLocked
		}
		secret := strings.TrimSpace(in.ClientSecret)
		if secret == "" {
			kept, err := s.keptSecret(ctx, cloud, clientID)
			if err != nil {
				return nil, err
			}
			secret = kept
		}
		if secret == "" {
			return nil, fmt.Errorf("%w: give the app's client secret to check it", ErrInvalid)
		}
		app = oauth.App{ClientID: clientID, ClientSecret: secret}
	}
	ctx, cancel := context.WithTimeout(ctx, appCheckTimeout)
	defer cancel()
	_, err := s.Providers[cloud].Exchange(ctx, app, oauth.CodeRequest{
		Redirect: s.CallbackURL, Code: checkCode, Verifier: checkVerifier, Settings: checkSettings[cloud],
	})
	out := classifyCheck(cloud, err)
	out.Detail = scrubSecret(out.Detail, app.ClientSecret)
	out.CheckedAt = s.now().UTC()
	return &out, nil
}

// classifyCheck reads what a cloud's token endpoint said to the made-up
// code.
func classifyCheck(cloud string, err error) AppCheck {
	title := cloudTitle(cloud)
	var netErr net.Error
	switch {
	case err == nil:
		return AppCheck{Result: AppCheckInconclusive,
			Summary: title + " gave a grant for a made-up code, which it shouldn't. Sign in with it to be sure."}
	case errors.Is(err, oauth.ErrAppRejected):
		return AppCheck{Result: AppCheckRejected, Detail: err.Error(),
			Summary: title + " doesn't accept this client ID and secret."}
	case errors.Is(err, oauth.ErrRefused):
		return AppCheck{Result: AppCheckAccepted, Detail: err.Error(),
			Summary: title + " accepted the client and refused only the test code, as expected. If sign-ins still fail, check the callback URL registered with the app."}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr):
		return AppCheck{Result: AppCheckUnreachable, Detail: err.Error(),
			Summary: "Couldn't reach " + title + "'s token endpoint."}
	}
	return AppCheck{Result: AppCheckInconclusive, Detail: err.Error(),
		Summary: title + " answered, but not in a way that says whether the client is right. Sign in with it to be sure."}
}

// scrubSecret keeps a secret out of anything the console shows.
func scrubSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[secret]")
}
