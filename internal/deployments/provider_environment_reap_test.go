package deployments

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// reapStore is the in-memory record store, listing a project's records.
type reapStore struct{ *removeStore }

func (s reapStore) ProviderEnvironments(_ context.Context, orgID, projectID string) ([]*ProviderEnvironment, error) {
	var out []*ProviderEnvironment
	for _, pe := range s.rows {
		if pe.OrgID == orgID && pe.ProjectID == projectID {
			out = append(out, pe)
		}
	}
	return out, nil
}

// cascade deletes an environment's records, as deleting its row does.
func (s reapStore) cascade(environmentID string) {
	for k, pe := range s.rows {
		if pe.EnvironmentID == environmentID {
			delete(s.rows, k)
		}
	}
}

// reapCredentials hands out the connections it holds, by ID.
type reapCredentials map[string]*connections.Connection

func (c reapCredentials) Credential(_ context.Context, _, id string) (*connections.Connection, []byte, error) {
	conn, ok := c[id]
	if !ok {
		return nil, nil, connections.ErrNotFound
	}
	return conn, []byte("secret"), nil
}

// untouchable is an executor nothing may remove through.
type untouchable struct {
	Executor
	t *testing.T
}

func (u untouchable) Remove(context.Context, *Job, *planning.Binding) error {
	u.t.Error("the reaper removed something on a provider it leaves alone")
	return nil
}

type providerReapHarness struct {
	vercelAPI *removeAPI
	renderAPI *fakeRenderAPI
	store     reapStore
	bindings  *envBindings
	warnings  *warnings
	reaper    *ProviderEnvironmentReaper
}

