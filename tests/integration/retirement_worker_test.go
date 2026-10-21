//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/trove"
	"github.com/xraph/trove/driver"
	"github.com/xraph/trove/drivers/localdriver"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

type retirementOwner struct {
	revoked bool
	user    string
}

func (a *retirementOwner) Require(_ context.Context, p security.Principal, permission security.Permission, project string) error {
	user := a.user
	if user == "" {
		user = "u"
	}
	if a.revoked || p.OrgID != "o" || p.UserID != user || project != "p" || permission != security.BackupRetire {
		return security.ErrForbidden
	}
	return nil
}

func TestRetirementWorkerPersistsDeletionAndRecoversDispatchGap(t *testing.T) {
	for _, mode := range []string{"valid", "missing_witness", "revoked_owner", "retry_owner"} {
		t.Run(mode, func(t *testing.T) {
			conn, records := retirementFixture(t)
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
			objects := map[string]backupfiles.Object{}
			for _, id := range []string{"b", "w"} {
				ref, putErr := storage.Put(t.Context(), "o/p/e/"+id+"/attempt.dump", strings.NewReader(id+" database bytes"))
				if putErr != nil {
					t.Fatal(putErr)
				}
				raw, marshalErr := json.Marshal(ref)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if _, err = conn.Exec(t.Context(), `UPDATE loomplane_service_backups SET object=$1 WHERE id=$2`, raw, id); err != nil {
					t.Fatal(err)
				}
				objects[id] = ref
			}
			auth := &retirementOwner{}
			queueFailure := errors.New("dispatch unavailable")
			service := &recovery.Service{Store: records, Retirements: records, Authz: auth, Files: storage, EnqueueRetirement: func(context.Context, string, string) error { return queueFailure }}
			input := recovery.RetirementInput{RequestID: "retirement-request", Policy: recovery.RetentionInput{OlderThanDays: 7, KeepLast: 1}}
			principal := security.Principal{OrgID: "o", UserID: "u"}
			r, err := service.Retire(t.Context(), principal, "b", input)
			if !errors.Is(err, queueFailure) || r == nil {
				t.Fatalf("dispatch failure lost intent: %+v %v", r, err)
			}
			again, err := service.Retire(t.Context(), principal, "b", input)
			if !errors.Is(err, queueFailure) || again == nil || again.ID != r.ID {
				t.Fatalf("idempotency: %+v %v", again, err)
			}
			pending, err := records.PendingRetirements(t.Context())
			if err != nil || len(pending) != 1 || pending[0].ID != r.ID {
				t.Fatalf("dispatch reconciliation inventory: %+v %v", pending, err)
			}
			switch mode {
			case "missing_witness":
				if err = files.Delete(t.Context(), "backups", objects["w"].Key); err != nil {
					t.Fatal(err)
				}
			case "revoked_owner":
				auth.revoked = true
			}
			if mode == "retry_owner" {
				auth.revoked = true
				if err = service.RunRetirement(t.Context(), "o", r.ID); !errors.Is(err, security.ErrForbidden) {
					t.Fatalf("revoked original owner ran retirement: %v", err)
				}
				auth.revoked, auth.user = false, "replacement"
				retried, retryErr := service.RetryRetirement(t.Context(), security.Principal{OrgID: "o", UserID: "replacement"}, r.ID, recovery.RetirementRetryInput{RequestID: "replacement-retry-key"})
				if !errors.Is(retryErr, queueFailure) || retried == nil || retried.CreatedBy != "u" || retried.ExecutionBy != "replacement" {
					t.Fatalf("replacement owner retry: %+v %v", retried, retryErr)
				}
			}
			successful := mode == "valid" || mode == "retry_owner"
			if err = service.RunRetirement(t.Context(), pending[0].OrgID, pending[0].ID); (err == nil) != successful {
				t.Fatalf("worker mode=%s: %v", mode, err)
			}
			stored, err := records.GetRetirement(t.Context(), "o", r.ID)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := records.Get(t.Context(), "o", "b")
			if err != nil {
				t.Fatal(err)
			}
			_, headErr := files.Head(t.Context(), "backups", objects["b"].Key)
			if successful {
				if stored.Status != "succeeded" || candidate.Status != "retired" || stored.CompletedAt == nil || !errors.Is(headErr, driver.ErrNotFound) {
					t.Fatalf("retirement publication: %+v %+v %v", stored, candidate, headErr)
				}
				if _, err = files.Head(t.Context(), "backups", objects["w"].Key); err != nil {
					t.Fatalf("witness lost: %v", err)
				}
				witnessRestore := restoreAdmission("witness-restore")
				witnessRestore.BackupID = "w"
				if _, err = records.CreateRestore(t.Context(), witnessRestore); err != nil {
					t.Fatalf("retained witness no longer restorable: %v", err)
				}
			} else if stored.Status != "failed" || stored.Error == "" || candidate.Status != "retiring" || headErr != nil {
				t.Fatalf("failure lost bytes or restore barrier: %+v %+v %v", stored, candidate, headErr)
			}
			if _, err = records.CreateRestore(t.Context(), restoreAdmission("blocked-retired-restore")); !errors.Is(err, recovery.ErrConflict) {
				t.Fatalf("retired candidate admitted a restore: %v", err)
			}
			if _, err = records.GetRetirement(t.Context(), "other-org", r.ID); !errors.Is(err, recovery.ErrNotFound) {
				t.Fatalf("cross-organization retirement exposed: %v", err)
			}
		})
	}
}
