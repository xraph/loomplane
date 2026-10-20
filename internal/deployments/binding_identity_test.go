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
