//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
)

type pullOnlyRegistryChecker struct{}

func (pullOnlyRegistryChecker) Parse([]byte, map[string]string) (connections.Check, error) {
	return connections.Check{}, nil
}
func (pullOnlyRegistryChecker) Check(context.Context, []byte, map[string]string) (connections.Check, error) {
	return connections.Check{Account: "Registry", Details: map[string]string{"endpoint": "registry.example", "repository": "app", "api": "ready", "pull": "ready"}, Missing: []string{"registry:push"}}, nil
}

type pausedReplacementSecrets struct {
	*infraSecrets
	entered chan struct{}
	release chan struct{}
}

func (s *pausedReplacementSecrets) Put(context.Context, string, string, []byte) error {
	close(s.entered)
	<-s.release
	return errors.New("fixture Vault write failed")
}

func TestCloudInfrastructureRegistryReplacementFence(t *testing.T) {
	f := newInfraFixture(t)
	service := connections.NewService(f.connections, f.secrets, map[string]connections.Checker{"registry": pullOnlyRegistryChecker{}}, &syncAuth{}, nil, nil)
	c, err := service.Create(t.Context(), f.p, connections.CreateInput{Cloud: "registry", Name: "Pull-only", Credential: "original-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if !connections.RegistryPullReady(c) {
		t.Fatal("fixture is not pull-ready")
	}
	if _, raw, readErr := service.Credential(t.Context(), "org", c.ID); readErr != nil || string(raw) != "original-fixture" {
		t.Fatalf("initial pull credential: %v", readErr)
	}
	paused := &pausedReplacementSecrets{infraSecrets: f.secrets, entered: make(chan struct{}), release: make(chan struct{})}
	replacement := connections.NewService(f.connections, paused, map[string]connections.Checker{"registry": pullOnlyRegistryChecker{}}, &syncAuth{}, nil, nil)
	done := make(chan error, 1)
	go func() {
		_, replaceErr := replacement.Replace(t.Context(), f.p, c.ID, connections.ReplaceInput{Credential: "replacement-fixture"})
		done <- replaceErr
	}()
	<-paused.entered
	_, raw, readErr := service.Credential(t.Context(), "org", c.ID)
	// Always release the writer before reporting a failed assertion.
	close(paused.release)
	replaceErr := <-done
	if replaceErr == nil {
		t.Fatal("fixture replacement did not fail")
	}
	if !errors.Is(readErr, connections.ErrNotReady) || len(raw) != 0 {
		t.Fatalf("paused replacement exposed pull credential: %v", readErr)
	}
	stored, err := f.connections.GetConnection(t.Context(), "org", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if connections.RegistryPullReady(stored) {
		t.Fatal("failed replacement remained eligible for pulls")
	}
	if _, raw, err = service.Credential(t.Context(), "org", c.ID); !errors.Is(err, connections.ErrNotReady) || len(raw) != 0 {
		t.Fatalf("failed replacement exposed pull credential: %v", err)
	}
	if _, err = service.Replace(t.Context(), f.p, c.ID, connections.ReplaceInput{Credential: "recovered-fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, raw, err = service.Credential(t.Context(), "org", c.ID); err != nil || string(raw) != "recovered-fixture" {
		t.Fatalf("successful replacement did not restore pulls: %v", err)
	}
}

func legacyAcceptedAdoption(t *testing.T, f *infraFixture) *cloudinfra.Operation {
	t.Helper()
	preview := adoptionPreview(t, f)
	legacy := *preview
	legacy.ID = "legacy-adoption"
	legacy.ServerID = "legacy-server"
	legacy.ConnectionGeneration = 0
	if err := f.store.CreatePreview(t.Context(), &legacy); err != nil {
		t.Fatal(err)
	}
	// Before transactional adoption claims, acceptance saved only this operation.
	legacy.Status = "pending"
	legacy.Attempt = 1
	raw, err := json.Marshal(&legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_infrastructure_operations SET status='pending',body=$1 WHERE id=$2`, raw, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.GetServer(t.Context(), "org", "cloud", legacy.ServerID); !errors.Is(err, cloudinfra.ErrNotFound) {
		t.Fatal("fixture unexpectedly has a claim")
	}
	return &legacy
}

func TestCloudInfrastructureLegacyAdoptionRecovery(t *testing.T) {
	for _, reservation := range []string{"none", "existing", "concurrent", "placeholder"} {
		t.Run(reservation, func(t *testing.T) {
			f := newInfraFixture(t)
			o := legacyAcceptedAdoption(t, f)
			if reservation == "placeholder" {
				old := &cloudinfra.Server{CreationOperationID: o.ID, ID: o.ServerID, OrgID: o.OrgID, CloudID: o.CloudID, Name: o.Input.Name, Ownership: o.Ownership, Provider: o.Provider, ConnectionID: o.ConnectionID, AccountID: o.AccountID, ResourceID: o.ResourceID, Revision: 1, Status: "provisioning", CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
				if err := f.store.SaveServer(t.Context(), old); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.ClaimAdoption(t.Context(), "other", "cloud", o.ID); !errors.Is(err, cloudinfra.ErrNotFound) {
				t.Fatalf("legacy claim crossed organization scope: %v", err)
			}
			if reservation == "existing" {
				if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'plan','legacy-conflict')`, o.ConnectionID); err != nil {
					t.Fatal(err)
				}
			}
			if reservation == "concurrent" {
				tx, err := f.sql.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(t.Context())
				if _, err = tx.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'plan','legacy-conflict')`, o.ConnectionID); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
				if _, err = f.store.ClaimAdoption(ctx, "org", "cloud", o.ID); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("legacy claim ignored concurrent reservation: %v", err)
				}
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			restarted := *f.svc
			f.svc = &restarted
			if err := f.svc.RecoverInfrastructure(t.Context()); err != nil {
				t.Fatal(err)
			}
			if f.workflow.calls != 1 {
				t.Fatal("legacy accepted operation was not recovered")
			}
			before := f.runtime.calls
			o = f.reconcile(t, o)
			if reservation == "existing" || reservation == "concurrent" {
				if o.Status != "failed" || f.runtime.calls != before {
					t.Fatalf("reserved host reached verification: %+v", o)
				}
				if _, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID); !errors.Is(err, cloudinfra.ErrNotFound) {
					t.Fatal("failed claim left a partial server")
				}
				if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_connection_uses WHERE owner_id='legacy-conflict'`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.svc.RetryOperation(t.Context(), f.p, "cloud", o.ID); err != nil {
					t.Fatal(err)
				}
				o = f.reconcile(t, o)
			}
			if o.Status != "succeeded" {
				t.Fatalf("legacy adoption could not resume: %+v", o)
			}
			server, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			if server.RuntimeConnectionID != o.ConnectionID || server.CreationOperationID != o.ID || server.Status != "ready" {
				t.Fatal("recovered claim lost immutable identity")
			}
		})
	}
}

