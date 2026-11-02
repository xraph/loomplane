//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type sourcePause struct {
	*environments.VaultSecrets
	mu      sync.Mutex
	entered chan struct{}
	resume  chan struct{}
}

func (s *sourcePause) PutPinned(ctx context.Context, org, id string, value []byte) (environments.SecretVersion, error) {
	s.mu.Lock()
	entered, resume := s.entered, s.resume
	s.entered = nil
	s.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
			return environments.SecretVersion{}, ctx.Err()
		}
	}
	return s.VaultSecrets.PutPinned(ctx, org, id, value)
}
func sourceFixture(t *testing.T) (*poolFixture, *environments.Service, *sourcePause, *pgdriver.PgDB) {
	t.Helper()
	f := newPoolFixture(t)
	dsn := f.sql.Config().ConnString()
	main := pgdriver.New()
	if err := main.Open(t.Context(), dsn, driver.WithPoolSize(1)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = main.Close() })
	pools, err := environments.OpenSourcePools(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := pools.Close(); e != nil {
			t.Error(e)
		}
		if e := pools.Close(); e != nil {
			t.Error(e)
		}
	})
	storage := vaultpg.New(pools.Vault)
	if err = storage.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secrets := &sourcePause{VaultSecrets: environments.NewVaultSecrets(secret.NewService(storage, enc))}
	store := environments.NewPgStoreWithSourcePool(main, pools.SQL)
	svc := environments.NewService(store, secrets, projects.NewPgStore(main), nil, &groupAuth{}, nil, func(msg string, e error) { t.Errorf("%s: %v", msg, e) })
	return f, svc, secrets, main
}

func waitSourceGate(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '/* source-direct */%')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestVariableSourceSinglePoolAndDirectMutationOrdering(t *testing.T) {
	for _, mode := range []string{"update", "delete", "environment", "project"} {
		t.Run(mode, func(t *testing.T) {
			f, svc, secrets, main := sourceFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			actor := security.Principal{OrgID: "org", UserID: "usr"}
			// A separate project has no unrelated resource RESTRICT references.
			for _, q := range []string{`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('sources','org','Sources','sources','ready','usr',now(),now())`, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('sources-env','org','sources','production','production',now(),now())`} {
				if _, err := f.sql.Exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			v, err := svc.CreateProject(ctx, actor, "sources", environments.CreateVariableInput{Key: "TOKEN", Value: "first", Secret: true, Environment: "production"})
			if err != nil {
				t.Fatal(err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			secrets.mu.Lock()
			secrets.entered, secrets.resume = entered, resume
			secrets.mu.Unlock()
			update := make(chan error, 1)
			go func() {
				_, e := svc.Update(ctx, actor, v.ID, environments.UpdateVariableInput{Value: "second"})
				update <- e
			}()
			select {
			case <-entered:
			case e := <-update:
				t.Fatalf("source failed before effect: %v", e)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			direct := make(chan error, 1)
			statements := map[string]string{"update": `/* source-direct */ UPDATE loomplane_variables SET updated_by='direct' WHERE id=$1`, "delete": `/* source-direct */ DELETE FROM loomplane_variables WHERE id=$1`, "environment": `/* source-direct */ DELETE FROM loomplane_environments WHERE id='sources-env' AND $1<>''`, "project": `/* source-direct */ DELETE FROM loomplane_projects WHERE id='sources' AND $1<>''`}
			go func() { _, e := main.Exec(ctx, statements[mode], v.ID); direct <- e }()
			waitSourceGate(ctx, t, f.sql)
			close(resume)
			if err = <-update; err != nil {
				t.Fatal(err)
			}
			if err = <-direct; err != nil {
				t.Fatal(err)
			}
			// The only main-pool connection was held by a direct gate waiter while
			// publication completed through the two reserved connections.
		})
	}
}

func TestVariableSourceSelectionAndConcurrentCleanup(t *testing.T) {
	_, svc, _, _ := sourceFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	selectNow := func() environments.RuntimeSelection {
		s, e := svc.SelectRuntime(ctx, actor, "proj", "production", "")
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	empty := selectNow()
	if len(empty.Sources) != 0 {
		t.Fatal("expected empty selection")
	}
	orgVar, err := svc.CreateOrganization(ctx, actor, environments.CreateVariableInput{Key: "SETTING", Value: "org"})
	if err != nil {
		t.Fatal(err)
	}
	orgSelection := selectNow()
	if orgSelection.Revision == empty.Revision {
		t.Fatal("empty selection did not invalidate")
	}
	if _, err = svc.Update(ctx, actor, orgVar.ID, environments.UpdateVariableInput{Value: "changed"}); err != nil {
		t.Fatal(err)
	}
	changed := selectNow()
	if changed.Revision == orgSelection.Revision {
		t.Fatal("plain change did not invalidate")
	}
	if _, _, err = svc.CaptureRuntimeSelection(ctx, actor, orgSelection); !errors.Is(err, environments.ErrSourceSelection) {
		t.Fatalf("stale plain selection accepted: %v", err)
	}
	projectVar, err := svc.CreateProject(ctx, actor, "proj", environments.CreateVariableInput{Key: "SETTING", Value: "project", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	selected := selectNow()
	if selected.Revision == changed.Revision || selected.Sources[0].VariableID != projectVar.ID {
		t.Fatal("precedence did not invalidate")
	}
	foreign := selected
	foreign.Sources = append([]environments.RuntimeSourcePin(nil), selected.Sources...)
	foreign.Sources[0].ProjectID = "second"
	foreign.Revision = ""
	raw, e := json.Marshal(foreign)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw)
	foreign.Revision = hex.EncodeToString(sum[:])
	if _, _, err = svc.CaptureRuntimeSelection(ctx, actor, foreign); !errors.Is(err, environments.ErrSourceSelection) {
		t.Fatalf("foreign scope accepted: %v", err)
	}
	asSecret := true
	asPlain := false
	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		failures := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				_, e := svc.Update(ctx, actor, projectVar.ID, environments.UpdateVariableInput{Value: "secret", Secret: &asSecret})
				failures <- e
			})
		}
		wg.Go(func() { svc.Sweep(ctx, actor.OrgID) })
		wg.Wait()
		close(failures)
		for e := range failures {
			if e != nil {
				t.Fatal(e)
			}
		}
		wg.Go(func() {
			_, e := svc.Update(ctx, actor, projectVar.ID, environments.UpdateVariableInput{Value: "plain", Secret: &asPlain})
			if e != nil {
				t.Error(e)
			}
		})
		wg.Go(func() { svc.Sweep(ctx, actor.OrgID) })
		wg.Go(func() {
			_, e := svc.Update(ctx, actor, projectVar.ID, environments.UpdateVariableInput{Value: "secret-again", Secret: &asSecret})
			if e != nil {
				t.Error(e)
			}
		})
		wg.Wait()
		values, flags, e := svc.Values(ctx, actor.OrgID, "proj", "production", "")
		if e != nil {
			t.Fatal(e)
		}
		if flags["SETTING"] && values["SETTING"] != "secret-again" || !flags["SETTING"] && values["SETTING"] != "plain" {
			t.Fatal("cleanup damaged a current incarnation")
		}
	}
}
