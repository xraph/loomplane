package kubernetes

import (
	"context"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
)

func TestDatabaseWorkflowPortRefusesUnqualifiedEngineBeforeAccess(t *testing.T) {
	r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		t.Fatal("opened an unqualified engine target")
		return nil, nil
	}}
	var native owned.DatabaseBackend = r
	labels := map[string]string{owned.LabelStateful: "true", owned.LabelPublic: "false", nativepg.LabelRecovery: "restore-fixture", "loomplane.mysql.database": "database", "loomplane.mysql.engine": "mysql"}
	iid := id.New(id.PrefixInstance)
	if err := native.ConfigureDatabase(t.Context(), iid, labels, "fixture-application-password-for-engine"); err == nil {
		t.Fatal("configured an unqualified engine")
	}
	if _, err := native.BackupDatabase(t.Context(), iid, labels, nil, "key"); err == nil {
		t.Fatal("exported an unqualified engine")
	}
	if err := native.RestoreDatabase(t.Context(), iid, labels, nil, backupfiles.Object{}); err == nil {
		t.Fatal("restored an unqualified engine")
	}
	if err := native.DatabaseProbe(t.Context(), iid, labels); err == nil {
		t.Fatal("probed an unqualified engine")
	}
}
