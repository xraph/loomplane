package recovery

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

type denied struct{}

func (denied) Require(context.Context, security.Principal, security.Permission, string) error {
	return security.ErrForbidden
}
func TestDeniedBackupCannotReadOrEnqueueSource(t *testing.T) {
	service := Service{Authz: denied{}}
	// All other dependencies are nil: permission denial must precede source reads.
	if _, err := service.Create(t.Context(), security.Principal{OrgID: "other"}, "project", BackupCreateInput{Environment: "production", Resource: "db", RequestID: "retry-123"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("authorization: %v", err)
	}
	if _, err := service.List(t.Context(), security.Principal{}, "project"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read authorization: %v", err)
	}
}

type lookupStore struct {
	Store
	org    string
	record *Backup
}

func (s *lookupStore) Get(_ context.Context, org, _ string) (*Backup, error) {
	s.org = org
	return s.record, nil
}
func TestBackupRetryRequiresManagementPermission(t *testing.T) {
	store := &lookupStore{record: &Backup{ID: "backup", ProjectID: "project"}}
	service := Service{Store: store, Authz: denied{}}
	if _, err := service.Retry(t.Context(), security.Principal{OrgID: "requesting-org"}, "backup"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("retry: %v", err)
	}
	if store.org != "requesting-org" {
		t.Fatal("lookup lost tenant scope")
	}
}
