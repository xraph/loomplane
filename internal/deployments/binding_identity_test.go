package deployments

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type bindingIdentityCredentials struct{ sameAccount bool }

func (c bindingIdentityCredentials) Credential(_ context.Context, _, id string) (*connections.Connection, []byte, error) {
	account := id
	if c.sameAccount {
		account = "same-team"
	}
	return &connections.Connection{ID: id, Cloud: connections.Vercel, Status: connections.StatusReady, Details: map[string]string{"team_id": account}}, []byte("secret-" + id), nil
}
func TestRunnerSameExternalIDInDifferentAccountsCleansOnlyTheOriginal(t *testing.T) {
	for _, sameAccount := range []bool{false, true} {
		t.Run(map[bool]string{false: "different accounts", true: "same account alias"}[sameAccount], func(t *testing.T) {
			log := &moveLog{}
			exec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK), externalID: "account-scoped-id"}
			plan := apiMoves()
			plan.Actions[0].Op = planning.Replace
			old := &planning.Binding{ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject, Name: "api", Provider: "vercel", ExternalID: "account-scoped-id", ConnectionID: "conn_old"}
			runner, _, plans, _ := moveRunner(t, plan, old, map[string]Executor{"vercel": exec})
			runner.credentials = bindingIdentityCredentials{sameAccount: sameAccount}
			if _, err := runAll(t, runner, plan); err != nil {
				t.Fatal(err)
			}
			want := []string{"apply vercel"}
			if !sameAccount {
				want = append(want, "remove vercel account-scoped-id")
			}
			if !slices.Equal(log.list(), want) {
				t.Fatalf("%v want %v", log.list(), want)
			}
			for _, job := range exec.removes {
				if job.Connection.ID != "conn_old" {
					t.Fatalf("removed serving account %s", job.Connection.ID)
				}
			}
			bound := plans.bindings["service:api"]
			if bound.ConnectionID != "conn_v" || len(bound.Replaced) != 0 {
				t.Fatalf("serving binding %+v", bound)
			}
		})
	}
}

func TestRunnerRemovalCheckpointsPreserveLegacyProgressAndScopeNewProgress(t *testing.T) {
	for _, test := range []struct{ name, scope, want string }{
		{name: "legacy checkpoint", want: "delete-operation"},
		{name: "matching scope", scope: bindingIdentity(&planning.Binding{Provider: "vercel", ConnectionID: "conn_old", ExternalID: "scoped-id"}), want: "delete-operation"},
		{name: "another account", scope: bindingIdentity(&planning.Binding{Provider: "vercel", ConnectionID: "conn_other", ExternalID: "scoped-id"}), want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := &moveLog{}
			exec := &placed{provider: "vercel", log: log}
			runner := &Runner{credentials: bindingIdentityCredentials{}, executors: map[string]Executor{"vercel": exec}}
			old := &planning.Binding{Provider: "vercel", ConnectionID: "conn_old", ExternalID: "scoped-id"}
			job := &Job{Deployment: &Deployment{OrgID: "org_1"}, Plan: &planning.Plan{}, Action: planning.Action{Name: "api", Provider: "vercel"}, Progress: func(string, string) {}}
			checkpoint := &replaceCheckpoint{Replace: true, Removing: "scoped-id", RemovingScope: test.scope, Remove: []byte("delete-operation")}
			done, err := runner.removeOne(t.Context(), job, old, checkpoint, func([]byte) error { return nil })
			if err != nil || !done {
				t.Fatalf("cleanup %v %v", done, err)
			}
			if len(exec.removes) != 1 || string(exec.removes[0].Resume) != test.want {
				t.Fatalf("removal resume %+v", exec.removes)
			}
			stored := readReplaceCheckpoint(checkpoint.encode())
			if stored.Removing != "scoped-id" || stored.RemovingScope != bindingIdentity(old) || string(stored.Remove) != "rm-vercel" {
				t.Fatalf("unscoped persisted checkpoint %+v", stored)
			}
		})
	}
}

func TestRunnerRegionChangeThroughSameAccountAliasRetainsOriginalResource(t *testing.T) {
	for _, sameAccount := range []bool{true, false} {
		t.Run(map[bool]string{true: "same account alias", false: "different accounts"}[sameAccount], func(t *testing.T) {
			log := &moveLog{}
			exec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK), externalID: "scoped-id"}
			plan := apiMoves()
			plan.Actions[0].Op = planning.Replace
			plan.Actions[0].Region = "new-region"
			old := &planning.Binding{ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject, Name: "api", Provider: "vercel", Region: "old-region", ExternalID: "scoped-id", ConnectionID: "conn_old"}
			runner, store, plans, _ := moveRunner(t, plan, old, map[string]Executor{"vercel": exec})
			runner.credentials = bindingIdentityCredentials{sameAccount: sameAccount}
			id, err := runAll(t, runner, plan)
			if err != nil {
				t.Fatal(err)
			}
			deployment, _ := store.GetDeployment(t.Context(), "org_1", id)
			bound := plans.bindings["service:api"]
			if sameAccount {
				if deployment.Status != StatusFailed || len(exec.removes) != 0 || bound.ConnectionID != "conn_old" || bound.Region != "old-region" {
					t.Fatalf("retained resource was changed: deployment %+v binding %+v removals %+v", deployment, bound, exec.removes)
				}
			} else {
				if deployment.Status != StatusSucceeded || len(exec.removes) != 1 || exec.removes[0].Connection.ID != "conn_old" || bound.ConnectionID != "conn_v" || bound.Region != "new-region" {
					t.Fatalf("cross-account move: deployment %+v binding %+v removals %+v", deployment, bound, exec.removes)
				}
			}
		})
	}
}

func TestRunnerCleanupNeverDeletesRetainedResourceThroughRegionAlias(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "vercel", log: log}
	plan := apiMoves()
	original := &planning.Binding{ID: "bind_api", Provider: "vercel", Region: "old-region", ConnectionID: "conn_old", ExternalID: "scoped-id", Name: "api", Subject: planning.ServiceSubject}
	serving := *original
	serving.ConnectionID = "conn_v"
	serving.Region = "new-region"
	serving.Replaced = []planning.Binding{*original}
	runner, _, plans, _ := moveRunner(t, plan, &serving, map[string]Executor{"vercel": exec})
	runner.credentials = bindingIdentityCredentials{sameAccount: true}
	job := &Job{Deployment: &Deployment{OrgID: "org_1"}, Plan: plan, Action: plan.Actions[0]}
	if err := runner.removeReplaced(t.Context(), job, &serving, original, nil, &replaceCheckpoint{}, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(exec.removes) != 0 || len(plans.bindings["service:api"].Replaced) != 0 {
		t.Fatalf("retained resource cleanup: removes %+v binding %+v", exec.removes, plans.bindings["service:api"])
	}
}
