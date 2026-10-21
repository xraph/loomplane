package recovery

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

type nativeLookup struct {
	NativeStore
	source *NativeSource
	org    string
}

func (s *nativeLookup) GetNativeSource(_ context.Context, org, _ string) (*NativeSource, error) {
	s.org = org
	return s.source, nil
}

func TestNativeInventoryRequiresBackupReadBeforeProviderAccess(t *testing.T) {
	p := security.Principal{OrgID: "org", UserID: "reader"}
	s := NativeService{Authz: denied{}}
	if _, err := s.List(t.Context(), p, "project", NativePageInput{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("list permission: %v", err)
	}
	store := &nativeLookup{source: &NativeSource{ID: "source", OrgID: "org", ProjectID: "project"}}
	s.Store = store
	if _, err := s.Inventory(t.Context(), p, "source", NativePageInput{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read permission: %v", err)
	}
	if _, err := s.Refresh(t.Context(), p, "source"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("refresh permission: %v", err)
	}
	if store.org != "org" {
		t.Fatal("native history lookup lost its organization")
	}
}

func TestNativeInventoryRejectsForeignSourcesEvenIfStoreIsWrong(t *testing.T) {
	s := NativeService{Authz: allowed{}, Store: &nativeLookup{source: &NativeSource{ID: "source", OrgID: "foreign", ProjectID: "project"}}}
	if _, err := s.Refresh(t.Context(), security.Principal{OrgID: "org"}, "source"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign source reached provider: %v", err)
	}
}
