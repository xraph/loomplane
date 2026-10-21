//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/trove"
	"github.com/xraph/trove/driver"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

type retentionSchedulerAuthority struct{ revoked bool }

func (a *retentionSchedulerAuthority) Require(_ context.Context, p security.Principal, permission security.Permission, project string) error {
	if a.revoked || p.OrgID != "o" || p.UserID == "" || project != "p" || p.ReadOnly && !permission.Reads() {
		return security.ErrForbidden
	}
	if permission == security.BackupRead || permission == security.BackupRetire && p.UserID == "u" {
		return nil
	}
	return security.ErrForbidden
}

func retentionSchedulerFixture(t *testing.T) (*pgx.Conn, *recovery.Service, *recovery.PgStore, *backupfiles.Store, *retentionSchedulerAuthority) {
	t.Helper()
	conn, records := retirementFixture(t)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at)
 SELECT 'b2',org_id,project_id,environment_id,connection_id,name,status,'second-old-backup',source,jsonb_set(object,'{key}','"o/p/e/b2/attempt.dump"'),token,created_by,now()-interval '41 days 1 minute',now()-interval '41 days' FROM loomplane_service_backups WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	dsn := "file://" + t.TempDir()
	drv := localdriver.New()
	if err := drv.Open(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	files, err := trove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close(context.Background()) })
	if err = files.CreateBucket(t.Context(), "backups"); err != nil {
		t.Fatal(err)
	}
	storage, err := backupfiles.New(files, "backups", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "b2", "w"} {
		object, putErr := storage.Put(t.Context(), "o/p/e/"+id+"/attempt.dump", strings.NewReader(id+" database bytes"))
		if putErr != nil {
			t.Fatal(putErr)
		}
		raw, marshalErr := json.Marshal(object)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET object=$1 WHERE id=$2`, raw, id); err != nil {
			t.Fatal(err)
		}
	}
	auth := &retentionSchedulerAuthority{}
	service := &recovery.Service{Store: records, Retirements: records, RetentionPolicies: records, Files: storage, Authz: auth, EnqueueRetirement: func(context.Context, string, string) error { return nil }}
	return conn, service, records, storage, auth
}
func enableScheduledRetention(t *testing.T, service *recovery.Service) *recovery.RetentionPolicy {
	t.Helper()
	policy, err := service.SetRetentionPolicy(t.Context(), security.Principal{OrgID: "o", UserID: "u"}, "w", recovery.RetentionPolicyInput{Enabled: true, Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
func dueRetentionNow(t *testing.T, conn *pgx.Conn, id string) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_retention_policies SET next_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}
func policyRetirements(t *testing.T, store *recovery.PgStore) []recovery.Retirement {
	t.Helper()
	page, err := store.ListRetirements(t.Context(), "o", "p", recovery.RetirementListInput{})
	if err != nil {
		t.Fatal(err)
	}
	return page.Retirements
}

func TestRetentionSchedulerRetiresOneOldestPointPerDueEvaluation(t *testing.T) {
	conn, service, records, files, _ := retentionSchedulerFixture(t)
	policy := enableScheduledRetention(t, service)
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	operations := policyRetirements(t, records)
	if len(operations) != 1 || operations[0].BackupID != "b2" || operations[0].RetentionPolicyID != policy.ID || operations[0].RetentionRevision != 1 {
		t.Fatalf("selection or policy provenance: %+v", operations)
	}
	refused := restoreAdmission("scheduled-blocked-restore")
	refused.BackupID = "b2"
	if _, err := records.CreateRestore(t.Context(), refused); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("scheduled candidate accepted restore: %v", err)
	}
	if err := service.RunRetirement(t.Context(), "o", operations[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Trove.Head(t.Context(), files.Bucket, "o/p/e/b2/attempt.dump"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("scheduled bytes retained: %v", err)
	}
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if list := policyRetirements(t, records); len(list) != 1 {
		t.Fatalf("one due evaluation admitted multiple points: %+v", list)
	}
	dueRetentionNow(t, conn, policy.ID)
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	operations = policyRetirements(t, records)
	if len(operations) != 2 || operations[0].BackupID != "b" {
		t.Fatalf("next evaluation: %+v", operations)
	}
	if err := service.RunRetirement(t.Context(), "o", operations[0].ID); err != nil {
		t.Fatal(err)
	}
	witness, err := records.Get(t.Context(), "o", "w")
	if err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if err = files.CopyVerified(t.Context(), witness.Object, &original); err != nil || original.String() != "w database bytes" {
		t.Fatalf("tested replacement changed: %q %v", original.String(), err)
	}
}

type retentionProgressUnavailable struct {
	recovery.RetentionPolicyStore
	failure error
}

func (s *retentionProgressUnavailable) AdvanceRetentionPolicy(ctx context.Context, p *recovery.RetentionPolicy, id string, cause error) error {
	if s.failure != nil {
		return s.failure
	}
	return s.RetentionPolicyStore.AdvanceRetentionPolicy(ctx, p, id, cause)
}

func TestRetentionSchedulerReopensAfterAcceptedWorkAndLostProgress(t *testing.T) {
	conn, service, records, files, _ := retentionSchedulerFixture(t)
	policy := enableScheduledRetention(t, service)
	unavailable := errors.New("progress unavailable")
	service.RetentionPolicies = &retentionProgressUnavailable{RetentionPolicyStore: records, failure: unavailable}
	service.EnqueueRetirement = func(ctx context.Context, org, id string) error { return service.RunRetirement(ctx, org, id) }
	if err := service.Reconcile(t.Context()); !errors.Is(err, unavailable) {
		t.Fatalf("progress failure disappeared: %v", err)
	}
	original, err := records.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || !original.NextAt.Equal(policy.NextAt) {
		t.Fatalf("failed progress advanced due time: %+v %v", original, err)
	}
	pg := pgdriver.New()
	if err = pg.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	reopened := recovery.NewPgStore(pg)
	service.Store, service.Retirements, service.RetentionPolicies = reopened, reopened, reopened
	if err = service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	list := policyRetirements(t, reopened)
	if len(list) != 1 || list[0].Status != "succeeded" || list[0].BackupID != "b2" {
		t.Fatalf("reopen created another selection: %+v", list)
	}
	after, err := reopened.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || after.LastRetirementID != list[0].ID || !after.NextAt.After(policy.NextAt) {
		t.Fatalf("accepted operation lost after reopen: %+v %v", after, err)
	}
	if _, err = files.Trove.Head(t.Context(), files.Bucket, "o/p/e/b/attempt.dump"); err != nil {
		t.Fatalf("replay deleted a second backup: %v", err)
	}
}

type retentionObservedDue struct {
	recovery.RetentionPolicyStore
	observed recovery.RetentionPolicy
}

func (s retentionObservedDue) DueRetentionPolicies(context.Context) ([]recovery.RetentionPolicy, error) {
	return []recovery.RetentionPolicy{s.observed}, nil
}

func TestRetentionSchedulerConcurrentPassesShareOneRequestEvenWithFastWorker(t *testing.T) {
	_, service, records, _, _ := retentionSchedulerFixture(t)
	policy := enableScheduledRetention(t, service)
	service.RetentionPolicies = retentionObservedDue{RetentionPolicyStore: records, observed: *policy}
	service.EnqueueRetirement = func(ctx context.Context, org, id string) error { return service.RunRetirement(ctx, org, id) }
	results := make(chan error, 8)
	var start sync.WaitGroup
	start.Add(1)
	for range 8 {
		go func() { start.Wait(); results <- service.Reconcile(t.Context()) }()
	}
	start.Done()
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	list := policyRetirements(t, records)
	if len(list) != 1 || list[0].Status != "succeeded" || list[0].BackupID != "b2" || list[0].Attempt != 1 {
		t.Fatalf("simultaneous passes granted extra work: %+v", list)
	}
}

func TestRetentionSchedulerPersistsCancellationThenRepairsTheDispatchGap(t *testing.T) {
	_, service, records, files, _ := retentionSchedulerFixture(t)
	policy := enableScheduledRetention(t, service)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service.EnqueueRetirement = func(ctx context.Context, _ string, _ string) error { cancel(); return ctx.Err() }
	if err := service.Reconcile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled enqueue: %v", err)
	}
	saved, err := records.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || saved.LastRetirementID == "" || saved.LastError == "" {
		t.Fatalf("accepted operation lost on cancellation: %+v %v", saved, err)
	}
	service.EnqueueRetirement = func(ctx context.Context, org, id string) error { return service.RunRetirement(ctx, org, id) }
	if err = service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := records.GetRetirement(t.Context(), "o", saved.LastRetirementID)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("dispatch gap not repaired: %+v %v", result, err)
	}
	if _, err = files.Trove.Head(t.Context(), files.Bucket, "o/p/e/b2/attempt.dump"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("repaired dispatch did not delete candidate: %v", err)
	}
}

func TestRetentionSchedulerStopsForRevocationPauseAndMissingRestoreEvidence(t *testing.T) {
	for _, mode := range []string{"revoked", "paused", "unverified", "changed-destination"} {
		t.Run(mode, func(t *testing.T) {
			conn, service, records, files, auth := retentionSchedulerFixture(t)
			policy := enableScheduledRetention(t, service)
			switch mode {
			case "revoked":
				auth.revoked = true
			case "paused":
				service.Files = nil
				if _, err := service.SetRetentionPolicy(t.Context(), security.Principal{OrgID: "o", UserID: "u"}, "w", recovery.RetentionPolicyInput{Revision: policy.Revision, Policy: policy.Policy, Enabled: false}); err != nil {
					t.Fatal(err)
				}
			case "unverified":
				if _, err := conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET restored_at=NULL`); err != nil {
					t.Fatal(err)
				}
			case "changed-destination":
				moved, err := backupfiles.New(files.Trove, files.Bucket, "file://"+t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				service.Files = moved
			}
			err := service.Reconcile(t.Context())
			if (err != nil) != (mode != "paused") {
				t.Fatalf("policy state %s: %v", mode, err)
			}
			if list := policyRetirements(t, records); len(list) != 0 {
				t.Fatalf("unsafe scheduled deletion: %+v", list)
			}
			for _, id := range []string{"b", "b2", "w"} {
				if _, err = files.Trove.Head(t.Context(), files.Bucket, "o/p/e/"+id+"/attempt.dump"); err != nil {
					t.Fatalf("policy %s changed %s: %v", mode, id, err)
				}
			}
		})
	}
}

