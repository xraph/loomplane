package deployments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

type snapshotRevokedProject struct {
	revokedExistingProject
	calls *int
}

func (s snapshotRevokedProject) VerifyProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, error) {
	*s.calls++
	return nil, nil, security.ErrForbidden
}

type snapshotNativeCredentials struct{ conn *connections.Connection }

func (c snapshotNativeCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return c.conn, []byte("explicit-provider-token"), nil
}

func TestNativeCanaryChangedAccountStopsBeforeSnapshot(t *testing.T) {
	plan := webAndAPI()
	a := &plan.Actions[0]
	a.Provider, a.ConnectionID = "azure-container-apps", "native"
	a.Service.Provider = a.Provider
	details := map[string]string{"tenant_id": "tenant", "subscription_id": "approved-sub", "resource_group": "prod"}
	a.Service.TargetAccountIdentity = connections.AccountIdentity(connections.Azure, "", details)
	a.Service.TargetSettings = planning.ReviewedConnectionSettings(connections.Azure, details)
	details["subscription_id"] = "changed-sub"
	exec := &weighted{}
	runner, _, _ := newRunner(t, plan, exec, fakeVariables{})
	runner.executors[a.Provider] = exec
	runner.credentials = snapshotNativeCredentials{conn: &connections.Connection{ID: "native", OrgID: "org_1", Cloud: connections.Azure, Status: connections.StatusReady, Details: details}}
	secrets := &accessSnapshotSecrets{snapshotSecrets: snapshotSecrets{}}
	runner.resources = secrets
	_, err := runner.canaryJob(t.Context(), &Deployment{ID: "dep", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production"}, plan, CanaryService{ActionID: a.ID})
	if err == nil || secrets.reads != 0 || secrets.writes != 0 || len(exec.jobs) != 0 {
		t.Fatalf("changed canary target reached credential snapshot: error=%v reads=%d writes=%d", err, secrets.reads, secrets.writes)
	}
}

type accessSnapshotSecrets struct {
	snapshotSecrets
	reads, writes int
}

func (s *accessSnapshotSecrets) Get(ctx context.Context, key string) ([]byte, error) {
	s.reads++
	return s.snapshotSecrets.Get(ctx, key)
}
func (s *accessSnapshotSecrets) Put(ctx context.Context, key string, value []byte) error {
	s.writes++
	return s.snapshotSecrets.Put(ctx, key, value)
}

func TestRevokedExternalGrantStopsBeforeOrdinaryAndCanarySnapshots(t *testing.T) {
	for _, mode := range []string{"ordinary", "canary"} {
		t.Run(mode, func(t *testing.T) {
			plan := webAndAPI()
			plan.CreatedBy = "developer"
			plan.Actions[0].Service.Env = []planning.EnvLink{{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject}}
			exec := &weighted{}
			runner, _, plans := newRunner(t, plan, exec, fakeVariables{})
			grantCalls := 0
			runner.existingResources = snapshotRevokedProject{calls: &grantCalls}
			secrets := &accessSnapshotSecrets{snapshotSecrets: snapshotSecrets{resourceKey("org_1", "env_1", "db", ResourceURL): []byte("postgres://app:private-password@db/app")}}
			runner.resources = secrets
			resourceConfig := &planning.ResourceConfig{Provider: "digitalocean", Kind: "postgres", AccountIdentity: "resource-account", ExistingResource: &planning.ExistingResourceConfig{ResourceID: "inventory", EndpointID: "endpoint", Revision: 1, EndpointRevision: 1, AccountIdentity: "resource-account", ExternalID: "database", Incarnation: "database-created"}}
			plan.Actions = append(plan.Actions, planning.Action{ID: "resource:db", Name: "db", Subject: planning.ResourceSubject, Op: planning.Keep, Provider: "digitalocean", ConnectionID: "resource-connection", Resource: resourceConfig})
			raw, err := json.Marshal(resourceConfig)
			if err != nil {
				t.Fatal(err)
			}
			plans.bindings["resource:db"] = &planning.Binding{Subject: planning.ResourceSubject, Name: "db", Provider: "digitalocean", ConnectionID: "resource-connection", Config: raw}
			d := &Deployment{ID: "dep", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production"}
			if mode == "ordinary" {
				err = runner.run(t.Context(), d, plan, plan.Actions[0], &Step{}, nil, func([]byte) error { return nil }, func(string, string) {}, nil)
			} else {
				_, err = runner.canaryJob(t.Context(), d, plan, CanaryService{ActionID: "service:api"})
			}
			if err == nil || grantCalls != 1 || secrets.reads != 0 || secrets.writes != 0 || len(exec.recorder.jobs) != 0 || len(exec.jobs) != 0 {
				t.Fatalf("revoked access reached credential snapshot or provider: error=%v reads=%d writes=%d", err, secrets.reads, secrets.writes)
			}
		})
	}
}
