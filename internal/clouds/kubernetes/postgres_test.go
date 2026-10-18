package kubernetes

import (
	"context"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
)

func TestPostgresSourceRestoreDoesNotResolveCredentials(t *testing.T) {
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("resolved a live source for restore")
		return nil, nil
	}}
	native, ok := any(r).(interface {
		RestorePostgres(context.Context, id.ID, map[string]string, *backupfiles.Store, backupfiles.Object) error
	})
	if !ok {
		t.Fatal("Kubernetes native PostgreSQL restore adapter is missing")
	}
	if err := native.RestorePostgres(t.Context(), id.New(id.PrefixInstance), map[string]string{nativepg.LabelDatabase: "database", owned.LabelStateful: "true", owned.LabelPublic: "false"}, nil, backupfiles.Object{}); err == nil {
		t.Fatal("source restore accepted")
	}
}

func TestPostgresInvalidPasswordDoesNotResolveCredentials(t *testing.T) {
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("resolved target for an invalid password")
		return nil, nil
	}}
	native, ok := any(r).(interface {
		ConfigurePostgres(context.Context, id.ID, map[string]string, string) error
	})
	if !ok {
		t.Fatal("Kubernetes native PostgreSQL setup adapter is missing")
	}
	if err := native.ConfigurePostgres(t.Context(), id.New(id.PrefixInstance), nil, "short"); err == nil {
		t.Fatal("invalid password accepted")
	}
}

func TestPostgresHasScopedRecoveryCleanup(t *testing.T) {
	var candidate any = &Runtime{}
	if _, ok := candidate.(nativepg.Backend); !ok {
		t.Fatal("Kubernetes PostgreSQL cannot clean up recovery storage")
	}
}
