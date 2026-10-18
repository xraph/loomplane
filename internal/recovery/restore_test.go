package recovery

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestRestoreRequiresManagementBeforeQueueing(t *testing.T) {
	store := &lookupStore{record: &Backup{ID: "backup", ProjectID: "project", Status: "succeeded"}}
	service := Service{Store: store, Authz: denied{}}
	_, err := service.Restore(t.Context(), security.Principal{OrgID: "requesting-org"}, "backup", RestoreInput{RequestID: "restore-request-1"})
	if !errors.Is(err, security.ErrForbidden) || store.org != "requesting-org" {
		t.Fatalf("restore scope/permission: %s %v", store.org, err)
	}
}

type cleanupStore struct {
	RestoreStore
	record   *Restore
	finished error
}

func (s *cleanupStore) GetRestore(context.Context, string, string) (*Restore, error) {
	return s.record, nil
}
func (s *cleanupStore) ClaimCleanup(context.Context, string, string) (*Restore, error) {
	return s.record, nil
}
func (s *cleanupStore) FinishCleanup(_ context.Context, _ *Restore, cause error) error {
	s.finished = cause
	return nil
}

type allowed struct{}

func (allowed) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type cleanupBackend struct {
	RestoreBackend
	caller security.Principal
}

func (b *cleanupBackend) DeleteRecovery(_ context.Context, p security.Principal, _ *planning.Binding, _ string) error {
	b.caller = p
	if p.UserID != "current-admin" {
		return security.ErrForbidden
	}
	return nil
}
func TestManualCleanupUsesAuthorizedAdministrator(t *testing.T) {
	r := &Restore{ID: "restore", OrgID: "org", ProjectID: "project", BackupID: "backup", CreatedBy: "removed-member", Status: "cleanup_failed"}
	store := &cleanupStore{record: r}
	backend := &cleanupBackend{}
	service := Service{Authz: allowed{}, Restores: store, Store: &lookupStore{record: &Backup{ID: "backup"}}, RestoreBackend: backend}
	p := security.Principal{OrgID: "org", UserID: "current-admin"}
	if err := service.DeleteRestore(t.Context(), p, r.ID); err != nil {
		t.Fatalf("administrator cannot finish cleanup: %v", err)
	}
	if backend.caller != p || store.finished != nil {
		t.Fatalf("cleanup lost caller: %+v %v", backend.caller, store.finished)
	}
}
