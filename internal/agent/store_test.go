package agent

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestRestoredAgentStartsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.AgentIdentity{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, DestinationID: "destination", AgentID: "agent", URI: "spiffe://loomplane.test/agent", Epoch: 1}
	if _, sequenceErr := store.NextSequence(t.Context()); !errors.Is(sequenceErr, ErrClosed) {
		t.Fatalf("new agent started open: %v", sequenceErr)
	}
	identity.URI, err = workloadidentity.AgentURI("loomplane.test", identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if activateErr := store.Activate(identity); activateErr != nil {
		t.Fatal(activateErr)
	}
	if n, sequenceErr := store.NextSequence(t.Context()); sequenceErr != nil || n != 1 {
		t.Fatalf("sequence: %d %v", n, sequenceErr)
	}
	restored, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, sequenceErr := restored.NextSequence(t.Context()); !errors.Is(sequenceErr, ErrClosed) {
		t.Fatalf("restored agent reused authority: %v", sequenceErr)
	}
	if activateErr := restored.Activate(identity); activateErr == nil {
		t.Fatal("reused epoch after restore")
	}
	identity.Epoch++
	identity.URI, err = workloadidentity.AgentURI("loomplane.test", identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := restored.Activate(identity); err != nil {
		t.Fatal(err)
	}
	if n, err := restored.NextSequence(t.Context()); err != nil || n != 1 {
		t.Fatalf("fresh epoch failed: %d %v", n, err)
	}
	if _, err := store.NextSequence(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("old process crossed fresh epoch: %v", err)
	}
}

func TestFailedReenrollmentClosesActiveAgent(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		store, err := OpenStore(filepath.Join(t.TempDir(), "agent", "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if closeErr := store.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		})
		a := domain.AgentIdentity{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, DestinationID: "destination", AgentID: "agent", Epoch: 1}
		a.URI, err = workloadidentity.AgentURI("loomplane.test", a)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Activate(a); err != nil {
			t.Fatal(err)
		}
		if invalid {
			a.URI = "invalid"
		}
		if err = store.Activate(a); !errors.Is(err, ErrClosed) {
			t.Fatalf("invalid reactivation: %v", err)
		}
		if _, err = store.NextSequence(t.Context()); !errors.Is(err, ErrClosed) {
			t.Fatalf("failed enrollment retained previous authority: %v", err)
		}
	}
}
