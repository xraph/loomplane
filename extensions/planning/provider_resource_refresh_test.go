package planning

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

type refreshResourceFixture struct{ calls chan string }

func (f refreshResourceFixture) SyncIfIdle(_ context.Context, p security.Principal, id string) (*providerresources.ProviderResourceSync, error) {
	f.calls <- p.UserID + ":" + id
	return &providerresources.ProviderResourceSync{}, nil
}
func (refreshResourceFixture) RefreshAll(context.Context) error { return nil }

type refreshConnectionsFixture struct{ connection connections.Connection }

func (f refreshConnectionsFixture) All(context.Context, string) ([]*connections.Connection, error) {
	c := f.connection
	return []*connections.Connection{&c}, nil
}

func TestProviderResourceRefreshUsesCheckedPrincipalAndStops(t *testing.T) {
	c := connections.Connection{ID: "conn", OrgID: "org", Cloud: "digitalocean", Status: connections.StatusReady, Generation: 1, Details: map[string]string{"account_uuid": "account"}}
	f := refreshResourceFixture{calls: make(chan string, 1)}
	r := newProviderResourceRefresh(f, refreshConnectionsFixture{c}, func(string) bool { return true }, func(err error) { t.Errorf("refresh: %v", err) })
	r.checked(t.Context(), security.Principal{OrgID: "org", UserID: "member"}, c)
	r.start(t.Context())
	select {
	case call := <-f.calls:
		if call != "member:conn" {
			t.Fatal(call)
		}
	case <-time.After(time.Second):
		t.Fatal("checked account was not queued")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := r.stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProviderResourceRefreshRejectsChangedQueuedIdentity(t *testing.T) {
	c := connections.Connection{ID: "conn", OrgID: "org", Cloud: "digitalocean", Status: connections.StatusReady, Generation: 1, Details: map[string]string{"account_uuid": "account"}}
	f := refreshResourceFixture{calls: make(chan string, 1)}
	r := newProviderResourceRefresh(f, refreshConnectionsFixture{c}, func(string) bool { return true }, func(error) {})
	changed := c
	changed.Generation = 2
	if err := r.runChecked(t.Context(), checkedResourceConnection{principal: security.Principal{OrgID: "org", UserID: "member"}, connection: changed}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.calls:
		t.Fatal("stale checked generation reached provider")
	default:
	}
}