// A legacy adoption claims its host when it runs. While a direct deployment
// runs on that daemon the claim is refused, and the operation waits for the
// deployment instead of failing as a verification problem.
func TestCloudInfrastructureLegacyAdoptionWaitsForDirectApply(t *testing.T) {
	f := newInfraFixture(t)
	o := legacyAcceptedAdoption(t, f)
	running, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = running.Rollback(context.Background()) }()
	// What a direct apply on the adopted daemon holds through its effects.
	if _, err = running.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('direct-apply/docker-real',0))`); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.ReconcileInfrastructure(t.Context(), "org", o.ID, nil); !errors.Is(err, cloudinfra.ErrPending) {
		t.Fatalf("claim beside a direct apply: %v", err)
	}
	waiting, err := f.store.OperationByID(t.Context(), "org", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "waiting" || waiting.Message != "Waiting for a direct deployment on this server to finish." {
		t.Fatalf("operation beside a direct apply: %s %q", waiting.Status, waiting.Message)
	}
	if _, err = f.store.GetServer(t.Context(), "org", "cloud", o.ServerID); !errors.Is(err, cloudinfra.ErrNotFound) {
		t.Fatalf("refused claim left a server: %v", err)
	}
	if err = running.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if o = f.reconcile(t, o); o.Status != "succeeded" {
		t.Fatalf("adoption after the direct apply: %+v", o)
	}
}
