package recovery

import (
	"errors"
	"testing"

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
