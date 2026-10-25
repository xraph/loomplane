//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
)

// countingRenewer renews rt-1 into at-2, slowly, and counts its calls.
type countingRenewer struct {
	calls   atomic.Int32
	refused bool
}

func (*countingRenewer) AuthorizeURL(oauth.App, string, string) string { return "" }

func (*countingRenewer) Exchange(context.Context, oauth.App, string, string) (oauth.Grant, error) {
	return oauth.Grant{}, oauth.ErrRefused
}

func (r *countingRenewer) Refresh(_ context.Context, _ oauth.App, refreshToken string) (oauth.Grant, error) {
	r.calls.Add(1)
	time.Sleep(50 * time.Millisecond)
	if r.refused || refreshToken != "rt-1" {
		return oauth.Grant{}, oauth.ErrRefused
	}
	return oauth.Grant{AccessToken: "at-2", RefreshToken: "rt-2", Expiry: time.Now().Add(30 * 24 * time.Hour)}, nil
}

type refreshFixture struct {
	store   *connections.PgStore
	secrets *infraSecrets
	renewer *countingRenewer
	svc     *connections.Service
}

// newRefreshFixture stores a ready DigitalOcean sign-in, conn_1, whose
// grant expires within the hour.
func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	dsn := freshDatabase(t)
	ctx := t.Context()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err := pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	f := &refreshFixture{store: connections.NewPgStore(pg), secrets: &infraSecrets{values: map[string][]byte{}}, renewer: &countingRenewer{}}
	f.svc = connections.NewService(f.store, f.secrets, nil, nil, nil, func(msg string, err error) { t.Logf("warn: %s: %v", msg, err) })
	f.svc.Providers = map[string]connections.Provider{connections.DigitalOcean: f.renewer}
	f.svc.ConfigApps = map[string]oauth.App{connections.DigitalOcean: {ClientID: "id", ClientSecret: "secret"}}
	now := time.Now().UTC()
	c := &connections.Connection{ID: "conn_1", OrgID: "org_1", Cloud: connections.DigitalOcean, Name: "Main", Method: connections.MethodOAuth,
		Account: "Acme", Details: map[string]string{"team_uuid": "team-1"}, Status: connections.StatusReady, CreatedAt: now, UpdatedAt: now}
	if err := f.store.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(oauth.Grant{AccessToken: "at-1", RefreshToken: "rt-1", Expiry: now.Add(time.Hour)})
	if err := f.secrets.Put(ctx, "org_1", "conn_1", raw); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCredentialRefreshesOnceAcrossConcurrentCallers(t *testing.T) {
	f := newRefreshFixture(t)
	var wg sync.WaitGroup
	creds := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Go(func() {
			_, cred, err := f.svc.Credential(t.Context(), "org_1", "conn_1")
			creds[i], errs[i] = string(cred), err
		})
	}
	wg.Wait()
	for i := range 8 {
		if errs[i] != nil || creds[i] != "at-2" {
			t.Fatalf("caller %d: %q %v", i, creds[i], errs[i])
		}
	}
	if n := f.renewer.calls.Load(); n != 1 {
		t.Fatalf("the single-use refresh token was spent %d times", n)
	}
	var kept oauth.Grant
	raw, _ := f.secrets.Reveal(t.Context(), "org_1", "conn_1")
	if err := json.Unmarshal(raw, &kept); err != nil || kept.RefreshToken != "rt-2" {
		t.Fatalf("vault holds a grant with refresh token %q: %v", kept.RefreshToken, err)
	}
	if c, err := f.store.GetConnection(t.Context(), "org_1", "conn_1"); err != nil || c.Generation != 1 {
		t.Fatalf("renewing changed the generation: %+v %v", c, err)
	}
}

func TestRefusedRefreshKeepsTheGeneration(t *testing.T) {
	f := newRefreshFixture(t)
	f.renewer.refused = true
	if _, _, err := f.svc.Credential(t.Context(), "org_1", "conn_1"); !errors.Is(err, connections.ErrNotReady) {
		t.Fatalf("got %v", err)
	}
	c, err := f.store.GetConnection(t.Context(), "org_1", "conn_1")
	if err != nil || c.Status != connections.StatusSignInNeeded || c.Generation != 1 || c.Message == "" {
		t.Fatalf("connection %+v %v", c, err)
	}
	if list, err := f.store.OAuthConnections(t.Context()); err != nil || len(list) != 0 {
		t.Fatalf("a lapsed sign-in is still listed for renewal: %v %v", list, err)
	}
}

func TestOAuthConnectionsListsSignInsAcrossOrganizations(t *testing.T) {
	f := newRefreshFixture(t)
	now := time.Now().UTC()
	for _, c := range []*connections.Connection{
		{ID: "conn_2", OrgID: "org_2", Cloud: connections.DigitalOcean, Name: "Other", Method: connections.MethodOAuth, Status: connections.StatusIncomplete, CreatedAt: now, UpdatedAt: now},
		{ID: "conn_3", OrgID: "org_1", Cloud: connections.DigitalOcean, Name: "Token", Method: connections.MethodToken, Status: connections.StatusReady, CreatedAt: now, UpdatedAt: now},
	} {
		if err := f.store.CreateConnection(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	list, err := f.store.OAuthConnections(t.Context())
	if err != nil || len(list) != 2 || list[0].ID != "conn_1" || list[1].ID != "conn_2" {
		t.Fatalf("listed %v %v", list, err)
	}
}

func TestRenewalWaitsForAReplacement(t *testing.T) {
	f := newRefreshFixture(t)
	c, err := f.store.GetConnection(t.Context(), "org_1", "conn_1")
	if err != nil {
		t.Fatal(err)
	}
	holding, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(letGo) // a failed check mustn't leave the replacement holding the lock
	replaced := make(chan error, 1)
	go func() {
		replaced <- f.store.WithCredentialReplacement(t.Context(), c, func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	var entered atomic.Bool
	locked := make(chan error, 1)
	go func() {
		locked <- f.store.WithConnectionLock(t.Context(), "org_1", "conn_1", func() error { entered.Store(true); return nil })
	}()
	time.Sleep(200 * time.Millisecond)
	if entered.Load() {
		t.Fatal("the connection lock was taken during a replacement")
	}
	letGo()
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	if err := <-locked; err != nil || !entered.Load() {
		t.Fatalf("lock after replacement: %v", err)
	}
}
