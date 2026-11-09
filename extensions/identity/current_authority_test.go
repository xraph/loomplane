package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/warden/id"
	"github.com/xraph/warden/role"
	"github.com/xraph/warden/store"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/security"
)

type sharedWardenStore struct {
	store.Store
	closes int
}

func (s *sharedWardenStore) Close() error { s.closes++; return nil }

func TestCurrentAuthorityWiringStaysUnavailable(t *testing.T) {
	e := New(config.Config{})
	ordinary := &sharedWardenStore{Store: memory.New()}
	routed, err := e.initWarden(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if e.warden.Store() != routed {
		t.Fatal("canonical engine bypasses router")
	}
	if len(e.warden.Plugins().Plugins()) != 1 || e.warden.Plugins().Plugins()[0].Name() != "warden-cache-invalidator" {
		t.Fatal("automatic cache invalidator missing")
	}
	role := &role.Role{ID: id.NewRoleID(), TenantID: "org", Name: "reader", Slug: "reader"}
	if checkErr := routed.CreateRole(context.Background(), role); checkErr != nil {
		t.Fatal(checkErr)
	}
	got, err := ordinary.GetRole(context.Background(), "org", role.ID)
	if err != nil || got.ID != role.ID {
		t.Fatalf("ordinary store=%v err=%v", got, err)
	}
	b := currentauthority.Binding{Actor: security.Principal{UserID: "user", OrgID: "org"}, AppID: "app", Operation: "issue", InputDigest: strings.Repeat("a", 64), RecordID: "r", AttemptID: "a"}
	if checkErr := e.authority.Run(context.Background(), b, func(context.Context, *currentauthority.Attempt) error {
		t.Fatal("unqualified authority ran")
		return nil
	}); !errors.Is(checkErr, currentauthority.ErrUnavailable) {
		t.Fatalf("Run=%v", checkErr)
	}
	if checkErr := e.Stop(context.Background()); checkErr != nil {
		t.Fatal(checkErr)
	}
	if ordinary.closes != 0 {
		t.Fatal("identity closed the shared main store")
	}
	if _, checkErr := ordinary.GetRole(context.Background(), "org", role.ID); checkErr != nil {
		t.Fatal("shared store unavailable after Stop", checkErr)
	}
}
