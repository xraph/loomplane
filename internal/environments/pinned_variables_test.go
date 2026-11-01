package environments

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

func TestPlacementGroupVariableVersionsDoNotReadLatest(t *testing.T) {
	f := newFixture(t)
	first, err := f.secrets.PutPinned(t.Context(), org, "versioned", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.secrets.PutPinned(t.Context(), org, "versioned", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity == "" || second.Identity == "" || second.Version <= first.Version {
		t.Fatal("missing persisted source version identity")
	}
	value, err := f.secrets.RevealPinned(t.Context(), org, "versioned", first)
	// The Vault memory store changes row identity on update. PostgreSQL retains
	// it; the integration test covers archived reads through that installed store.
	if first.Identity != second.Identity && !errors.Is(err, ErrNotFound) || first.Identity == second.Identity && (err != nil || string(value) != "first") {
		t.Fatalf("exact version changed: %q %v", value, err)
	}
	if err = f.secrets.Delete(t.Context(), org, "versioned"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.secrets.RevealPinned(t.Context(), org, "versioned", first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted exact version did not fail closed: %v", err)
	}
	if _, err = f.secrets.PutPinned(t.Context(), org, "versioned", []byte("recreated")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.secrets.RevealPinned(t.Context(), org, "versioned", first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recreated key reused old version authority: %v", err)
	}
}

type selectionStore struct{ *memStore }

func (s selectionStore) WithSourceTransaction(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s selectionStore) PublishSecretVersion(_ context.Context, v *Variable, pin SecretVersion) error {
	v.secretIdentity, v.secretVersion = pin.Identity, pin.Version
	return nil
}

func TestRuntimeSelectionRejectsMissingSecretPin(t *testing.T) {
	f := newFixture(t)
	actor := f.as(t, "reviewer", security.RoleAdmin)
	v, err := f.svc.CreateProject(t.Context(), actor, project, CreateVariableInput{Key: "TOKEN", Secret: true, Value: "must-not-reveal"})
	if err != nil {
		t.Fatal(err)
	}
	v.sourceRevision = 1
	f.store.vars[v.ID] = v
	f.svc.store = selectionStore{f.store}
	env, err := f.svc.environment(t.Context(), org, project, "production")
	if err != nil {
		t.Fatal(err)
	}
	selection := RuntimeSelection{SchemaVersion: 1, OrgID: org, ProjectID: project, EnvironmentID: env.ID, Environment: env.Name, Sources: []RuntimeSourcePin{sourcePin(v)}}
	selection.Revision, err = selectionRevision(selection)
	if err != nil {
		t.Fatal(err)
	}
	values, _, err := f.svc.CaptureRuntimeSelection(t.Context(), actor, selection)
	if !errors.Is(err, ErrSourceSelection) || values != nil {
		t.Fatalf("unpinned capture returned values=%v error=%v", values, err)
	}
}
