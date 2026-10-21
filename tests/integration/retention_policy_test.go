//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/recovery"
)

func retentionPolicyRequest() *recovery.RetentionPolicy {
	return &recovery.RetentionPolicy{ID: "retention-1", OrgID: "o", ProjectID: "p", AnchorID: "b", AuthorizedBy: "u", Enabled: true, Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}}
}
func retentionGuard(context.Context) error { return nil }

func TestRetentionPolicyPersistsSourceRevisionAndHistory(t *testing.T) {
	conn, store := retirementFixture(t)
	if _, err := store.FindRetentionPolicy(t.Context(), "o", "b"); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatalf("missing policy: %v", err)
	}
	in := retentionPolicyRequest()
	in.Enabled = false
	policy, err := store.PutRetentionPolicy(t.Context(), in, 0, retentionGuard)
	if err != nil || policy.Revision != 1 || policy.Enabled || policy.CreatedBy != "u" || policy.SourceKey == "" {
		t.Fatalf("initial policy: %+v %v", policy, err)
	}
	in.ID, in.AnchorID, in.AuthorizedBy, in.Enabled = "ignored-new-id", "w", "replacement", true
	in.Policy = recovery.RetentionInput{OlderThanDays: 30, KeepLast: 7}
	current, err := store.PutRetentionPolicy(t.Context(), in, policy.Revision, retentionGuard)
	if err != nil || current.ID != policy.ID || current.Revision != 2 || current.CreatedBy != "u" || current.AuthorizedBy != "replacement" || current.SourceKey != policy.SourceKey || current.Policy != in.Policy {
		t.Fatalf("updated policy: %+v %v", current, err)
	}
	pg := pgdriver.New()
	if err = pg.Open(t.Context(), conn.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	reopened := recovery.NewPgStore(pg)
	found, err := reopened.FindRetentionPolicy(t.Context(), "o", "w")
	if err != nil || found.ID != policy.ID || found.Revision != 2 {
		t.Fatalf("reopened policy: %+v %v", found, err)
	}
	list, err := reopened.ListRetentionPolicies(t.Context(), "o", "p")
	if err != nil || len(list) != 1 || list[0].ID != policy.ID {
		t.Fatalf("scoped list: %+v %v", list, err)
	}
	for _, scope := range [][2]string{{"other-org", "p"}, {"o", "other-project"}} {
		list, err = reopened.ListRetentionPolicies(t.Context(), scope[0], scope[1])
		if err != nil || len(list) != 0 {
			t.Fatalf("foreign policy list: %+v %v", list, err)
		}
	}
	if _, err = reopened.GetRetentionPolicy(t.Context(), "foreign", policy.ID); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatalf("foreign policy read: %v", err)
	}
	var revisions int
	if err = conn.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_retention_policy_revisions WHERE org_id='o' AND policy_id=$1`, policy.ID).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("lost authorization revisions: %d %v", revisions, err)
	}
	var first, last string
	if err = conn.QueryRow(t.Context(), `SELECT min(authorized_by) FILTER (WHERE revision=1),min(authorized_by) FILTER (WHERE revision=2) FROM loomplane_retention_policy_revisions WHERE org_id='o' AND policy_id=$1`, policy.ID).Scan(&first, &last); err != nil || first != "u" || last != "replacement" {
		t.Fatalf("authorization history: %s %s %v", first, last, err)
	}
}

func TestRetentionPolicyUpdatesFenceStaleRevisionsAndPermissionLoss(t *testing.T) {
	_, store := retirementFixture(t)
	original, err := store.PutRetentionPolicy(t.Context(), retentionPolicyRequest(), 0, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []int64{0, original.Revision + 1} {
		if _, err = store.PutRetentionPolicy(t.Context(), retentionPolicyRequest(), expected, retentionGuard); !errors.Is(err, recovery.ErrConflict) {
			t.Fatalf("stale update accepted at %d: %v", expected, err)
		}
	}
	calls := 0
	denied := errors.New("owner revoked")
	input := retentionPolicyRequest()
	input.Enabled = false
	if _, err = store.PutRetentionPolicy(t.Context(), input, original.Revision, func(context.Context) error {
		calls++
		if calls > 1 {
			return denied
		}
		return nil
	}); !errors.Is(err, denied) {
		t.Fatalf("revoked authority committed: %v", err)
	}
	current, err := store.GetRetentionPolicy(t.Context(), "o", original.ID)
	if err != nil || current.Revision != original.Revision || !current.Enabled {
		t.Fatalf("failed update changed policy: %+v %v", current, err)
	}
	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, putErr := store.PutRetentionPolicy(t.Context(), input, original.Revision, retentionGuard)
			results <- putErr
		}()
	}
	wins := 0
	for range 8 {
		if err = <-results; err == nil {
			wins++
		} else if !errors.Is(err, recovery.ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent updates won %d times", wins)
	}
}

func TestRetentionPolicyAdmissionRejectsPausedAndChangedGrants(t *testing.T) {
	for _, change := range []string{"paused", "new-revision", "wrong-owner", "wrong-settings", "wrong-source", "partial-reference"} {
		t.Run(change, func(t *testing.T) {
			conn, store := retirementFixture(t)
			input := retentionPolicyRequest()
			policy, err := store.PutRetentionPolicy(t.Context(), input, 0, retentionGuard)
			if err != nil {
				t.Fatal(err)
			}
			request := retirementRequest()
			request.RetentionPolicyID, request.RetentionRevision = policy.ID, policy.Revision
			request.RetentionExpectedAt = policy.NextAt
			switch change {
			case "paused":
				input.Enabled = false
				_, err = store.PutRetentionPolicy(t.Context(), input, policy.Revision, retentionGuard)
			case "new-revision":
				_, err = store.PutRetentionPolicy(t.Context(), input, policy.Revision, retentionGuard)
			case "wrong-owner":
				request.CreatedBy = "other-owner"
			case "wrong-settings":
				request.Policy.KeepLast = 2
			case "wrong-source":
				_, err = conn.Exec(t.Context(), `INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,object,token,created_by,created_at,completed_at,restored_at)
 SELECT id||'2',org_id,project_id,environment_id,connection_id,name,status,request_id||'2',jsonb_set(source,'{external_id}','"other-instance"'),jsonb_set(object,'{key}',to_jsonb('o/p/e/'||id||'2/attempt.dump')),token,created_by,created_at,completed_at,restored_at FROM loomplane_service_backups`)
				request.BackupID = "b2"
			case "partial-reference":
				request.RetentionRevision = 0
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.BeginRetirement(t.Context(), request, retentionGuard); !errors.Is(err, recovery.ErrConflict) && !errors.Is(err, recovery.ErrInvalid) {
				t.Fatalf("invalid grant admitted: %v", err)
			}
			point, err := store.Get(t.Context(), "o", request.BackupID)
			if err != nil || point.Status != "succeeded" {
				t.Fatalf("refused grant changed point: %+v %v", point, err)
			}
		})
	}
}

func TestRetentionPolicyAcceptedIntentSurvivesPauseWithoutGrantingAnother(t *testing.T) {
	_, store := retirementFixture(t)
	input := retentionPolicyRequest()
	policy, err := store.PutRetentionPolicy(t.Context(), input, 0, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	request := retirementRequest()
	request.RetentionPolicyID, request.RetentionRevision = policy.ID, policy.Revision
	request.RetentionExpectedAt = policy.NextAt
	accepted, err := store.BeginRetirement(t.Context(), request, retentionGuard)
	if err != nil || accepted.RetentionPolicyID != policy.ID || accepted.RetentionRevision != policy.Revision {
		t.Fatalf("policy provenance missing: %+v %v", accepted, err)
	}
	if err = store.AdvanceRetentionPolicy(t.Context(), policy, accepted.ID, nil); err != nil {
		t.Fatal(err)
	}
	progressed, err := store.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || progressed.LastRetirementID != accepted.ID {
		t.Fatalf("policy lost accepted intent: %+v %v", progressed, err)
	}
	input.Enabled = false
	if _, err = store.PutRetentionPolicy(t.Context(), input, policy.Revision, retentionGuard); err != nil {
		t.Fatal(err)
	}
	replay, err := store.BeginRetirement(t.Context(), request, retentionGuard)
	if err != nil || replay.ID != accepted.ID {
		t.Fatalf("pause lost accepted operation: %+v %v", replay, err)
	}
	forged := *request
	forged.RetentionPolicyID = "other-policy"
	if _, err = store.BeginRetirement(t.Context(), &forged, retentionGuard); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("replay changed policy provenance: %v", err)
	}
	stored, err := store.GetRetirement(t.Context(), "o", accepted.ID)
	if err != nil || stored.RetentionRevision != 1 || stored.RetentionPolicyID != policy.ID {
		t.Fatalf("intent provenance was overwritten: %+v %v", stored, err)
	}
}

func TestRetentionPolicyPauseAndAdmissionSerialize(t *testing.T) {
	conn, store := retirementFixture(t)
	input := retentionPolicyRequest()
	policy, err := store.PutRetentionPolicy(t.Context(), input, 0, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	input.Enabled = false
	paused := make(chan error, 1)
	go func() {
		_, putErr := store.PutRetentionPolicy(ctx, input, policy.Revision, func(ctx context.Context) error {
			once.Do(func() { close(held) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		paused <- putErr
	}()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	request := retirementRequest()
	request.RetentionPolicyID, request.RetentionRevision = policy.ID, policy.Revision
	request.RetentionExpectedAt = policy.NextAt
	admission := make(chan error, 1)
	go func() { _, beginErr := store.BeginRetirement(ctx, request, retentionGuard); admission <- beginErr }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked int
		if err = conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='loomplane-retirement-admission' AND wait_event_type='Lock'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("retirement did not wait for policy update's source lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	if err = <-paused; err != nil {
		t.Fatal(err)
	}
	if err = <-admission; !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("admission ignored completed pause: %v", err)
	}
}

func TestRetentionPolicyDueStateRejectsStaleSchedulerWrites(t *testing.T) {
	conn, store := retirementFixture(t)
	policy, err := store.PutRetentionPolicy(t.Context(), retentionPolicyRequest(), 0, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.DueRetentionPolicies(t.Context())
	if err != nil || len(due) != 1 || due[0].ID != policy.ID {
		t.Fatalf("due policies: %+v %v", due, err)
	}
	if err = store.AdvanceRetentionPolicy(t.Context(), &due[0], "", errors.New("secret transport detail")); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || current.LastError == "" || strings.Contains(current.LastError, "secret transport detail") || current.NextAt.Before(time.Now().Add(55*time.Minute)) {
		t.Fatalf("backoff state: %+v %v", current, err)
	}
	if err = store.AdvanceRetentionPolicy(t.Context(), &due[0], "", nil); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || after.LastError != current.LastError || !after.NextAt.Equal(current.NextAt) {
		t.Fatalf("stale due state overwrote result: %+v %v", after, err)
	}
	input := retentionPolicyRequest()
	input.Enabled = false
	disabled, err := store.PutRetentionPolicy(t.Context(), input, policy.Revision, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AdvanceRetentionPolicy(t.Context(), current, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_retention_policies SET next_at=now()-interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	due, err = store.DueRetentionPolicies(t.Context())
	if err != nil || len(due) != 0 {
		t.Fatalf("disabled policy due at revision %d: %+v %v", disabled.Revision, due, err)
	}
}

func TestRetentionPolicyRejectsInvalidInputBeforeMutation(t *testing.T) {
	_, store := retirementFixture(t)
	for n, change := range []func(*recovery.RetentionPolicy){func(p *recovery.RetentionPolicy) { p.OrgID = "" }, func(p *recovery.RetentionPolicy) { p.AuthorizedBy = "" }, func(p *recovery.RetentionPolicy) { p.ProjectID = "foreign" }, func(p *recovery.RetentionPolicy) { p.Policy.KeepLast = 0 }} {
		input := retentionPolicyRequest()
		change(input)
		if _, err := store.PutRetentionPolicy(t.Context(), input, 0, retentionGuard); err == nil {
			t.Fatalf("invalid input %d accepted: %+v", n, input)
		}
	}
	for _, revision := range []int64{-1, 2147483647} {
		if _, err := store.PutRetentionPolicy(t.Context(), retentionPolicyRequest(), revision, retentionGuard); err == nil {
			t.Fatalf("invalid revision accepted: %d", revision)
		}
	}
	policies, err := store.ListRetentionPolicies(t.Context(), "o", "p")
	if err != nil || len(policies) != 0 {
		t.Fatalf("invalid inputs persisted: %+v %v", policies, err)
	}
}

func TestRetentionPolicyProgressCannotLinkAnUnrelatedRetirement(t *testing.T) {
	_, store := retirementFixture(t)
	policy, err := store.PutRetentionPolicy(t.Context(), retentionPolicyRequest(), 0, retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := store.BeginRetirement(t.Context(), retirementRequest(), retentionGuard)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AdvanceRetentionPolicy(t.Context(), policy, manual.ID, nil); !errors.Is(err, recovery.ErrConflict) {
		t.Fatalf("unrelated retirement attached to policy: %v", err)
	}
	current, err := store.GetRetentionPolicy(t.Context(), "o", policy.ID)
	if err != nil || current.LastRetirementID != "" || !current.NextAt.Equal(policy.NextAt) {
		t.Fatalf("invalid progress mutated policy: %+v %v", current, err)
	}
}
