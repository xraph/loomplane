package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

type targetCredentials map[string]*connections.Connection

func (c targetCredentials) Credential(_ context.Context, orgID, id string) (*connections.Connection, []byte, error) {
	if orgID != "org_1" || c[id] == nil {
		return nil, nil, connections.ErrNotFound
	}
	return c[id], []byte("rotated-" + id), nil
}

func targetRunner(t *testing.T, changed bool) (*Runner, *planning.Plan, *fakePlans, *recorder) {
	t.Helper()
	plan := webAndAPI()
	plan.Connections = map[string]string{"server": "wrong-default"}
	for i := range plan.Actions {
		id := fmt.Sprintf("conn_%d", i)
		wire := fmt.Sprintf(`{"id":"service:%s","op":"create","subject":"service","name":"%s","provider":"server","connection_id":"%s","step":%d,"service":{"provider":"server","service":"%s","target":"%s","target_settings":{"host":"host-%d"},"public":false}}`, plan.Actions[i].Name, plan.Actions[i].Name, id, i+1, plan.Actions[i].Service.Service, id, i)
		if err := json.Unmarshal([]byte(wire), &plan.Actions[i]); err != nil {
			t.Fatal(err)
		}
	}
	exec := &recorder{}
	r, _, plans := newRunner(t, plan, exec, nil)
	reg, err := provider.NewRegistry(provider.Provider{Name: "server", Title: "Server", Cloud: "server", Model: provider.Instance,
		Available: true, Runs: []provider.Runs{{Workload: provider.HTTPService, Execution: provider.Container, Creates: "Docker container"}}})
	if err != nil {
		t.Fatal(err)
	}
	r.providers, r.executors = reg, map[string]Executor{"server": exec}
	c := targetCredentials{}
	for i := range plan.Actions {
		id := fmt.Sprintf("conn_%d", i)
		host := fmt.Sprintf("host-%d", i)
		if changed && i == 0 {
			host = "another-server"
		}
		c[id] = &connections.Connection{ID: id, Cloud: "server", Status: connections.StatusReady, Details: map[string]string{"host": host}}
	}
	r.credentials = c
	return r, plan, plans, exec
}

func TestRunner_targetsEachActionAndAllowsCredentialRotation(t *testing.T) {
	r, plan, plans, exec := targetRunner(t, false)
	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"api", "web"} {
		want := fmt.Sprintf("conn_%d", i)
		if b := plans.bindings["service:"+name]; b == nil || b.ConnectionID != want {
			t.Fatalf("%s binding went to the environment default: %+v", name, b)
		}
		if string(exec.jobs[i].Credential) != "rotated-"+want {
			t.Fatal("credential rotation was not used")
		}
	}
}

func TestRunner_rejectsPlacementChangeAfterApproval(t *testing.T) {
	r, plan, plans, exec := targetRunner(t, true)
	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.store.GetDeployment(t.Context(), plan.OrgID, id)
	if err != nil || d.Status != StatusFailed || !strings.Contains(d.Error, "changed since") {
		t.Fatalf("changed target was not rejected: %+v %v", d, err)
	}
	if len(exec.jobs) != 0 || len(plans.bindings) != 0 {
		t.Fatal("deployment mutated the changed target")
	}
}
