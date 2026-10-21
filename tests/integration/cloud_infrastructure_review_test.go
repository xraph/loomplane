//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
)

type infrastructureChecker struct{}

func (infrastructureChecker) Parse([]byte, map[string]string) (connections.Check, error) {
	return connections.Check{}, nil
}
func (infrastructureChecker) Check(context.Context, []byte, map[string]string) (connections.Check, error) {
	return connections.Check{Account: "Account renamed", Details: map[string]string{"account_uuid": "account", "api": "ready"}}, nil
}

func TestCloudInfrastructureConnectionGeneration(t *testing.T) {
	for _, change := range []string{"health", "credential", "configuration"} {
		t.Run(change, func(t *testing.T) {
			f := newInfraFixture(t)
			f.provider.lost = true
			o := f.reconcile(t, f.create(t))
			if !o.Attempted || o.ResourceID != "" {
				t.Fatal("fixture did not lose the create receipt")
			}
			before, err := f.connections.GetConnection(t.Context(), "org", "infra-account")
			if err != nil {
				t.Fatal(err)
			}
			service := connections.NewService(f.connections, f.secrets, map[string]connections.Checker{"digitalocean": infrastructureChecker{}}, &syncAuth{}, nil, nil)
			switch change {
			case "health":
				checked, checkErr := service.Recheck(t.Context(), f.p, before.ID)
				if checkErr != nil {
					t.Fatal(checkErr)
				}
				if checked.Generation != before.Generation || !checked.UpdatedAt.After(before.UpdatedAt) {
					t.Fatal("health check did not preserve generation and advance health timestamp")
				}
			case "credential":
				replaced, replaceErr := service.Replace(t.Context(), f.p, before.ID, connections.ReplaceInput{Credential: "dop_v1_replacement"})
				if replaceErr != nil {
					t.Fatal(replaceErr)
				}
				if replaced.Generation <= before.Generation {
					t.Fatal("replacement preserved approval generation")
				}
			case "configuration":
				before.Details["team_uuid"] = "different-team"
				if err = f.connections.UpdateConnection(t.Context(), before); err != nil {
					t.Fatal(err)
				}
			}
			_, err = f.svc.RetryOperation(t.Context(), f.p, "cloud", o.ID)
			if change != "health" {
				if !errors.Is(err, cloudinfra.ErrConflict) {
					t.Fatalf("changed connection retry: %v", err)
				}
				o = f.reconcile(t, o)
				if o.Status != "failed" || f.provider.creates != 1 || f.provider.seenReconcile {
					t.Fatal("changed connection reached provider")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			o = f.reconcile(t, o)
			if o.ResourceID != "42" || !f.provider.seenReconcile || f.provider.creates != 1 {
				t.Fatal("health check stranded recovery or repeated create")
			}
		})
	}
}

func adoptionPreview(t *testing.T, f *infraFixture) *cloudinfra.Operation {
	t.Helper()
	now := time.Now().UTC()
	c := &connections.Connection{ID: "adoption-source", OrgID: "org", Cloud: "server", Name: "Existing host", Account: "docker-real", Details: map[string]string{"host_key": "fixture-key", "docker_id": "docker-real"}, Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := f.connections.CreateConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := f.secrets.Put(t.Context(), "org", c.ID, []byte("fixture-key")); err != nil {
		t.Fatal(err)
	}
	o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "adopt", Name: "Claimed", ConnectionID: c.ID, CloudRevision: 1, ConfirmAdoption: true})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestCloudInfrastructureAdoptionClaimsConnection(t *testing.T) {
	f := newInfraFixture(t)
	o := adoptionPreview(t, f)
	// A plan may reserve an otherwise empty remote host before adoption accepts.
	tx, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'plan','reserved')`, o.ConnectionID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err = f.svc.ApplyServerOperation(ctx, f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("adoption did not serialize with reservation: %v", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review}); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("adopted reserved host: %v", err)
	}
	if _, err = f.store.GetServer(t.Context(), "org", "cloud", o.ServerID); !errors.Is(err, cloudinfra.ErrNotFound) {
		t.Fatal("rejected adoption retained partial claim")
	}
	if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_connection_uses WHERE owner_id='reserved'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review}); err != nil {
		t.Fatal(err)
	}
	checked := false
	f.runtime.beforeInspect = func() {
		checked = true
		if _, insertErr := f.sql.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'plan','during-verification')`, o.ConnectionID); insertErr == nil {
			t.Error("workload entered host during adoption verification")
		}
	}
	o = f.reconcile(t, o)
	if o.Status != "succeeded" || !checked {
		t.Fatalf("adoption verification: %+v", o)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'plan','after-verification')`, o.ConnectionID); err != nil {
		t.Fatalf("ready server admission: %v", err)
	}
}

func TestCloudInfrastructureCancelledAdoptionReleasesClaim(t *testing.T) {
	f := newInfraFixture(t)
	o := adoptionPreview(t, f)
	if _, err := f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review}); err != nil {
		t.Fatal(err)
	}
	f.runtime.reject = true
	o = f.reconcile(t, o)
	if o.Status != "failed" {
		t.Fatal("expected failed verification")
	}
	if _, err := f.svc.CancelOperation(t.Context(), f.p, "cloud", o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'plan','after-cancel')`, o.ConnectionID); err != nil {
		t.Fatalf("cancelled adoption held connection: %v", err)
	}
}

