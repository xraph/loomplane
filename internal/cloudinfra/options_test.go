package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
)

// optionsAccount is one DigitalOcean account whose generation a test moves.
type optionsAccount struct{ generation int64 }

func (a *optionsAccount) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	if id != "do" {
		return nil, nil, connections.ErrNotFound
	}
	return &connections.Connection{ID: id, OrgID: org, Cloud: connections.DigitalOcean, Generation: a.generation, Details: map[string]string{"account_uuid": "acct"}}, []byte("token"), nil
}

// An account's sizes are read once per ten minutes and connection
// generation, without the Cloud authorization the reviewed route needs.
func TestAccountServerOptionsCachedPerGeneration(t *testing.T) {
	adapters := NewAdapters()
	catalog := &countingCatalog{}
	if err := adapters.RegisterServer(connections.DigitalOcean, catalog); err != nil {
		t.Fatal(err)
	}
	account := &optionsAccount{generation: 1}
	s := NewService(Deps{Connections: account, Adapters: adapters})
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for range 3 {
		options, err := s.AccountServerOptions(t.Context(), "org", "do")
		if err != nil || len(options.Sizes) != 2 {
			t.Fatalf("options: %+v %v", options, err)
		}
	}
	if catalog.calls != 1 {
		t.Fatalf("reads within ten minutes: %d", catalog.calls)
	}
	// Another organization's connection of the same ID is its own entry.
	if _, err := s.AccountServerOptions(t.Context(), "other", "do"); err != nil || catalog.calls != 2 {
		t.Fatalf("other organization: %v %d", err, catalog.calls)
	}
	account.generation = 2
	if _, err := s.AccountServerOptions(t.Context(), "org", "do"); err != nil || catalog.calls != 3 {
		t.Fatalf("new generation: %v %d", err, catalog.calls)
	}
	now = now.Add(optionsTTL)
	if _, err := s.AccountServerOptions(t.Context(), "org", "do"); err != nil || catalog.calls != 4 {
		t.Fatalf("after ten minutes: %v %d", err, catalog.calls)
	}
}

// A failed read is kept for a minute, so a rate-limited account isn't
// asked on every page load, and is asked again after it.
func TestAccountServerOptionsRetryFailuresAfterAMinute(t *testing.T) {
	adapters := NewAdapters()
	catalog := &countingCatalog{fail: true}
	if err := adapters.RegisterServer(connections.DigitalOcean, catalog); err != nil {
		t.Fatal(err)
	}
	s := NewService(Deps{Connections: &optionsAccount{generation: 1}, Adapters: adapters})
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for range 2 {
		if _, err := s.AccountServerOptions(t.Context(), "org", "do"); err == nil {
			t.Fatal("failed read reported ok")
		}
	}
	if catalog.calls != 1 {
		t.Fatalf("failed reads within the minute: %d", catalog.calls)
	}
	catalog.fail = false
	now = now.Add(optionsFailureTTL)
	if _, err := s.AccountServerOptions(t.Context(), "org", "do"); err != nil || catalog.calls != 2 {
		t.Fatalf("after a minute: %v %d", err, catalog.calls)
	}
}

func TestAccountServerOptionsNeedAnOptionsAdapter(t *testing.T) {
	if _, err := NewService(Deps{Connections: &optionsAccount{generation: 1}, Adapters: NewAdapters()}).AccountServerOptions(t.Context(), "org", "do"); err == nil {
		t.Fatal("no adapter read as an empty catalog")
	}
	if _, err := NewService(Deps{Connections: &optionsAccount{}, Adapters: NewAdapters()}).AccountServerOptions(t.Context(), "org", "missing"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("missing connection: %v", err)
	}
}

// revealCounter counts Vault reveals through Credential.
type revealCounter struct {
	optionsAccount
	reveals int
}

func (a *revealCounter) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	a.reveals++
	c, raw, err := a.optionsAccount.Credential(ctx, org, id)
	if c != nil {
		c.Status = connections.StatusReady
	}
	return c, raw, err
}

// accountRows reads the account's row, as the connections store does.
type accountRows struct {
	connections.Store
	account *revealCounter
	status  string
}

func (r accountRows) GetConnection(_ context.Context, org, id string) (*connections.Connection, error) {
	if id != "do" {
		return nil, connections.ErrNotFound
	}
	return &connections.Connection{ID: id, OrgID: org, Cloud: connections.DigitalOcean, Status: r.status, Generation: r.account.generation}, nil
}

// A cached read checks the account's generation from its row and never
// reveals the credential. A new generation, or a connection that isn't
// ready, goes through Credential again.
func TestAccountServerOptionsCacheHitSkipsTheReveal(t *testing.T) {
	adapters := NewAdapters()
	catalog := &countingCatalog{}
	if err := adapters.RegisterServer(connections.DigitalOcean, catalog); err != nil {
		t.Fatal(err)
	}
	account := &revealCounter{optionsAccount: optionsAccount{generation: 1}}
	rows := &accountRows{account: account, status: connections.StatusReady}
	s := NewService(Deps{Connections: account, RuntimeConnections: rows, Adapters: adapters})
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for range 3 {
		if _, err := s.AccountServerOptions(t.Context(), "org", "do"); err != nil {
			t.Fatal(err)
		}
	}
	if account.reveals != 1 || catalog.calls != 1 {
		t.Fatalf("cached reads: %d reveals, %d reads", account.reveals, catalog.calls)
	}
	account.generation = 2
	if _, err := s.AccountServerOptions(t.Context(), "org", "do"); err != nil || account.reveals != 2 || catalog.calls != 2 {
		t.Fatalf("new generation: %v, %d reveals, %d reads", err, account.reveals, catalog.calls)
	}
	rows.status = connections.StatusIncomplete
	if _, err := s.AccountServerOptions(t.Context(), "org", "do"); err != nil || account.reveals != 3 || catalog.calls != 2 {
		t.Fatalf("not ready: %v, %d reveals, %d reads", err, account.reveals, catalog.calls)
	}
}