func TestRetentionSchedulerFailureDoesNotGrantAnotherRetryBudget(t *testing.T) {
	conn, service, records, files, _ := retentionSchedulerFixture(t)
	policy := enableScheduledRetention(t, service)
	if _, err := files.Put(t.Context(), "o/p/e/w/attempt.dump", strings.NewReader("changed witness")); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	list := policyRetirements(t, records)
	if len(list) != 1 {
		t.Fatalf("initial intent: %+v", list)
	}
	if err := service.RunRetirement(t.Context(), "o", list[0].ID); err == nil {
		t.Fatal("corrupt witness accepted")
	}
	dueRetentionNow(t, conn, policy.ID)
	if err := service.Reconcile(t.Context()); err == nil {
		t.Fatal("failed source hold hidden")
	}
	after := policyRetirements(t, records)
	if len(after) != 1 || after[0].Attempt != 1 || after[0].AttemptLimit != 3 || after[0].Status != "failed" {
		t.Fatalf("scheduler silently granted retry authority: %+v", after)
	}
	if _, err := files.Trove.Head(t.Context(), files.Bucket, "o/p/e/b2/attempt.dump"); err != nil {
		t.Fatalf("failed worker deleted candidate: %v", err)
	}
}

func TestRetentionSchedulerFailureDoesNotStarveAnotherSource(t *testing.T) {
	conn, service, records, files, _ := retentionSchedulerFixture(t)
	enableScheduledRetention(t, service)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at,restored_at)
 SELECT id||'-other',org_id,project_id,environment_id,connection_id,name,status,request_id||'-other',jsonb_set(source,'{external_id}','"other-physical-source"'),jsonb_set(object,'{key}',to_jsonb('o/p/e/'||id||'-other/attempt.dump')),token,created_by,created_at,completed_at,restored_at FROM loomplane_service_backups`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b-other", "b2-other", "w-other"} {
		object, err := files.Put(t.Context(), "o/p/e/"+id+"/attempt.dump", strings.NewReader(id+" database bytes"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET object=$1 WHERE id=$2`, raw, id); err != nil {
			t.Fatal(err)
		}
	}
	second, err := service.SetRetentionPolicy(t.Context(), security.Principal{OrgID: "o", UserID: "u"}, "w-other", recovery.RetentionPolicyInput{Enabled: true, Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET restored_at=NULL WHERE id='w'`); err != nil {
		t.Fatal(err)
	}
	if err = service.Reconcile(t.Context()); err == nil {
		t.Fatal("first policy failure disappeared")
	}
	list := policyRetirements(t, records)
	if len(list) != 1 || list[0].BackupID != "b2-other" || list[0].RetentionPolicyID != second.ID {
		t.Fatalf("first policy starved later source: %+v", list)
	}
}

func TestRetentionPolicyManagementRejectsReadKeyAndAllowsInspection(t *testing.T) {
	_, service, records, _, _ := retentionSchedulerFixture(t)
	readKey := security.Principal{OrgID: "o", UserID: "u", ReadOnly: true}
	if _, err := service.SetRetentionPolicy(t.Context(), readKey, "w", recovery.RetentionPolicyInput{Enabled: true, Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read key granted recurring deletion: %v", err)
	}
	if list, err := records.ListRetentionPolicies(t.Context(), "o", "p"); err != nil || len(list) != 0 {
		t.Fatalf("denied mutation persisted: %+v %v", list, err)
	}
	policy := enableScheduledRetention(t, service)
	found, err := service.RetentionPolicyForBackup(t.Context(), readKey, "b")
	if err != nil || found == nil || found.ID != policy.ID {
		t.Fatalf("read key could not inspect policy: %+v %v", found, err)
	}
	if _, err = service.RetentionPolicyForBackup(t.Context(), security.Principal{OrgID: "foreign", UserID: "u"}, "b"); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatalf("foreign policy inspection: %v", err)
	}
}

type retentionDelayedLookup struct {
	recovery.RetirementStore
	missed chan struct{}
	resume chan struct{}
	once   sync.Once
}

func (s *retentionDelayedLookup) RetirementByRequest(ctx context.Context, org, project, key string) (*recovery.Retirement, error) {
	result, err := s.RetirementStore.RetirementByRequest(ctx, org, project, key)
	s.once.Do(func() {
		close(s.missed)
		select {
		case <-s.resume:
		case <-ctx.Done():
		}
	})
	return result, err
}

func TestRetentionSchedulerStaleDueTimeCannotAdmitAfterProgressAdvanced(t *testing.T) {
	_, service, records, files, _ := retentionSchedulerFixture(t)
	policy := enableScheduledRetention(t, service)
	service.RetentionPolicies = retentionObservedDue{RetentionPolicyStore: records, observed: *policy}
	delayed := &retentionDelayedLookup{RetirementStore: records, missed: make(chan struct{}), resume: make(chan struct{})}
	service.Retirements = delayed
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var released sync.Once
	release := func() { released.Do(func() { close(delayed.resume) }) }
	defer release()
	result := make(chan error, 1)
	go func() { result <- service.Reconcile(ctx) }()
	select {
	case <-delayed.missed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := records.AdvanceRetentionPolicy(t.Context(), policy, "", nil); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("stale evaluation should stop cleanly: %v", err)
	}
	if list := policyRetirements(t, records); len(list) != 0 {
		t.Fatalf("expired due-time snapshot admitted deletion: %+v", list)
	}
	if _, err := files.Trove.Head(t.Context(), files.Bucket, "o/p/e/b2/attempt.dump"); err != nil {
		t.Fatalf("stale evaluation changed bytes: %v", err)
	}
}

func TestRetentionSchedulerResolvesSelectionConflictAfterAnotherWorkerFinishes(t *testing.T) {
	_, first, records, files, _ := retentionSchedulerFixture(t)
	policy := enableScheduledRetention(t, first)
	first.RetentionPolicies = retentionObservedDue{RetentionPolicyStore: records, observed: *policy}
	first.EnqueueRetirement = func(ctx context.Context, org, id string) error { return first.RunRetirement(ctx, org, id) }
	second := *first
	delayed := &retentionDelayedLookup{RetirementStore: records, missed: make(chan struct{}), resume: make(chan struct{})}
	second.Retirements = delayed
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var released sync.Once
	release := func() { released.Do(func() { close(delayed.resume) }) }
	defer release()
	result := make(chan error, 1)
	go func() { result <- second.Reconcile(ctx) }()
	select {
	case <-delayed.missed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := first.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("conflicting selection lost accepted intent: %v", err)
	}
	list := policyRetirements(t, records)
	if len(list) != 1 || list[0].BackupID != "b2" || list[0].Status != "succeeded" {
		t.Fatalf("late selection duplicated work: %+v", list)
	}
	if _, err := files.Trove.Head(t.Context(), files.Bucket, "o/p/e/b/attempt.dump"); err != nil {
		t.Fatalf("late scheduler deleted another candidate: %v", err)
	}
}

// Hide a committed response and its immediate resolution read. Progress still
// reaches the real database and must recover the accepted evaluation itself.
type retentionLostAdmission struct {
	recovery.RetirementStore
	committed bool
	lost      error
}

func (s *retentionLostAdmission) BeginRetirement(ctx context.Context, in *recovery.Retirement, guard recovery.RetirementGuard) (*recovery.Retirement, error) {
	result, err := s.RetirementStore.BeginRetirement(ctx, in, guard)
	if err != nil {
		return result, err
	}
	s.committed = true
	return nil, s.lost
}
func (s *retentionLostAdmission) RetirementByRequest(ctx context.Context, org, project, key string) (*recovery.Retirement, error) {
	if s.committed {
		return nil, s.lost
	}
	return s.RetirementStore.RetirementByRequest(ctx, org, project, key)
}
func TestRetentionSchedulerProgressResolvesCommittedAdmissionAfterLostReads(t *testing.T) {
	_, service, records, _, _ := retentionSchedulerFixture(t)
	policy := enableScheduledRetention(t, service)
	lost := errors.New("admission response and resolution read unavailable")
	service.Retirements = &retentionLostAdmission{RetirementStore: records, lost: lost}
	if err := service.Reconcile(t.Context()); !errors.Is(err, lost) {
		t.Fatalf("lost read hidden: %v", err)
	}
	list := policyRetirements(t, records)
	if len(list) != 1 {
		t.Fatalf("accepted work: %+v", list)
	}
	saved, err := records.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || saved.LastRetirementID != list[0].ID || !saved.NextAt.After(policy.NextAt) {
		t.Fatalf("progress discarded accepted request: %+v %v", saved, err)
	}
	service.Retirements = records
	if err = service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := policyRetirements(t, records); len(after) != 1 {
		t.Fatalf("recovery duplicated selection: %+v", after)
	}
}

func TestRetentionSchedulerEmptySelectionLinksConcurrentCompletedAdmission(t *testing.T) {
	conn, first, records, _, _ := retentionSchedulerFixture(t)
	// Only b2 is old enough. Once it is retired, the delayed pass selects nothing.
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET completed_at=now() WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	policy := enableScheduledRetention(t, first)
	first.RetentionPolicies = &retentionProgressUnavailable{RetentionPolicyStore: records, failure: errors.New("admitting pass stopped before progress")}
	first.EnqueueRetirement = func(ctx context.Context, org, id string) error { return first.RunRetirement(ctx, org, id) }
	second := *first
	second.RetentionPolicies = retentionObservedDue{RetentionPolicyStore: records, observed: *policy}
	delayed := &retentionDelayedLookup{RetirementStore: records, missed: make(chan struct{}), resume: make(chan struct{})}
	second.Retirements = delayed
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var released sync.Once
	release := func() { released.Do(func() { close(delayed.resume) }) }
	defer release()
	result := make(chan error, 1)
	go func() { result <- second.Reconcile(ctx) }()
	select {
	case <-delayed.missed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := first.Reconcile(t.Context()); err == nil {
		t.Fatal("progress failure hidden")
	}
	release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	list := policyRetirements(t, records)
	if len(list) != 1 || list[0].Status != "succeeded" {
		t.Fatalf("completed work: %+v", list)
	}
	saved, err := records.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || saved.LastRetirementID != list[0].ID || !saved.NextAt.After(policy.NextAt) {
		t.Fatalf("empty selection discarded accepted request: %+v %v", saved, err)
	}
	// The original pass cannot clear the linked result with a stale observation.
	if err = records.AdvanceRetentionPolicy(t.Context(), policy, list[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	after, err := records.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || after.LastRetirementID != list[0].ID || !after.NextAt.Equal(saved.NextAt) {
		t.Fatalf("stale completion changed progress: %+v %v", after, err)
	}
}
