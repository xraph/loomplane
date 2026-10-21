package recovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

type policyServiceAuthorization struct {
	denied        bool
	calls, denyAt int
	principals    []security.Principal
}

func (a *policyServiceAuthorization) Require(_ context.Context, p security.Principal, perm security.Permission, project string) error {
	a.calls++
	a.principals = append(a.principals, p)
	if a.denied || a.denyAt > 0 && a.calls >= a.denyAt || p.OrgID != "org" || p.UserID == "" || project != "project" || perm != security.BackupRead && perm != security.BackupRetire {
		return security.ErrForbidden
	}
	return nil
}

type policyProgress struct {
	observed   RetentionPolicy
	retirement string
	cause      error
}
type policyServiceStore struct {
	RetentionPolicyStore
	saved      *RetentionPolicy
	due        []RetentionPolicy
	progress   []policyProgress
	advanceErr error
	writes     int
	expected   int64
}

func (p *policyServiceStore) PutRetentionPolicy(ctx context.Context, in *RetentionPolicy, expected int64, guard RetirementGuard) (*RetentionPolicy, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	if err := guard(ctx); err != nil {
		return nil, err
	}
	snapshot := *in
	snapshot.Revision = expected + 1
	p.saved = &snapshot
	p.writes++
	p.expected = expected
	return &snapshot, nil
}
func (p *policyServiceStore) FindRetentionPolicy(context.Context, string, string) (*RetentionPolicy, error) {
	if p.saved == nil {
		return nil, ErrNotFound
	}
	return p.saved, nil
}
func (p *policyServiceStore) DueRetentionPolicies(context.Context) ([]RetentionPolicy, error) {
	return p.due, nil
}
func (p *policyServiceStore) AdvanceRetentionPolicy(ctx context.Context, in *RetentionPolicy, retirement string, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.progress = append(p.progress, policyProgress{*in, retirement, cause})
	return p.advanceErr
}

type policyServiceRecords struct {
	*retirementRecords
	entries                    []RetentionEntry
	pendingErr, errorInventory error
}

func (r *policyServiceRecords) RetentionInventory(context.Context, *Backup) ([]RetentionEntry, error) {
	return r.entries, r.errorInventory
}
func (r *policyServiceRecords) Pending(context.Context) ([]Backup, error) { return nil, r.pendingErr }

type policyServiceOperations struct {
	RetirementStore
	requests     map[string]*Retirement
	active       *Retirement
	records      *policyServiceRecords
	begun        int
	afterBegin   func()
	lostResponse bool
}

func (r *policyServiceOperations) RetirementByRequest(ctx context.Context, org, project, key string) (*Retirement, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	found := r.requests[key]
	if found == nil || found.OrgID != org || found.ProjectID != project {
		return nil, ErrNotFound
	}
	return found, nil
}
func (r *policyServiceOperations) ActiveRetirement(_ context.Context, org, source string) (*Retirement, error) {
	if r.active == nil || r.active.OrgID != org || r.active.SourceKey != source {
		return nil, ErrNotFound
	}
	return r.active, nil
}
func (r *policyServiceOperations) PendingRetirements(context.Context) ([]Retirement, error) {
	return nil, nil
}
func (r *policyServiceOperations) BeginRetirement(ctx context.Context, in *Retirement, guard RetirementGuard) (*Retirement, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	if found := r.requests[in.RequestID]; found != nil {
		return found, nil
	}
	r.begun++
	saved := *in
	saved.Status = "queued"
	saved.ExecutionBy = saved.CreatedBy
	var err error
	saved.SourceKey, err = retirementSourceKey(r.records.points[saved.BackupID])
	if err != nil {
		return nil, err
	}
	r.requests[in.RequestID] = &saved
	r.active = &saved
	r.records.points[saved.BackupID].Status = "retiring"
	if r.afterBegin != nil {
		r.afterBegin()
	}
	if r.lostResponse {
		return nil, errors.New("response lost after commit")
	}
	return &saved, nil
}
func policyServiceFixture(t *testing.T) (*Service, *policyServiceStore, *policyServiceOperations, *policyServiceRecords) {
	t.Helper()
	service, _, base := retirementWorkerFixture(t)
	base.points["candidate"].Status = "succeeded"
	records := &policyServiceRecords{retirementRecords: base, entries: []RetentionEntry{{Backup: *base.points["witness"]}, {Backup: *base.points["candidate"]}}}
	source, err := retirementSourceKey(base.points["candidate"])
	if err != nil {
		t.Fatal(err)
	}
	policy := RetentionPolicy{ID: "retention-policy", OrgID: "org", ProjectID: "project", AnchorID: "witness", SourceKey: source, Revision: 1, Policy: RetentionInput{OlderThanDays: 7, KeepLast: 1}, Enabled: true, AuthorizedBy: "owner", NextAt: time.Date(2026, 10, 21, 1, 2, 3, 456000, time.UTC)}
	policies := &policyServiceStore{saved: &policy, due: []RetentionPolicy{policy}}
	operations := &policyServiceOperations{requests: map[string]*Retirement{}, records: records}
	service.Store, service.Retirements, service.RetentionPolicies = records, operations, policies
	service.Authz = &policyServiceAuthorization{}
	service.EnqueueRetirement = func(ctx context.Context, _ string, _ string) error { return ctx.Err() }
	return service, policies, operations, records
}

