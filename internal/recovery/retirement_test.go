package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/trove"
	"github.com/xraph/trove/driver"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type retirementRecords struct {
	Store
	points map[string]*Backup
}

func (s *retirementRecords) Get(_ context.Context, org, id string) (*Backup, error) {
	b := s.points[id]
	if b == nil || b.OrgID != org {
		return nil, ErrNotFound
	}
	return b, nil
}

type retirementOperations struct {
	RetirementStore
	record   *Retirement
	cause    error
	finished bool
	claimed  int
	pending  []Retirement
}

func (s *retirementOperations) ClaimRetirement(_ context.Context, org, id, token string) (*Retirement, error) {
	if s.claimed != 0 {
		return nil, ErrConflict
	}
	if s.record.OrgID != org || s.record.ID != id || token == "" {
		return nil, ErrNotFound
	}
	s.claimed++
	r := *s.record
	r.Token, r.Status = token, "running"
	return &r, nil
}
func (s *retirementOperations) FinishRetirement(ctx context.Context, _ *Retirement, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.finished, s.cause = true, cause
	return nil
}
func (s *retirementOperations) PendingRetirements(context.Context) ([]Retirement, error) {
	return s.pending, nil
}
func (s *retirementOperations) BeginRetirement(ctx context.Context, r *Retirement, guard RetirementGuard) (*Retirement, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	s.record = r
	r.Status = "queued"
	return r, nil
}

type retirementAuthorization struct {
	calls      int
	denyAt     int
	principals []security.Principal
}

func (a *retirementAuthorization) Require(_ context.Context, p security.Principal, perm security.Permission, project string) error {
	a.calls++
	a.principals = append(a.principals, p)
	if perm != security.BackupRetire || project != "project" {
		return errors.New("incorrect retirement permission or project")
	}
	if a.denyAt > 0 && a.calls >= a.denyAt {
		return security.ErrForbidden
	}
	return nil
}

type retirementAudit struct{ events []projects.AuditEvent }

func (a *retirementAudit) Record(_ context.Context, e projects.AuditEvent) {
	a.events = append(a.events, e)
}

func retirementWorkerFixture(t *testing.T) (*Service, *retirementOperations, *retirementRecords) {
	t.Helper()
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
	store, err := backupfiles.New(files, "backups", dsn)
	if err != nil {
		t.Fatal(err)
	}
	records := &retirementRecords{points: map[string]*Backup{}}
	for _, id := range []string{"candidate", "witness"} {
		b := retentionBackup(t, id, 40, id == "witness")
		completed := time.Now().UTC().Add(-40 * 24 * time.Hour)
		b.CreatedAt, b.CompletedAt = completed.Add(-time.Minute), &completed
		if id == "witness" {
			restored := completed.Add(time.Minute)
			b.RestoredAt = &restored
		}
		b.Object, err = store.Put(t.Context(), b.objectKey(), strings.NewReader(id+" database bytes"))
		if err != nil {
			t.Fatal(err)
		}
		records.points[id] = &b
	}
	records.points["candidate"].Status = "retiring"
	key, err := retirementSourceKey(records.points["candidate"])
	if err != nil {
		t.Fatal(err)
	}
	operations := &retirementOperations{record: &Retirement{ID: "retirement", OrgID: "org", ProjectID: "project", BackupID: "candidate", WitnessID: "witness", CreatedBy: "owner", ExecutionBy: "owner", SourceKey: key, Object: records.points["candidate"].Object, WitnessObject: records.points["witness"].Object}}
	service := &Service{Store: records, Retirements: operations, Files: store, Authz: &retirementAuthorization{}, Audit: &retirementAudit{}}
	return service, operations, records
}

