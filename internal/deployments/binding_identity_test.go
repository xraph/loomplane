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