func TestSetRetentionPolicyRequiresOwnerAndStorageButPauseSurvivesMissingStorage(t *testing.T) {
	for _, mode := range []string{"enabled", "denied", "revoked", "unconfigured", "legacy", "pause"} {
		t.Run(mode, func(t *testing.T) {
			service, policies, _, records := policyServiceFixture(t)
			input := RetentionPolicyInput{Revision: 4, Enabled: true, Policy: RetentionInput{OlderThanDays: 30, KeepLast: 7}}
			switch mode {
			case "denied":
				service.Authz.(*policyServiceAuthorization).denied = true
			case "revoked":
				service.Authz.(*policyServiceAuthorization).denyAt = 2
			case "unconfigured":
				service.Files = nil
			case "legacy":
				records.points["candidate"].Object.DestinationID = ""
			case "pause":
				service.Files = nil
				input.Enabled = false
			}
			policy, err := service.SetRetentionPolicy(t.Context(), security.Principal{OrgID: "org", UserID: "owner"}, "candidate", input)
			success := mode == "enabled" || mode == "pause"
			if (err == nil) != success || (policies.writes == 1) != success {
				t.Fatalf("policy mutation %s: %+v %v writes=%d", mode, policy, err, policies.writes)
			}
			if success && (policy.OrgID != "org" || policy.ProjectID != "project" || policy.AuthorizedBy != "owner" || policies.expected != 4 || policy.Policy != input.Policy || policy.Enabled != input.Enabled) {
				t.Fatalf("policy scope or authority: %+v", policy)
			}
		})
	}
}

func TestRetentionPolicyReadDistinguishesUnsetDeniedAndForeignState(t *testing.T) {
	for _, mode := range []string{"saved", "unset", "denied", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			service, policies, _, _ := policyServiceFixture(t)
			switch mode {
			case "unset":
				policies.saved = nil
			case "denied":
				service.Authz.(*policyServiceAuthorization).denied = true
			case "foreign":
				snapshot := *policies.saved
				snapshot.OrgID = "foreign"
				policies.saved = &snapshot
			}
			result, err := service.RetentionPolicyForBackup(t.Context(), security.Principal{OrgID: "org", UserID: "reader"}, "candidate")
			switch mode {
			case "saved":
				if err != nil || result == nil || result.ID != policies.saved.ID {
					t.Fatalf("saved policy: %+v %v", result, err)
				}
			case "unset":
				if err != nil || result != nil {
					t.Fatalf("unset policy: %+v %v", result, err)
				}
			default:
				if err == nil || result != nil {
					t.Fatalf("unauthorized policy escaped: %+v %v", result, err)
				}
			}
		})
	}
}

func TestRetentionPolicySchedulerRecoversAcceptedRequestBeforeReselecting(t *testing.T) {
	service, policies, operations, records := policyServiceFixture(t)
	unavailable := errors.New("progress database unavailable")
	policies.advanceErr = unavailable
	if err := service.reconcileRetentionPolicies(t.Context()); !errors.Is(err, unavailable) {
		t.Fatalf("progress failure hidden: %v", err)
	}
	if operations.begun != 1 || len(policies.progress) != 1 || policies.progress[0].retirement == "" {
		t.Fatalf("accepted intent not recorded: %+v", policies.progress)
	}
	accepted := operations.active
	accepted.Status = "succeeded"
	operations.active = nil
	records.points["candidate"].Status = "retired"
	replacement := *records.points["candidate"]
	replacement.ID = "new-candidate"
	replacement.Status = "succeeded"
	replacement.Token = "new-attempt"
	replacement.Object.Key = replacement.objectKey()
	records.points[replacement.ID] = &replacement
	records.entries = []RetentionEntry{{Backup: *records.points["witness"]}, {Backup: replacement}}
	policies.advanceErr = nil
	if err := service.reconcileRetentionPolicies(t.Context()); err != nil {
		t.Fatal(err)
	}
	if operations.begun != 1 || len(policies.progress) != 2 || policies.progress[1].retirement != accepted.ID {
		t.Fatalf("same due evaluation selected another backup: begun=%d progress=%+v", operations.begun, policies.progress)
	}
}