func TestCloudInfrastructureCredentialInvalidationRejectsStaleHealth(t *testing.T) {
	f := newInfraFixture(t)
	stale, err := f.connections.GetConnection(t.Context(), "org", "infra-account")
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.connections.GetConnection(t.Context(), "org", "infra-account")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.connections.BeginCredentialReplacement(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	if err = f.connections.UpdateConnection(t.Context(), stale); !errors.Is(err, connections.ErrChanged) {
		t.Fatalf("stale health overwrote credential generation: %v", err)
	}
	service := connections.NewService(f.connections, f.secrets, map[string]connections.Checker{"digitalocean": infrastructureChecker{}}, &syncAuth{}, nil, nil)
	if _, err = service.Recheck(t.Context(), f.p, current.ID); !errors.Is(err, connections.ErrUnavailable) {
		t.Fatalf("unfinished replacement rechecked old secret: %v", err)
	}
	if _, _, err = service.Credential(t.Context(), "org", current.ID); !errors.Is(err, connections.ErrNotReady) {
		t.Fatalf("unfinished replacement credential exposed: %v", err)
	}
	if _, err = service.Replace(t.Context(), f.p, current.ID, connections.ReplaceInput{Credential: "dop_v1_recovered"}); err != nil {
		t.Fatal(err)
	}
}

func TestCloudInfrastructureCredentialWritesAreSerialized(t *testing.T) {
	f := newInfraFixture(t)
	first, err := f.connections.GetConnection(t.Context(), "org", "infra-account")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.connections.WithCredentialReplacement(t.Context(), first, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	second, err := f.connections.GetConnection(t.Context(), "org", "infra-account")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	called := false
	err = f.connections.WithCredentialReplacement(ctx, second, func() error { called = true; return nil })
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("concurrent credential write entered: %v, callback=%v", err, called)
	}
}

func TestCloudInfrastructureAdoptionPinsRuntimeConfiguration(t *testing.T) {
	f := newInfraFixture(t)
	o := adoptionPreview(t, f)
	c, err := f.connections.GetConnection(t.Context(), "org", o.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	c.Details["host_key"] = "another-pinned-key"
	if err = f.connections.UpdateConnection(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if c.Generation == o.ConnectionGeneration {
		t.Fatal("runtime configuration preserved generation")
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review}); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("accepted changed host configuration: %v", err)
	}
}
