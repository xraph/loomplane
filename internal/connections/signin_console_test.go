package connections

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

func TestSetSignInAppKeepsTheSecretForTheSameClient(t *testing.T) {
	s, store, secrets, _ := signInService(nil)
	if _, err := s.SetSignInApp(t.Context(), "usr_1", DigitalOcean, AppInput{ClientID: "abc", ClientSecret: "s1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSignInApp(t.Context(), "usr_2", DigitalOcean, AppInput{ClientID: "abc"}); err != nil {
		t.Fatalf("a blank secret for the same client should keep the saved one: %v", err)
	}
	if secrets.kept[DigitalOcean] != "s1" || store.apps[DigitalOcean].UpdatedBy != "usr_2" {
		t.Fatalf("secret %q, app %+v", secrets.kept[DigitalOcean], store.apps[DigitalOcean])
	}
	// A new client never inherits the old one's secret.
	_, err := s.SetSignInApp(t.Context(), "usr_2", DigitalOcean, AppInput{ClientID: "other"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a new client with no secret: %v", err)
	}
	if store.apps[DigitalOcean].ClientID != "abc" || secrets.kept[DigitalOcean] != "s1" {
		t.Fatalf("a refused save changed the app: %+v %q", store.apps[DigitalOcean], secrets.kept[DigitalOcean])
	}
}

// checkProvider answers every exchange with err and remembers what it was
// asked with.
type checkProvider struct {
	Provider
	err  error
	app  oauth.App
	code oauth.CodeRequest
}

func (p *checkProvider) Exchange(_ context.Context, app oauth.App, r oauth.CodeRequest) (oauth.Grant, error) {
	p.app, p.code = app, r
	return oauth.Grant{}, p.err
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "dial tcp: i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

func TestCheckSignInAppClassifiesTheCloudsAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"code refused", fmt.Errorf("%w: invalid_grant", oauth.ErrRefused), AppCheckAccepted},
		{"client refused", fmt.Errorf("%w: invalid_client", oauth.ErrAppRejected), AppCheckRejected},
		{"unreachable", fmt.Errorf("digitalocean: %w", timeoutError{}), AppCheckUnreachable},
		{"deadline", context.DeadlineExceeded, AppCheckUnreachable},
		{"other", errors.New("digitalocean answered 500 from its token endpoint: server_error"), AppCheckInconclusive},
		{"a grant for a made-up code", nil, AppCheckInconclusive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := signInService(map[string]oauth.App{DigitalOcean: {ClientID: "cfg", ClientSecret: "cfg-secret"}})
			p := &checkProvider{err: tc.err}
			s.Providers[DigitalOcean] = p
			s.CallbackURL = "https://lp.example/integrations/oauth/callback"
			got, err := s.CheckSignInApp(t.Context(), DigitalOcean, AppCheckInput{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Result != tc.want || got.Summary == "" || got.CheckedAt.IsZero() {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
			// The current app, the instance's callback and the fake code.
			if p.app.ClientID != "cfg" || p.app.ClientSecret != "cfg-secret" ||
				p.code.Redirect != s.CallbackURL || p.code.Code != checkCode || p.code.Verifier == "" {
				t.Fatalf("asked with app %+v, request %+v", p.app, p.code)
			}
		})
	}
}

func TestCheckSignInAppWithUnsavedValues(t *testing.T) {
	s, store, secrets, _ := signInService(nil)
	p := &checkProvider{err: oauth.ErrRefused}
	s.Providers[Azure] = p
	store.apps[Azure] = &StoredApp{Cloud: Azure, ClientID: "saved"}
	secrets.kept[Azure] = "saved-secret"

	// The saved client with its secret left blank uses the saved secret,
	// and Azure gets a tenant to reach a token endpoint.
	if _, err := s.CheckSignInApp(t.Context(), Azure, AppCheckInput{ClientID: " saved "}); err != nil {
		t.Fatal(err)
	}
	if p.app.ClientSecret != "saved-secret" || p.code.Settings["tenant_id"] == "" {
		t.Fatalf("asked with app %+v, settings %v", p.app, p.code.Settings)
	}
	// Another client never borrows the saved secret.
	if _, err := s.CheckSignInApp(t.Context(), Azure, AppCheckInput{ClientID: "new"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a new client with no secret: %v", err)
	}
	if _, err := s.CheckSignInApp(t.Context(), Azure, AppCheckInput{ClientID: "new", ClientSecret: "typed"}); err != nil || p.app.ClientSecret != "typed" {
		t.Fatalf("typed secret: %v, asked with %+v", err, p.app)
	}
}

func TestCheckSignInAppRefusesWhatItCantCheck(t *testing.T) {
	s, _, _, _ := signInService(map[string]oauth.App{GCP: {ClientID: "cfg", ClientSecret: "x"}})
	if _, err := s.CheckSignInApp(t.Context(), DigitalOcean, AppCheckInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no app yet: %v", err)
	}
	if _, err := s.CheckSignInApp(t.Context(), GCP, AppCheckInput{ClientID: "other", ClientSecret: "y"}); !errors.Is(err, ErrAppLocked) {
		t.Fatalf("other values for a config app: %v", err)
	}
	if _, err := s.CheckSignInApp(t.Context(), "nowhere", AppCheckInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown cloud: %v", err)
	}
}

func TestCheckSignInAppKeepsTheSecretOutOfItsAnswer(t *testing.T) {
	s, _, _, _ := signInService(nil)
	s.Providers[DigitalOcean] = &checkProvider{err: errors.New("bad request with client_secret=hunter22")}
	got, err := s.CheckSignInApp(t.Context(), DigitalOcean, AppCheckInput{ClientID: "abc", ClientSecret: "hunter22"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Detail, "hunter22") || !strings.Contains(got.Detail, "[secret]") {
		t.Fatalf("detail %q", got.Detail)
	}
}

// healthStore is a fakeAppStore that reads health, remembering the current
// clients it was given.
type healthStore struct {
	*fakeAppStore
	current map[string]string
	filter  SignedInFilter
}

func (h *healthStore) SignInHealthCounts(_ context.Context, current map[string]string) (map[string]SignInCounts, error) {
	h.current = current
	return map[string]SignInCounts{}, nil
}

func (h *healthStore) SignedInConnections(_ context.Context, current map[string]string, f SignedInFilter) (SignedInPage, error) {
	h.current, h.filter = current, f
	return SignedInPage{}, nil
}

type outliving struct{ Provider }

func (outliving) GrantOutlivesApp() bool { return true }

func TestSignInHealthComparesWithTheCurrentApps(t *testing.T) {
	s, store, _, _ := signInService(map[string]oauth.App{GCP: {ClientID: "cfg-gcp", ClientSecret: "x"}})
	s.Providers[Vercel] = outliving{}
	store.apps[DigitalOcean] = &StoredApp{Cloud: DigitalOcean, ClientID: "do-app"}
	h := &healthStore{fakeAppStore: store}
	s.Apps = h
	if _, err := s.SignInHealth(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Azure has no app, so every sign-in it has is from an app since
	// removed. Vercel's grants outlive their app, so it isn't compared.
	want := map[string]string{DigitalOcean: "do-app", GCP: "cfg-gcp", Azure: ""}
	if len(h.current) != len(want) {
		t.Fatalf("current %v, want %v", h.current, want)
	}
	for k, v := range want {
		if got, ok := h.current[k]; !ok || got != v {
			t.Fatalf("current %v, want %v", h.current, want)
		}
	}
}

func TestSignedInChecksItsFilter(t *testing.T) {
	s, store, _, _ := signInService(nil)
	h := &healthStore{fakeAppStore: store}
	s.Apps = h
	if _, err := s.SignedIn(t.Context(), SignedInFilter{Health: "broken"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown health: %v", err)
	}
	if _, err := s.SignedIn(t.Context(), SignedInFilter{Health: " problems ", Limit: 1000, Offset: -3}); err != nil {
		t.Fatal(err)
	}
	if f := h.filter; f.Health != "problems" || f.Limit != 100 || f.Offset != 0 {
		t.Fatalf("filter %+v", f)
	}
	if _, err := s.SignedIn(t.Context(), SignedInFilter{}); err != nil || h.filter.Limit != 25 {
		t.Fatalf("default limit: %v %+v", err, h.filter)
	}

	// A store that can't read health says so.
	s.Apps = store
	if _, err := s.SignedIn(t.Context(), SignedInFilter{}); !errors.Is(err, ErrNoHealth) {
		t.Fatalf("no health: %v", err)
	}
}

func TestSignInCounts(t *testing.T) {
	var c SignInCounts
	for h, n := range map[string]int{SignInHealthy: 5, SignInIncomplete: 1, SignInFailed: 2, SignInNeeded: 3, SignInAppChanged: 4} {
		c.Add(h, n)
	}
	if c.Total != 15 || c.Of("problems") != 9 || c.Of("") != 15 || c.Of(SignInAppChanged) != 4 || c.Of("nope") != 0 {
		t.Fatalf("counts %+v", c)
	}
}
