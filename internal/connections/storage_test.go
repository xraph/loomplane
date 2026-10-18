package connections

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/security"
)

type inventoryConnections struct{ Store }

func (inventoryConnections) GetConnection(_ context.Context, org, key string) (*Connection, error) {
	if org != "org" || key != "connection" {
		return nil, ErrNotFound
	}
	return &Connection{ID: key, OrgID: org, Cloud: Server, Status: StatusReady}, nil
}

type inventorySecrets struct {
	Secrets
	reads int
}

func (s *inventorySecrets) Reveal(context.Context, string, string) ([]byte, error) {
	s.reads++
	return []byte("private"), nil
}

type inventoryAuthorization struct {
	denied      security.Permission
	unavailable bool
}

func (a inventoryAuthorization) Require(_ context.Context, _ security.Principal, permission security.Permission, resource string) error {
	if a.unavailable && permission == security.ProjectRead {
		return errors.New("authorization unavailable")
	}
	if permission == a.denied || permission == security.ProjectRead && resource == "hidden" {
		return security.ErrForbidden
	}
	return nil
}

func TestStorageInventoryAuthorizesBeforeReadingCredentialsAndFiltersProjects(t *testing.T) {
	for _, denied := range []security.Permission{security.InfrastructureRead, security.CloudAccountRead, {}} {
		secrets := &inventorySecrets{}
		service := &Service{store: inventoryConnections{}, secrets: secrets, authz: inventoryAuthorization{denied: denied}}
		called := false
		reader := func(context.Context, *Connection, []byte) ([]owned.StoredVolume, error) {
			called = true
			return []owned.StoredVolume{{ID: "visible", ConnectionID: "connection", ProjectID: "project"}, {ID: "hidden", ConnectionID: "connection", ProjectID: "hidden"}}, nil
		}
		volumes, err := service.storageVolumes(t.Context(), security.Principal{OrgID: "org", UserID: "viewer", ReadOnly: true}, "connection", reader)
		if denied.Resource != "" {
			if !errors.Is(err, security.ErrForbidden) || secrets.reads != 0 || called {
				t.Fatalf("unauthorized inspection: volumes=%v err=%v reads=%d called=%v", volumes, err, secrets.reads, called)
			}
		} else if err != nil || len(volumes) != 1 || volumes[0].ID != "visible" {
			t.Fatalf("project scope: %+v %v", volumes, err)
		}
	}
}

func TestStorageInventoryDoesNotTurnScopeErrorsIntoAnEmptyResult(t *testing.T) {
	service := &Service{store: inventoryConnections{}, secrets: &inventorySecrets{}, authz: inventoryAuthorization{unavailable: true}}
	reader := func(context.Context, *Connection, []byte) ([]owned.StoredVolume, error) {
		return []owned.StoredVolume{{ProjectID: "project", ConnectionID: "connection"}}, nil
	}
	if _, err := service.storageVolumes(t.Context(), security.Principal{OrgID: "org"}, "connection", reader); err == nil {
		t.Fatal("authorization outage was hidden")
	}
	if _, err := service.storageVolumes(t.Context(), security.Principal{OrgID: "other-org"}, "connection", reader); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign connection was visible: %v", err)
	}
}
