//go:build integration

package integration

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/recovery"
)

func dailyPolicy(id string) *recovery.Policy {
	return &recovery.Policy{ID: id, OrgID: "o", ProjectID: "p", EnvironmentID: "e", Environment: "prod", Resource: "data", EveryHours: 24, Enabled: true, CreatedBy: "deployer", NextAt: time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)}
}

func TestDefaultBackupPolicyPreservesOperatorChangesAfterRestart(t *testing.T) {
	conn, store := retirementFixture(t)
	in := dailyPolicy("operator-policy")
	in.Enabled, in.EveryHours, in.CreatedBy = false, 72, "operator"
	in.NextAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	existing, err := store.PutPolicy(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_backup_policies SET last_error='permission revoked' WHERE id=$1`, in.ID); err != nil {
		t.Fatal(err)
	}
	existing.LastError = "permission revoked"
	pg := pgdriver.New()
	if err = pg.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	reopened := recovery.NewPgStore(pg)
	got, created, err := reopened.EnsurePolicy(t.Context(), dailyPolicy("new-id"))
	if err != nil || created || !reflect.DeepEqual(got, existing) {
		t.Fatalf("default overwrote operator policy: got %+v, want %+v, created %v, err %v", got, existing, created, err)
	}
	// An explicit edit still changes the policy through the original API.
	edited := dailyPolicy("ignored-id")
	edited.EveryHours, edited.CreatedBy = 12, "new-operator"
	got, err = reopened.PutPolicy(t.Context(), edited)
	if err != nil || got.ID != existing.ID || got.EveryHours != 12 || !got.Enabled || got.CreatedBy != "new-operator" || got.LastError != "" {
		t.Fatalf("manual edit: %+v, %v", got, err)
	}
	foreign := dailyPolicy("foreign-id")
	foreign.ProjectID = "other-project"
	got, created, err = reopened.EnsurePolicy(t.Context(), foreign)
	if !errors.Is(err, recovery.ErrNotFound) || got != nil || created {
		t.Fatalf("foreign project exposed policy: %+v, %v, %v", got, created, err)
	}
}

func TestConcurrentDefaultBackupPoliciesKeepOneCreatorAndDueTime(t *testing.T) {
	_, store := retirementFixture(t)
	const workers = 12
	type result struct {
		policy  *recovery.Policy
		created bool
		err     error
	}
	results := make(chan result, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			in := dailyPolicy(fmt.Sprintf("policy-%d", i))
			in.CreatedBy = fmt.Sprintf("deployer-%d", i)
			<-start
			p, created, err := store.EnsurePolicy(t.Context(), in)
			results <- result{p, created, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var winner *recovery.Policy
	createdCount := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.created {
			createdCount++
		}
		if winner == nil {
			winner = r.policy
		}
		if !reflect.DeepEqual(r.policy, winner) {
			t.Fatalf("concurrent defaults differ: %+v versus %+v", r.policy, winner)
		}
	}
	list, err := store.ListPolicies(t.Context(), "o", "p")
	if err != nil || createdCount != 1 || len(list) != 1 || list[0].ID != winner.ID {
		t.Fatalf("policy count: created %d, stored %+v, err %v", createdCount, list, err)
	}
}

func TestDefaultBackupPolicyDoesNotPostponeDueBackup(t *testing.T) {
	_, store := retirementFixture(t)
	in := dailyPolicy("due-policy")
	in.NextAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if _, created, err := store.EnsurePolicy(t.Context(), in); err != nil || !created {
		t.Fatalf("create: %v, %v", created, err)
	}
	got, created, err := store.EnsurePolicy(t.Context(), dailyPolicy("retry-policy"))
	if err != nil || created || !got.NextAt.Equal(in.NextAt) {
		t.Fatalf("due time moved: %+v, %v, %v", got, created, err)
	}
	due, err := store.DuePolicies(t.Context())
	if err != nil || len(due) != 1 || due[0].ID != in.ID {
		t.Fatalf("scheduler lost due policy: %+v, %v", due, err)
	}
	if err = store.AdvancePolicy(t.Context(), &due[0], nil); err != nil {
		t.Fatal(err)
	}
	due, err = store.DuePolicies(t.Context())
	if err != nil || len(due) != 0 {
		t.Fatalf("scheduler did not advance: %+v, %v", due, err)
	}
}
