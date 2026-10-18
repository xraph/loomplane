package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

type recordedTarget struct {
	calls     []string
	principal security.Principal
	binding   *planning.Binding
}

func (r *recordedTarget) BackupDatabase(_ context.Context, p security.Principal, b *planning.Binding, _ *backupfiles.Store, key string) (backupfiles.Object, error) {
	r.calls = append(r.calls, "backup:"+key)
	r.principal, r.binding = p, b
	return backupfiles.Object{Key: key}, nil
}
func (r *recordedTarget) RestoreDatabase(_ context.Context, p security.Principal, b *planning.Binding, _ *backupfiles.Store, _ backupfiles.Object, operation string) (string, error) {
	r.calls = append(r.calls, "restore:"+operation)
	r.principal, r.binding = p, b
	return "isolated-copy", nil
}
func (r *recordedTarget) DeleteRecovery(_ context.Context, p security.Principal, b *planning.Binding, operation string) error {
	r.calls = append(r.calls, "cleanup:"+operation)
	r.principal, r.binding = p, b
	return nil
}

func routedBinding(t *testing.T, provider string) *planning.Binding {
	t.Helper()
	recipe, err := recipes.Postgres("17", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(planning.ResourceConfig{Provider: provider, Target: "selected", Kind: "postgres", Version: "17", Catalog: recipe})
	if err != nil {
		t.Fatal(err)
	}
	return &planning.Binding{OrgID: "org", Subject: planning.ResourceSubject, Provider: provider, ConnectionID: "selected", Config: cfg}
}
func TestRecoveryRoutesEveryOperationThroughRecordedTarget(t *testing.T) {
	for _, provider := range []string{"server", "kubernetes"} {
		t.Run(provider, func(t *testing.T) {
			server, cluster := &recordedTarget{}, &recordedTarget{}
			targets := Targets{"server": server, "kubernetes": cluster}
			b := routedBinding(t, provider)
			p := security.Principal{OrgID: "org", UserID: "current-admin"}
			object, err := targets.BackupDatabase(t.Context(), p, b, nil, "key")
			if err != nil || object.Key != "key" {
				t.Fatalf("backup routing: %+v %v", object, err)
			}
			copyID, err := targets.RestoreDatabase(t.Context(), p, b, nil, object, "restore")
			if err != nil || copyID != "isolated-copy" {
				t.Fatalf("restore routing: %s %v", copyID, err)
			}
			if err = targets.DeleteRecovery(t.Context(), p, b, "restore"); err != nil {
				t.Fatal(err)
			}
			chosen, other := server, cluster
			if provider == "kubernetes" {
				chosen, other = cluster, server
			}
			if len(chosen.calls) != 3 || len(other.calls) != 0 || chosen.principal != p || chosen.binding != b {
				t.Fatalf("recovery crossed its target or caller scope: %+v %+v", chosen, other)
			}
		})
	}
}
func TestRecoveryRejectsInconsistentBindingBeforeTargetAccess(t *testing.T) {
	for _, change := range []string{"tenant", "subject", "provider", "connection", "missing-target", "unqualified-provider", "invalid-json", "missing-recipe", "missing-kind"} {
		t.Run(change, func(t *testing.T) {
			target := &recordedTarget{}
			targets := Targets{"server": target, "kubernetes": target}
			b := routedBinding(t, "kubernetes")
			p := security.Principal{OrgID: "org"}
			switch change {
			case "tenant":
				p.OrgID = "other"
			case "subject":
				b.Subject = planning.ServiceSubject
			case "provider":
				b.Provider = "server"
			case "connection":
				b.ConnectionID = "another"
			case "missing-target":
				delete(targets, "kubernetes")
			case "unqualified-provider":
				b = routedBinding(t, "managed")
			case "invalid-json":
				b.Config = []byte("invalid")
			case "missing-kind":
				b.Config = []byte(`{"provider":"kubernetes","target":"selected","catalog":{}}`)
			case "missing-recipe":
				b.Config = []byte(`{"kind":"postgres","provider":"kubernetes","target":"selected"}`)
			}
			_, backupErr := targets.BackupDatabase(t.Context(), p, b, nil, "key")
			_, restoreErr := targets.RestoreDatabase(t.Context(), p, b, nil, backupfiles.Object{}, "restore")
			cleanupErr := targets.DeleteRecovery(t.Context(), p, b, "restore")
			if backupErr == nil || restoreErr == nil || cleanupErr == nil || len(target.calls) != 0 {
				t.Fatalf("inconsistent scope reached target: %v %v %v %+v", backupErr, restoreErr, cleanupErr, target.calls)
			}
		})
	}
}
func TestRecoveryPreservesSelectedBackendFailure(t *testing.T) {
	cause := errors.New("selected target unavailable")
	targets := Targets{"kubernetes": failingTarget{cause: cause}}
	if _, err := targets.BackupDatabase(t.Context(), security.Principal{OrgID: "org"}, routedBinding(t, "kubernetes"), nil, "key"); !errors.Is(err, cause) {
		t.Fatalf("lost backend error: %v", err)
	}
}

type failingTarget struct {
	TargetBackend
	cause error
}

func (f failingTarget) BackupDatabase(context.Context, security.Principal, *planning.Binding, *backupfiles.Store, string) (backupfiles.Object, error) {
	return backupfiles.Object{}, f.cause
}

func TestRecoveryDelegatesEngineQualificationToSelectedBackend(t *testing.T) {
	b := routedBinding(t, "server")
	var cfg planning.ResourceConfig
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Kind = "unqualified-engine"
	var err error
	b.Config, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("the selected adapter has no qualified recipe for this engine")
	targets := Targets{"server": failingTarget{cause: cause}}
	if _, err = targets.BackupDatabase(t.Context(), security.Principal{OrgID: "org"}, b, nil, "key"); !errors.Is(err, cause) {
		t.Fatalf("target routing bypassed the engine's validation: %v", err)
	}
}
