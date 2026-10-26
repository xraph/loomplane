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