func TestRetirementWorkerVerifiesRetainedBytesBeforeDeleting(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "corrupt", "revoked", "changed_scope", "changed_object"} {
		t.Run(mode, func(t *testing.T) {
			s, ops, records := retirementWorkerFixture(t)
			witness := records.points["witness"]
			switch mode {
			case "missing":
				if err := s.Files.Trove.Delete(t.Context(), s.Files.Bucket, witness.Object.Key); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if _, err := s.Files.Put(t.Context(), witness.Object.Key, strings.NewReader("corrupted database bytes")); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				s.Authz = &retirementAuthorization{denyAt: 2}
			case "changed_scope":
				witness.Source.ExternalID = "another-database"
			case "changed_object":
				ops.record.Object = witness.Object
			}
			err := s.RunRetirement(t.Context(), "org", "retirement")
			if !ops.finished || (err == nil) != (mode == "valid") || (ops.cause == nil) != (mode == "valid") {
				t.Fatalf("retirement result mode=%s err=%v finish=%v cause=%v", mode, err, ops.finished, ops.cause)
			}
			_, headErr := s.Files.Trove.Head(t.Context(), s.Files.Bucket, records.points["candidate"].Object.Key)
			if mode == "valid" && !errors.Is(headErr, driver.ErrNotFound) || mode != "valid" && headErr != nil {
				t.Fatalf("candidate preservation: %v", headErr)
			}
			if mode == "valid" {
				if _, err = s.Files.Trove.Head(t.Context(), s.Files.Bucket, witness.Object.Key); err != nil {
					t.Fatalf("retained bytes deleted: %v", err)
				}
				auth := s.Authz.(*retirementAuthorization)
				if len(auth.principals) != 2 || auth.principals[1].UserID != "owner" || auth.principals[1].OrgID != "org" {
					t.Fatalf("lost current creator authority: %+v", auth)
				}
			}
			audit := s.Audit.(*retirementAudit)
			if len(audit.events) != 1 || audit.events[0].Principal.UserID != "owner" {
				t.Fatalf("missing worker audit: %+v", audit.events)
			}
			if mode != "valid" && (audit.events[0].Outcome != "failure" || audit.events[0].Severity != "warning") {
				t.Fatalf("failure was recorded as success: %+v", audit.events[0])
			}
			if err = s.RunRetirement(t.Context(), "org", "retirement"); err != nil {
				t.Fatalf("duplicate dispatch: %v", err)
			}
		})
	}
}

func TestRetirementRequestRequiresOwnerBeforeDurableIntent(t *testing.T) {
	s, ops, records := retirementWorkerFixture(t)
	ops.record = nil
	records.points["candidate"].Status = "succeeded"
	s.Authz = &retirementAuthorization{denyAt: 1}
	_, err := s.Retire(t.Context(), security.Principal{OrgID: "org", UserID: "admin"}, "candidate", RetirementInput{RequestID: "retirement-request", Policy: RetentionInput{OlderThanDays: 7, KeepLast: 1}})
	if !errors.Is(err, security.ErrForbidden) || ops.record != nil {
		t.Fatalf("unauthorized intent: %+v %v", ops.record, err)
	}
}

func TestRetirementEnqueueGapRemainsReconcileable(t *testing.T) {
	s, ops, records := retirementWorkerFixture(t)
	records.points["candidate"].Status = "succeeded"
	unavailable := errors.New("queue unavailable")
	s.EnqueueRetirement = func(context.Context, string, string) error { return unavailable }
	r, err := s.Retire(t.Context(), security.Principal{OrgID: "org", UserID: "owner"}, "candidate", RetirementInput{RequestID: "retirement-request", Policy: RetentionInput{OlderThanDays: 7, KeepLast: 1}})
	if !errors.Is(err, unavailable) || r == nil || ops.record != r {
		t.Fatalf("lost durable intent: %+v %v", r, err)
	}
	ops.pending = []Retirement{*r}
	var queued []string
	s.EnqueueRetirement = func(_ context.Context, org, id string) error { queued = append(queued, org+"/"+id); return nil }
	if err = s.reconcileRetirements(t.Context()); err != nil || len(queued) != 1 || queued[0] != "org/"+r.ID {
		t.Fatalf("enqueue repair: %v %v", queued, err)
	}
}

type retirementPendingBackups struct {
	Store
	err error
}

func (s retirementPendingBackups) Pending(context.Context) ([]Backup, error) { return nil, s.err }
func TestReconciliationFailureDoesNotStarveRetirement(t *testing.T) {
	unavailable := errors.New("backup reconciliation failed")
	queued := false
	s := &Service{Store: retirementPendingBackups{err: unavailable}, Retirements: &retirementOperations{pending: []Retirement{{OrgID: "org", ID: "retirement"}}}, EnqueueRetirement: func(context.Context, string, string) error { queued = true; return nil }}
	if err := s.Reconcile(t.Context()); !errors.Is(err, unavailable) || !queued {
		t.Fatalf("retirement was starved: queued=%v err=%v", queued, err)
	}
}

func TestCancelledRetirementPreservesCandidateAndFinishesClaim(t *testing.T) {
	s, ops, records := retirementWorkerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.RunRetirement(ctx, "org", "retirement"); !errors.Is(err, context.Canceled) || !ops.finished || !errors.Is(ops.cause, context.Canceled) {
		t.Fatalf("cancelled claim not published safely: %v finished=%v cause=%v", err, ops.finished, ops.cause)
	}
	if _, err := s.Files.Trove.Head(t.Context(), s.Files.Bucket, records.points["candidate"].Object.Key); err != nil {
		t.Fatalf("cancelled worker deleted bytes: %v", err)
	}
}

