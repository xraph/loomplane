//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type sourceVaultPublicationFault struct {
	*vaultpg.Store
	failAfterSet atomic.Bool
	failMetadata atomic.Bool
}

func (s *sourceVaultPublicationFault) SetSecret(ctx context.Context, value *secret.Secret) error {
	if err := s.Store.SetSecret(ctx, value); err != nil {
		return err
	}
	if s.failAfterSet.Swap(false) {
		s.failMetadata.Store(true)
	}
	return nil
}
func (s *sourceVaultPublicationFault) GetSecret(ctx context.Context, key, app, tenant string) (*secret.Secret, error) {
	if s.failMetadata.Swap(false) {
		return nil, errors.New("injected metadata response failure")
	}
	return s.Store.GetSecret(ctx, key, app, tenant)
}

func TestPlacementGroupRecoveryVariablePublication(t *testing.T) {
	f := newPoolFixture(t)
	pools, err := environments.OpenSourcePools(t.Context(), f.sql.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := pools.Close(); e != nil {
			t.Error(e)
		}
	})
	storage := &sourceVaultPublicationFault{Store: vaultpg.New(pools.Vault)}
	if err = storage.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{51}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secrets := environments.NewVaultSecrets(secret.NewService(storage, enc))
	svc := environments.NewService(environments.NewPgStoreWithSourcePool(f.db, pools.SQL), secrets, projects.NewPgStore(f.db), nil, &groupAuth{}, nil, nil)
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	// The installed wiring reserves independent bounded source and Vault pools.
	// The context bounds every publication and fault recovery.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	v, err := svc.CreateProject(ctx, actor, "proj", environments.CreateVariableInput{Key: "TOKEN", Secret: true, Value: "first", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := secrets.Metadata(ctx, "org", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := svc.SelectRuntime(ctx, actor, "proj", "production", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, actor, v.ID, environments.UpdateVariableInput{Value: "second"}); err != nil {
		t.Fatal(err)
	}
	second, err := secrets.Metadata(ctx, "org", v.ID)
	if err != nil || first.Identity != second.Identity || second.Version <= first.Version {
		t.Fatalf("persisted incarnation: %+v %+v %v", first, second, err)
	}
	old, err := secrets.RevealPinned(ctx, "org", v.ID, first)
	if err != nil || string(old) != "first" {
		t.Fatalf("archived exact version: %q %v", old, err)
	}
	rotated, err := svc.SelectRuntime(ctx, actor, "proj", "production", "")
	if err != nil || rotated.Revision == selection.Revision {
		t.Fatalf("rotation did not invalidate effective config selection: %v", err)
	}
	pinned, _, err := svc.CaptureRuntimeSelection(ctx, actor, selection)
	if err != nil || pinned["TOKEN"] != "first" {
		t.Fatalf("approved selection changed after source rotation: %v %v", pinned, err)
	}
	_, err = f.sql.Exec(ctx, `CREATE FUNCTION reject_variable_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.updated_by='fail-publication' THEN RAISE EXCEPTION 'injected publication failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_variable_publication BEFORE UPDATE ON loomplane_variables FOR EACH ROW EXECUTE FUNCTION reject_variable_publication()`)
	if err != nil {
		t.Fatal(err)
	}
	failedActor := actor
	failedActor.UserID = "fail-publication"
	if _, err = svc.Update(ctx, failedActor, v.ID, environments.UpdateVariableInput{Value: "unpublished"}); err == nil {
		t.Fatal("injected SQL failure did not run")
	}
	values, flags, err := svc.Values(ctx, "org", "proj", "production", "")
	if err != nil || values["TOKEN"] != "second" || !flags["TOKEN"] {
		t.Fatalf("failed SQL publication exposed latest Vault value: %v %v", values, err)
	}
	var publishedVersion int64
	var publishedID string
	if err = f.sql.QueryRow(ctx, `SELECT secret_identity,secret_version FROM loomplane_variables WHERE id=$1`, v.ID).Scan(&publishedID, &publishedVersion); err != nil || publishedVersion != second.Version || publishedID != second.Identity {
		t.Fatalf("published pin changed: %s %d %v", publishedID, publishedVersion, err)
	}
	storage.failAfterSet.Store(true)
	if _, err = svc.Update(ctx, actor, v.ID, environments.UpdateVariableInput{Value: "metadata-unpublished"}); err == nil {
		t.Fatal("injected metadata failure did not run")
	}
	values, _, err = svc.Values(ctx, "org", "proj", "production", "")
	if err != nil || values["TOKEN"] != "second" {
		t.Fatalf("metadata failure replaced published value: %v %v", values, err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, value := range []string{"concurrent-one", "concurrent-two"} {
		wg.Go(func() {
			_, updateErr := svc.Update(ctx, actor, v.ID, environments.UpdateVariableInput{Value: value})
			failures <- updateErr
		})
	}
	wg.Wait()
	close(failures)
	for updateErr := range failures {
		if updateErr != nil {
			t.Fatal(updateErr)
		}
	}
	values, _, err = svc.Values(ctx, "org", "proj", "production", "")
	if err != nil || values["TOKEN"] != "concurrent-one" && values["TOKEN"] != "concurrent-two" {
		t.Fatalf("concurrent publication: %v %v", values, err)
	}
	if err = svc.Delete(ctx, actor, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = svc.CaptureRuntimeSelection(ctx, actor, selection); !errors.Is(err, environments.ErrSourceSelection) {
		t.Fatalf("source deleted before independent capture: %v", err)
	}
	if _, err = secrets.RevealPinned(ctx, "org", v.ID, first); !errors.Is(err, environments.ErrNotFound) {
		t.Fatalf("deleted source pin readable: %v", err)
	}
	store := environments.NewPgStoreWithSourcePool(f.db, pools.SQL)
	var recreated environments.SecretVersion
	if err = store.WithSourceTransaction(ctx, "org", func(ctx context.Context) error {
		var e error
		recreated, e = secrets.PutPinned(ctx, "org", v.ID, []byte("recreated"))
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if recreated.Identity == first.Identity || recreated.Version != 1 {
		t.Fatalf("recreated incarnation: %+v", recreated)
	}
	if _, err = secrets.RevealPinned(ctx, "org", v.ID, first); !errors.Is(err, environments.ErrNotFound) {
		t.Fatalf("recreated key accepted old pin: %v", err)
	}
	// Legacy enrollment commits before attempting a new Vault write.
	legacy := &environments.Variable{ID: "legacy-source", OrgID: "org", ProjectID: "proj", Scope: environments.ScopeProject, Key: "LEGACY", Secret: true, CreatedBy: "usr", UpdatedBy: "usr", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = store.WithSourceTransaction(ctx, "org", func(ctx context.Context) error {
		if e := store.CreateVariable(ctx, legacy); e != nil {
			return e
		}
		return secrets.Put(ctx, "org", legacy.ID, []byte("legacy-published"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, failedActor, legacy.ID, environments.UpdateVariableInput{Value: "legacy-unpublished"}); err == nil {
		t.Fatal("legacy publication failure missing")
	}
	values, _, err = svc.Values(ctx, "org", "proj", "production", "")
	if err != nil || values["LEGACY"] != "legacy-published" {
		t.Fatalf("legacy enrollment rolled back with failed mutation: %v %v", values, err)
	}
}