// newProviderReapHarness is the environment staging of proj with the
// service web natively on Vercel, and the service api and the database db
// natively in a Render environment of their own. Production shares
// neither provider project.
func newProviderReapHarness(t *testing.T) *providerReapHarness {
	t.Helper()
	h := &providerReapHarness{vercelAPI: &removeAPI{}, renderAPI: newFakeRenderAPI(), warnings: &warnings{}}
	vercelServer := httptest.NewServer(h.vercelAPI)
	t.Cleanup(vercelServer.Close)
	renderServer := httptest.NewServer(h.renderAPI)
	t.Cleanup(renderServer.Close)
	h.store = reapStore{&removeStore{&roleStore{fakePEStore: newFakePEStore()}}}
	for _, pe := range []*ProviderEnvironment{
		{OrgID: "org", ProjectID: "proj", EnvironmentID: "env_staging", Provider: "vercel", ConnectionID: "conn_v", Scope: "web", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "env_9"},
		{OrgID: "org", ProjectID: "proj", EnvironmentID: "env_staging", Provider: "render", ConnectionID: "conn_r", Mode: ModeNative, ProjectRef: "prj_r", EnvironmentRef: "evm_1"},
	} {
		h.store.rows[peKey(pe)] = pe
	}
	h.renderAPI.projects["loomplane-proj"] = "prj_r"
	h.renderAPI.envs["evm_1"] = &renderTestEnv{id: "evm_1", name: "staging", project: "prj_r"}
	h.renderAPI.resources["srv_1"] = map[string]string{"name": "api", "environmentId": "evm_1", "kind": "service"}
	h.renderAPI.resources["dpg_1"] = map[string]string{"name": "db", "environmentId": "evm_1", "kind": "postgres"}
	h.bindings = &envBindings{bindings: []*planning.Binding{
		{ID: "bnd_web", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_staging", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ConnectionID: "conn_v", ExternalID: "prj_1#env_9"},
		{ID: "bnd_api", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_staging", Subject: planning.ServiceSubject, Name: "api", Provider: "render", Region: "oregon", ConnectionID: "conn_r", ExternalID: "srv_1"},
		{ID: "bnd_db", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_staging", Subject: planning.ResourceSubject, Name: "db", Provider: "render", Region: "oregon", ConnectionID: "conn_r", ExternalID: "postgres/dpg_1"},
	}}
	envs := &NativeEnvironments{Store: h.store}
	h.reaper = &ProviderEnvironmentReaper{
		Bindings:     h.bindings,
		Environments: projectEnvironments{envs: []*projects.Environment{{ID: "env_prod", Name: "production"}, {ID: "env_staging", Name: "staging"}}},
		Records:      h.store,
		Credentials: reapCredentials{
			"conn_v": {ID: "conn_v", OrgID: "org", Cloud: connections.Vercel},
			"conn_r": {ID: "conn_r", OrgID: "org", Details: map[string]string{"owner_id": "tea-1"}},
		},
		Executors: map[string]Executor{
			"vercel": &Vercel{Client: vercel.New(vercelServer.URL), Environments: envs},
			"render": &Render{Client: render.New(renderServer.URL), Environments: envs},
		},
		Warn: h.warnings.warn,
	}
	return h
}

// deleteStaging deletes the environment staging the way the projects
// service does: read, cascade, then clean up.
func (h *providerReapHarness) deleteStaging(t *testing.T, keep ...string) {
	t.Helper()
	finish := h.reaper.ReapWithOptions(t.Context(), "org", "proj", "env_staging", projects.DeleteEnvironmentOptions{Keep: keep})
	if finish == nil {
		t.Fatal("an environment with Vercel and Render deployments had nothing to clean up")
	}
	if len(h.vercelAPI.calls) != 0 || len(h.renderAPI.calls) != 0 {
		t.Fatal("reading touched a provider before the environment was gone")
	}
	h.store.cascade("env_staging")
	finish(t.Context())
}

func TestProviderEnvironmentReaperRemovesWhatLoomplaneCreated(t *testing.T) {
	h := newProviderReapHarness(t)
	h.deleteStaging(t)
	for _, want := range []string{
		"DELETE /v9/projects/prj_1/env/var_staging",
		"DELETE /v9/projects/prj_1/custom-environments/env_9",
		"DELETE /v9/projects/prj_1",
	} {
		if !h.vercelAPI.called(want) {
			t.Errorf("vercel wasn't asked to %s: %v", want, h.vercelAPI.calls)
		}
	}
	// The Render environment and project go only through the record read
	// before the cascade took it.
	for _, want := range []string{"DELETE /services/srv_1", "DELETE /postgres/dpg_1", "DELETE /environments/evm_1", "DELETE /projects/prj_r"} {
		if !h.renderAPI.called(want) {
			t.Errorf("render wasn't asked to %s: %v", want, h.renderAPI.calls)
		}
	}
	if got := h.warnings.all(); got != "" {
		t.Fatalf("warned although everything was removed: %q", got)
	}
}

func TestProviderEnvironmentReaperKeepsTickedResources(t *testing.T) {
	h := newProviderReapHarness(t)
	h.deleteStaging(t, "bnd_db", "bnd_unknown")
	if h.renderAPI.called("DELETE /postgres/dpg_1") {
		t.Fatal("the kept database was deleted")
	}
	if !h.renderAPI.called("DELETE /services/srv_1") || !h.vercelAPI.called("DELETE /v9/projects/prj_1") {
		t.Fatal("what wasn't kept stayed")
	}
	if h.renderAPI.called("DELETE /environments/evm_1") || h.renderAPI.called("DELETE /projects/prj_r") {
		t.Fatal("the Render environment holding the kept database was deleted")
	}
	got := h.warnings.all()
	if !strings.Contains(got, "the resource db went with the environment staging") || !strings.Contains(got, "postgres/dpg_1") {
		t.Fatalf("the kept database isn't reported as left running: %q", got)
	}
	if strings.Contains(got, "srv_1") || strings.Contains(got, "prj_1") {
		t.Fatalf("what was removed is reported as left running: %q", got)
	}
}

func TestProviderEnvironmentReaperReportsAFailedRemoval(t *testing.T) {
	h := newProviderReapHarness(t)
	delete(h.reaper.Credentials.(reapCredentials), "conn_r")
	h.deleteStaging(t)
	if !h.vercelAPI.called("DELETE /v9/projects/prj_1") {
		t.Fatal("a failure on Render stopped the Vercel removal")
	}
	got := h.warnings.all()
	for _, want := range []string{"the service api went with the environment staging", "srv_1", "the resource db went with the environment staging", "postgres/dpg_1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q isn't reported: %q", want, got)
		}
	}
	if strings.Contains(got, "prj_1") {
		t.Fatalf("what was removed is reported as left running: %q", got)
	}
}

func TestProviderEnvironmentReaperLeavesOtherProviders(t *testing.T) {
	h := newProviderReapHarness(t)
	cloudRun := &planning.Binding{ID: "bnd_worker", OrgID: "org", ProjectID: "proj", EnvironmentID: "env_staging", Subject: planning.ServiceSubject, Name: "worker", Provider: "cloud-run", Region: "us-central1", ConnectionID: "conn_g", ExternalID: "worker-staging"}
	h.bindings.bindings = []*planning.Binding{cloudRun}
	h.reaper.Executors["cloud-run"] = untouchable{t: t}
	if h.reaper.Removes(cloudRun) {
		t.Fatal("the reaper claims a Cloud Run binding")
	}
	if finish := h.reaper.ReapWithOptions(t.Context(), "org", "proj", "env_staging", projects.DeleteEnvironmentOptions{}); finish != nil {
		h.store.cascade("env_staging")
		finish(t.Context())
	}

	// BindingReaper still reports it, and leaves Vercel and Render to the
	// reaper that removes them.
	w := &warnings{}
	vercelBinding := &planning.Binding{ID: "bnd_web", OrgID: "org", EnvironmentID: "env_staging", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ExternalID: "prj_1#env_9"}
	br := &BindingReaper{
		Bindings:     &envBindings{bindings: []*planning.Binding{cloudRun, vercelBinding}},
		Environments: projectEnvironments{envs: []*projects.Environment{{ID: "env_staging", Name: "staging"}}},
		Removal:      h.reaper, Warn: w.warn,
	}
	br.Reap(t.Context(), "org", "proj", "env_staging")(t.Context())
	got := w.all()
	if !strings.Contains(got, "worker-staging on cloud-run in us-central1") {
		t.Fatalf("the Cloud Run service isn't reported: %q", got)
	}
	if strings.Contains(got, "prj_1") {
		t.Fatalf("the Vercel service is reported by both reapers: %q", got)
	}

	// A whole project's deletion removes nothing, so BindingReaper reports
	// every binding as it always did.
	if finish := h.reaper.Reap(t.Context(), "org", "proj", ""); finish != nil {
		t.Fatal("deleting a project removes what runs on Vercel and Render")
	}
	w.logs = nil
	br.Reap(t.Context(), "org", "proj", "")(t.Context())
	if got := w.all(); !strings.Contains(got, "prj_1") || !strings.Contains(got, "worker-staging") {
		t.Fatalf("a deleted project's deployments aren't all reported: %q", got)
	}
}