func TestRetirementPreflightDoesNotLeaveUnsupportedIntent(t *testing.T) {
	s, ops, records := retirementWorkerFixture(t)
	ops.record = nil
	records.points["candidate"].Object.DestinationID = ""
	_, err := s.Retire(t.Context(), security.Principal{OrgID: "org", UserID: "owner"}, "candidate", RetirementInput{RequestID: "retirement-request", Policy: RetentionInput{OlderThanDays: 7, KeepLast: 1}})
	if err == nil || ops.record != nil {
		t.Fatalf("unqualified destination left an intent: %+v %v", ops.record, err)
	}
}

func TestRetirementWorkerUsesRetryOwnerWithoutRewritingCreator(t *testing.T) {
	s, ops, _ := retirementWorkerFixture(t)
	ops.record.ExecutionBy = "replacement-owner"
	if err := s.RunRetirement(t.Context(), "org", "retirement"); err != nil {
		t.Fatal(err)
	}
	auth := s.Authz.(*retirementAuthorization)
	if len(auth.principals) != 2 || auth.principals[0].UserID != "replacement-owner" || auth.principals[1].UserID != "replacement-owner" || ops.record.CreatedBy != "owner" {
		t.Fatalf("worker attributed retry to creator: %+v %+v", auth.principals, ops.record)
	}
	audit := s.Audit.(*retirementAudit)
	if audit.events[0].Principal.UserID != "replacement-owner" {
		t.Fatalf("retry audit lost actor: %+v", audit.events)
	}
}

func (s *retirementOperations) GetRetirement(_ context.Context, org, id string) (*Retirement, error) {
	if s.record == nil || s.record.OrgID != org || s.record.ID != id {
		return nil, ErrNotFound
	}
	return s.record, nil
}
func (s *retirementOperations) RetryRetirement(ctx context.Context, org, id, actor, key string, guard RetirementGuard) (*Retirement, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	r, err := s.GetRetirement(ctx, org, id)
	if err != nil {
		return nil, err
	}
	r.Status, r.ExecutionBy, r.RequestID = "queued", actor, key
	return r, nil
}

func TestRetirementRetryQueuesAndAuditsTheAuthorizedOwner(t *testing.T) {
	s, ops, _ := retirementWorkerFixture(t)
	unavailable := errors.New("dispatch unavailable")
	s.EnqueueRetirement = func(_ context.Context, org, id string) error {
		if org != "org" || id != "retirement" {
			t.Fatal("lost retry scope")
		}
		return unavailable
	}
	p := security.Principal{OrgID: "org", UserID: "replacement-owner"}
	r, err := s.RetryRetirement(t.Context(), p, "retirement", RetirementRetryInput{RequestID: "retry-request-key"})
	if !errors.Is(err, unavailable) || r == nil || r.ExecutionBy != p.UserID || r.CreatedBy != "owner" || ops.record.RequestID != "retry-request-key" {
		t.Fatalf("retry lost durable intent or actor: %+v %v", r, err)
	}
	audit := s.Audit.(*retirementAudit)
	if len(audit.events) != 1 || audit.events[0].Principal != p || audit.events[0].Action != "service.backup.retirement.retry" {
		t.Fatalf("retry audit attributed to creator: %+v", audit.events)
	}
}

func TestDeniedRetryAndHistoryCannotReachStores(t *testing.T) {
	s, _, _ := retirementWorkerFixture(t)
	s.Authz = denied{}
	p := security.Principal{OrgID: "org", UserID: "admin"}
	if _, err := s.RetryRetirement(t.Context(), p, "retirement", RetirementRetryInput{RequestID: "retry-request-key"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("retry denial: %v", err)
	}
	if _, err := s.RetirementHistory(t.Context(), p, "retirement", RetirementHistoryInput{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("history denial: %v", err)
	}
}

func TestRetirementHistoryBoundsRejectMalformedOrUnboundedPages(t *testing.T) {
	for _, in := range []RetirementHistoryInput{{Limit: -1}, {Limit: 101}, {Before: "-1"}, {Before: "0"}, {Before: "01"}, {Before: "+1"}, {Before: "9223372036854775808"}, {Before: "1;SELECT"}} {
		if _, _, err := in.bounds(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted history input: %+v %v", in, err)
		}
	}
	limit, before, err := (RetirementHistoryInput{}).bounds()
	if err != nil || limit != 50 || before != 0 {
		t.Fatalf("history defaults: %d %d %v", limit, before, err)
	}
}