func TestRetentionPolicySchedulerReconcilesCommitAfterLostResponseOrCancellation(t *testing.T) {
	for _, mode := range []string{"response-lost", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			service, policies, operations, _ := policyServiceFixture(t)
			operations.lostResponse = true
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancelled" {
				operations.afterBegin = cancel
			}
			err := service.reconcileRetentionPolicies(ctx)
			if (err != nil) != (mode == "cancelled") || operations.begun != 1 || len(policies.progress) != 1 || policies.progress[0].retirement == "" {
				t.Fatalf("accepted commit lost during %s: err=%v begun=%d progress=%+v", mode, err, operations.begun, policies.progress)
			}
		})
	}
}

func TestRetentionPolicySchedulerRejectsUnsafeSelectionAndContinuesAfterFailure(t *testing.T) {
	for _, mode := range []string{"revoked", "source-changed", "incomplete", "unverified", "unsupported", "failed-retirement", "idle", "inventory-error"} {
		t.Run(mode, func(t *testing.T) {
			service, policies, operations, records := policyServiceFixture(t)
			switch mode {
			case "revoked":
				service.Authz.(*policyServiceAuthorization).denied = true
			case "source-changed":
				policies.due[0].SourceKey = "other-source"
			case "incomplete":
				for len(records.entries) <= retentionInventoryLimit {
					snapshot := records.entries[1]
					snapshot.Backup.ID = fmt.Sprintf("old-%d", len(records.entries))
					snapshot.Backup.Object.Key = snapshot.Backup.objectKey()
					records.entries = append(records.entries, snapshot)
				}
			case "unverified":
				records.points["witness"].RestoredAt = nil
				records.entries[0].Backup.RestoredAt = nil
			case "unsupported":
				service.Files = nil
			case "failed-retirement":
				operations.active = &Retirement{ID: "failed", OrgID: "org", ProjectID: "project", SourceKey: policies.due[0].SourceKey, Status: "failed"}
			case "idle":
				policies.due[0].Policy.KeepLast = 7
			case "inventory-error":
				records.errorInventory = errors.New("private storage detail")
			}
			err := service.reconcileRetentionPolicies(t.Context())
			expectedFailure := mode != "idle"
			if (err != nil) != expectedFailure || operations.begun != 0 || len(policies.progress) != 1 || (policies.progress[0].cause != nil) != expectedFailure {
				t.Fatalf("unsafe selection %s: %v admitted=%d progress=%+v", mode, err, operations.begun, policies.progress)
			}
			if mode == "inventory-error" && strings.Contains(err.Error(), records.errorInventory.Error()) {
				t.Fatal("private error escaped scheduler diagnostics")
			}
		})
	}
}

func TestRetentionPolicyReconciliationRunsDespiteAnotherCategoryFailure(t *testing.T) {
	service, policies, operations, records := policyServiceFixture(t)
	unavailable := errors.New("backup queue read failed")
	records.pendingErr = unavailable
	if err := service.Reconcile(t.Context()); !errors.Is(err, unavailable) || operations.begun != 1 || len(policies.progress) != 1 {
		t.Fatalf("policy reconciliation starved: %v begun=%d", err, operations.begun)
	}
}

func TestRetentionPolicyRequestKeyIsStableAndSeparatesRevisionAndDueTime(t *testing.T) {
	_, policies, _, _ := policyServiceFixture(t)
	policy := policies.due[0]
	key := retentionPolicyRequestKey(&policy)
	if !requestKey.MatchString(key) || key != "retention_1f54b149b4ee14e8c1ea1826a35fbb3f8692a3e9e84c6cdd538af8b6215a330c" || key != retentionPolicyRequestKey(&policy) {
		t.Fatalf("invalid stable request: %s", key)
	}
	snapshot := policy
	snapshot.NextAt = policy.NextAt.In(time.FixedZone("alternate", 3600))
	if retentionPolicyRequestKey(&snapshot) != key {
		t.Fatal("timezone changed persisted request identity")
	}
	for _, change := range []func(*RetentionPolicy){func(p *RetentionPolicy) { p.ID += "-other" }, func(p *RetentionPolicy) { p.Revision++ }, func(p *RetentionPolicy) { p.NextAt = p.NextAt.Add(time.Microsecond) }, func(p *RetentionPolicy) { p.OrgID = "other" }, func(p *RetentionPolicy) { p.ProjectID = "other" }} {
		changed := policy
		change(&changed)
		if retentionPolicyRequestKey(&changed) == key {
			t.Fatal("different evaluation reused request identity")
		}
	}
}

func TestRetirementMissingStorageIsRefusedBeforeAdmission(t *testing.T) {
	service, _, operations, _ := policyServiceFixture(t)
	service.Files = nil
	if _, err := service.Retire(t.Context(), security.Principal{OrgID: "org", UserID: "owner"}, "candidate", RetirementInput{RequestID: "manual-retirement", Policy: RetentionInput{OlderThanDays: 7, KeepLast: 1}}); err == nil || operations.begun != 0 {
		t.Fatalf("unconfigured retirement admitted: %v begun=%d", err, operations.begun)
	}
}
