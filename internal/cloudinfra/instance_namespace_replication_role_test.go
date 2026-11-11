package cloudinfra

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/currentauthority"
)

func namespaceReplaceMainSession(t *testing.T, f *namespacePGFixture, role string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("session_replication_role", role)
	u.RawQuery = q.Encode()
	if openErr := f.db.Open(ctx, u.String(), driver.WithPoolSize(1)); openErr != nil {
		t.Fatal(openErr)
	}
}

func TestNamespacePostgresReceiptRequiresOriginSession(t *testing.T) {
	f := newNamespacePGFixture(t)
	first, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	o := namespaceCloneOperation(t, first.Operation)
	at := time.Now().UTC()
	o.Enrollment = &ClusterEnrollment{AttachmentID: "attachment", RuntimeConnectionID: "runtime-connection", TargetID: "runtime-target", ResourceRevision: 1, State: kubernetes.EnrollmentState{Attempted: map[string]bool{"Namespace": true}, Objects: map[string]string{}}}
	o.Steps[1].State = "attempted"
	o.Steps[1].AttemptedAt = &at
	o.Revision++
	if err = f.transactions.Checkpoint(t.Context(), first.Record, &o, 1); err != nil {
		t.Fatal(err)
	}
	observed := namespaceCloneOperation(t, o)
	observed.Enrollment.State.NamespaceUID = "namespace-uid"
	// Restore the installed main handle before fixture cleanup, even on failure.
	t.Cleanup(func() { namespaceReplaceMainSession(t, f, "origin") })
	for _, mode := range []string{"reused-session", "replacement-session"} {
		t.Run(mode, func(t *testing.T) {
			before, caseErr := f.transactions.store.readNamespaceOutcomes(t.Context(), o.OrgID, o.ID)
			if caseErr != nil {
				t.Fatal(caseErr)
			}
			if mode == "reused-session" {
				if _, caseErr = f.db.Exec(t.Context(), `SET session_replication_role=replica`); caseErr != nil {
					t.Fatal(caseErr)
				}
			} else {
				var priorPID int
				if caseErr = f.db.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&priorPID); caseErr != nil {
					t.Fatal(caseErr)
				}
				namespaceReplaceMainSession(t, f, "replica")
				var nextPID int
				if caseErr = f.db.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&nextPID); caseErr != nil || nextPID == priorPID {
					t.Fatalf("main session was not replaced: %v", caseErr)
				}
				observed.Status = "needs_attention"
			}
			if caseErr = f.transactions.preserveOutcome(t.Context(), &observed); !errors.Is(caseErr, currentauthority.ErrUnavailable) {
				t.Errorf("replica-mode receipt writer was not refused: %v", caseErr)
			}
			var actual string
			if caseErr = f.db.QueryRow(t.Context(), `SHOW session_replication_role`).Scan(&actual); caseErr != nil || actual != "replica" {
				t.Fatalf("writer silently changed session mode: %q %v", actual, caseErr)
			}
			after, caseErr := f.transactions.store.readNamespaceOutcomes(t.Context(), o.OrgID, o.ID)
			if caseErr != nil || !namespaceEqual(before, after) {
				t.Errorf("replica-mode call changed receipt evidence: %v", caseErr)
			}
			namespaceReplaceMainSession(t, f, "origin")
			if caseErr = f.transactions.preserveOutcome(t.Context(), &observed); caseErr != nil {
				t.Fatalf("origin-mode recovery failed: %v", caseErr)
			}
			saved, caseErr := f.transactions.store.getNamespaceAttemptOutcome(t.Context(), o.OrgID, o.ID, o.NamespaceAttemptID)
			if caseErr != nil || saved.State.NamespaceUID != "namespace-uid" {
				t.Fatalf("origin-mode recovery missing: %v", caseErr)
			}
			if mode == "replacement-session" && saved.Reason != "operator_recovery_required" {
				t.Fatal("origin replacement lost new outcome")
			}
		})
	}
}
