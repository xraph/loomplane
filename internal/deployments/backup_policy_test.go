package deployments

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type backupPermission struct {
	denied bool
	calls  int
}

func (a *backupPermission) Require(_ context.Context, _ security.Principal, p security.Permission, _ string) error {
	if p != security.BackupManage {
		return errors.New("unexpected permission")
	}
	a.calls++
	if a.denied {
		return security.ErrForbidden
	}
	return nil
}

type backupInstaller struct {
	plans     *fakePlans
	err       error
	calls     int
	principal security.Principal
	input     recovery.PolicyInput
}

func (i *backupInstaller) EnsurePolicy(_ context.Context, p security.Principal, project string, in recovery.PolicyInput) (*recovery.Policy, error) {
	i.calls++
	i.principal = p
	i.input = in
	if project != "proj_1" || i.plans.bindings["resource:data--postgres"] == nil {
		return nil, errors.New("policy installed before source binding")
	}
	return &recovery.Policy{ID: "policy"}, i.err
}

func backupRunner(t *testing.T) (*Runner, *memStore, *fakePlans, *recorder, *backupInstaller, *backupPermission) {
	t.Helper()
	recipe, err := recipes.Default().LookupEngine("timescaledb", "16", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	plan := webAndAPI()
	plan.Catalog = true
	plan.Sources = nil
	plan.CreatedBy = "planner"
	plan.DecidedBy = "approver"
	plan.Connections = map[string]string{"server": "conn_s"}
	plan.Actions = []planning.Action{{ID: "resource:data--postgres", Name: "data--postgres", Subject: planning.ResourceSubject, Op: planning.Create, Provider: "server", ConnectionID: "conn_s", Engine: provider.ResourceEngine, Step: 1, BackupEveryHours: 24, Resource: &planning.ResourceConfig{Provider: "server", Kind: "timescaledb", Version: "16", Catalog: recipe}}}
	exec := &recorder{}
	r, store, plans := newRunner(t, plan, exec, nil)
	installer := &backupInstaller{plans: plans}
	auth := &backupPermission{}
	r.credentials = dialCredentials{"conn_s": {ID: "conn_s", OrgID: "org_1", Cloud: connections.Server, Status: connections.StatusReady, Details: map[string]string{}}}
	r.backupPolicies = installer
	r.authz = auth
	r.executors["server"] = exec
	return r, store, plans, exec, installer, auth
}

func TestDefaultBackupInstallationFollowsBindingAndKeepsApprover(t *testing.T) {
	r, store, plans, _, installer, _ := backupRunner(t)
	id, err := runAll(t, r, plans.plan)
	if err != nil {
		t.Fatal(err)
	}
	if store.d[id].Status != StatusSucceeded || installer.calls != 1 || installer.principal.UserID != "approver" || installer.principal.OrgID != "org_1" || installer.input.Resource != "data--postgres" || installer.input.Environment != "production" || installer.input.EveryHours != 24 || !installer.input.Enabled {
		t.Fatalf("installation: %+v, deployment %+v", installer, store.d[id])
	}
}

func TestDefaultBackupPermissionLossPreventsProvisioning(t *testing.T) {
	r, _, plans, exec, installer, auth := backupRunner(t)
	id, _, err := r.Start(t.Context(), plans.plan.OrgID, plans.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	auth.denied = true
	err = r.Run(t.Context(), plans.plan.OrgID, id, "resource:data--postgres", nil, func([]byte) error { return nil })
	if !errors.Is(err, security.ErrForbidden) || len(exec.jobs) != 0 || installer.calls != 0 {
		t.Fatalf("permission loss allowed effects: jobs %d, policy %d, err %v", len(exec.jobs), installer.calls, err)
	}
}

func TestDefaultBackupFailureDoesNotReportSuccess(t *testing.T) {
	r, store, plans, _, installer, _ := backupRunner(t)
	installer.err = errors.New("backup policy unavailable")
	id, _, err := r.Start(t.Context(), plans.plan.OrgID, plans.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(t.Context(), plans.plan.OrgID, id, "resource:data--postgres", nil, func([]byte) error { return nil })
	if !errors.Is(err, installer.err) || store.d[id].Steps[0].Status != StepFailed {
		t.Fatalf("policy failure reported success: %+v %v", store.d[id].Steps, err)
	}
	if plans.bindings["resource:data--postgres"] == nil {
		t.Fatal("created database binding lost after policy failure")
	}
	installer.err = nil
	if err = r.Run(t.Context(), plans.plan.OrgID, id, "resource:data--postgres", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if store.d[id].Steps[0].Status != StepSucceeded || installer.calls != 2 {
		t.Fatalf("policy retry: %+v, calls %d", store.d[id].Steps, installer.calls)
	}
}

func TestDefaultBackupMissingInstallerRejectsBeforeDeployment(t *testing.T) {
	r, store, plans, exec, _, _ := backupRunner(t)
	r.backupPolicies = nil
	if _, _, err := r.Start(t.Context(), plans.plan.OrgID, plans.plan.ID); err == nil {
		t.Fatal("missing installer accepted")
	}
	if len(exec.jobs) != 0 || len(store.d) != 0 {
		t.Fatal("missing installer created a deployment")
	}
}

func TestDefaultBackupInvalidActionRejectsBeforeProvisioning(t *testing.T) {
	for name, mutate := range map[string]func(*planning.Action){
		"negative interval":      func(a *planning.Action) { a.BackupEveryHours = -1 },
		"long interval":          func(a *planning.Action) { a.BackupEveryHours = 169 },
		"existing resource":      func(a *planning.Action) { a.Op = planning.Keep },
		"missing config":         func(a *planning.Action) { a.Resource = nil },
		"wrong subject":          func(a *planning.Action) { a.Subject = planning.ServiceSubject },
		"external source":        func(a *planning.Action) { a.Provider = planning.DependencyExternal; a.Resource.Provider = a.Provider },
		"unqualified Kubernetes": func(a *planning.Action) { a.Provider = "kubernetes"; a.Resource.Provider = a.Provider },
	} {
		t.Run(name, func(t *testing.T) {
			r, store, plans, exec, installer, _ := backupRunner(t)
			mutate(&plans.plan.Actions[0])
			if _, _, err := r.Start(t.Context(), plans.plan.OrgID, plans.plan.ID); err == nil {
				t.Fatal("invalid default accepted")
			}
			if len(exec.jobs) != 0 || len(store.d) != 0 || installer.calls != 0 {
				t.Fatal("invalid default performed effects")
			}
		})
	}
}
