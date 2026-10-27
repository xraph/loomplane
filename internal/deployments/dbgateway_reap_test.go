package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// envBindings is an in-memory binding store that lists by environment.
type envBindings struct {
	bindings []*planning.Binding
	err      error
}

func (e *envBindings) ListBindings(_ context.Context, org, env string) ([]*planning.Binding, error) {
	if e.err != nil {
		return nil, e.err
	}
	var out []*planning.Binding
	for _, b := range e.bindings {
		if b.OrgID == org && b.EnvironmentID == env {
			out = append(out, b)
		}
	}
	return out, nil
}

// projectEnvironments lists a fixed set of environments.
type projectEnvironments struct {
	envs []*projects.Environment
	err  error
}

func (p projectEnvironments) ListEnvironments(context.Context, string, string) ([]*projects.Environment, error) {
	return p.envs, p.err
}

// warnings records what a reaper warns about.
type warnings struct {
	mu   sync.Mutex
	logs []string
}

func (w *warnings) warn(msg string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logs = append(w.logs, msg+": "+err.Error())
}

func (w *warnings) all() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.logs, "\n")
}

// failingDeletes is memSecrets whose Delete fails for one key.
type failingDeletes struct {
	*memSecrets
	fail string
}

func (f failingDeletes) Delete(ctx context.Context, key string) error {
	if key == f.fail {
		return errors.New("vault is down")
	}
	return f.memSecrets.Delete(ctx, key)
}

func reapedGatewayBinding(t *testing.T, env, externalID string, replaced ...planning.Binding) *planning.Binding {
	t.Helper()
	cfg, err := json.Marshal(planning.ServiceConfig{
		Provider: "cloud-run", Region: "us-central1",
		DatabaseGateway: &planning.DatabaseGatewayConfig{Name: "db-gateway", Suffix: "db.example.com", Resources: []string{"orders"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &planning.Binding{
		ID: "bind_" + env, OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: env,
		Subject: planning.ServiceSubject, Name: "db-gateway", Provider: "cloud-run", Region: "us-central1",
		ExternalID: externalID, Config: cfg, Replaced: replaced,
	}
}

// reaperFixture is two environments, each with a recorded gateway whose
// keys are in Vault and a deployed gateway service, beside an app.
func reaperFixture(t *testing.T) (*DatabaseGatewayReaper, *memSecrets, *warnings) {
	t.Helper()
	secrets := &memSecrets{m: map[string][]byte{}}
	store := &fakeDBGateways{}
	bindings := &envBindings{}
	for _, env := range []string{"env_1", "env_2"} {
		gw := dbgateway.Gateway{OrgID: "org_1", ID: "dbgw_" + env, ProjectID: "proj_1", EnvironmentID: env, Name: "db-gateway"}
		store.gateways = append(store.gateways, gw)
		for _, field := range []string{dbGatewaySigningField, dbGatewaySealingField} {
			secrets.m[dbGatewayKey("org_1", env, gw.ID, field)] = []byte("key")
		}
		bindings.bindings = append(bindings.bindings, reapedGatewayBinding(t, env, "gw-"+env),
			&planning.Binding{ID: "app_" + env, OrgID: "org_1", EnvironmentID: env, Subject: planning.ServiceSubject, Name: "api", ExternalID: "api-" + env})
	}
	w := &warnings{}
	return &DatabaseGatewayReaper{
		Gateways: store, Bindings: bindings, Secrets: secrets, Warn: w.warn,
		Environments: projectEnvironments{envs: []*projects.Environment{{ID: "env_1", Name: "production"}, {ID: "env_2", Name: "staging"}}},
	}, secrets, w
}

func hasKeys(s *memSecrets, env string) bool {
	_, err := s.Get(context.Background(), dbGatewayKey("org_1", env, "dbgw_"+env, dbGatewaySigningField))
	_, err2 := s.Get(context.Background(), dbGatewayKey("org_1", env, "dbgw_"+env, dbGatewaySealingField))
	return err == nil && err2 == nil
}

func TestDatabaseGatewayReaper_environment(t *testing.T) {
	ctx := context.Background()
	r, secrets, w := reaperFixture(t)
	finish := r.Reap(ctx, "org_1", "proj_1", "env_2")
	if finish == nil {
		t.Fatal("an environment with a gateway had nothing to clean up")
	}
	// Nothing changes until the environment is gone.
	if !hasKeys(secrets, "env_2") || w.all() != "" {
		t.Fatalf("reading changed something: keys %v, warnings %q", hasKeys(secrets, "env_2"), w.all())
	}
	finish(ctx)
	if hasKeys(secrets, "env_2") {
		t.Fatal("the deleted environment's gateway keys are still in Vault")
	}
	if !hasKeys(secrets, "env_1") {
		t.Fatal("another environment's gateway keys went too")
	}
	got := w.all()
	if !strings.Contains(got, "gw-env_2 on cloud-run in us-central1") || !strings.Contains(got, "staging") {
		t.Fatalf("the leftover gateway isn't reported where it runs: %q", got)
	}
	if strings.Contains(got, "gw-env_1") || strings.Contains(got, "api-env_2") {
		t.Fatalf("something besides the deleted environment's gateway is reported: %q", got)
	}
}

func TestDatabaseGatewayReaper_project(t *testing.T) {
	ctx := context.Background()
	r, secrets, w := reaperFixture(t)
	finish := r.Reap(ctx, "org_1", "proj_1", "")
	if finish == nil {
		t.Fatal("a project with gateways had nothing to clean up")
	}
	finish(ctx)
	if hasKeys(secrets, "env_1") || hasKeys(secrets, "env_2") {
		t.Fatal("the project's gateway keys are still in Vault")
	}
	if got := w.all(); !strings.Contains(got, "gw-env_1") || !strings.Contains(got, "gw-env_2") {
		t.Fatalf("not every leftover gateway is reported: %q", got)
	}
}

func TestDatabaseGatewayReaper_reportsWhatAMoveLeft(t *testing.T) {
	ctx := context.Background()
	r, _, w := reaperFixture(t)
	bindings := r.Bindings.(*envBindings)
	bindings.bindings[2] = reapedGatewayBinding(t, "env_2", "gw-env_2",
		planning.Binding{Provider: "render", ExternalID: "srv-old"})
	r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
	if got := w.all(); !strings.Contains(got, "gw-env_2") || !strings.Contains(got, "srv-old on render") {
		t.Fatalf("the gateway a move left running isn't reported: %q", got)
	}
}

func TestDatabaseGatewayReaper_nothingToDo(t *testing.T) {
	ctx := context.Background()
	r, _, w := reaperFixture(t)
	r.Environments = projectEnvironments{envs: []*projects.Environment{{ID: "env_3", Name: "preview"}}}
	if finish := r.Reap(ctx, "org_1", "proj_1", "env_3"); finish != nil {
		t.Fatal("an environment without a gateway has something to clean up")
	}
	if w.all() != "" {
		t.Fatalf("warned about nothing: %q", w.all())
	}
}

func TestDatabaseGatewayReaper_bestEffort(t *testing.T) {
	ctx := context.Background()

	t.Run("environments unreadable", func(t *testing.T) {
		r, secrets, w := reaperFixture(t)
		r.Environments = projectEnvironments{err: errors.New("the database is down")}
		if finish := r.Reap(ctx, "org_1", "proj_1", "env_2"); finish != nil {
			t.Fatal("cleaned up without knowing what")
		}
		if !hasKeys(secrets, "env_2") || !strings.Contains(w.all(), "the database is down") {
			t.Fatalf("the failure isn't logged: %q", w.all())
		}
	})

	t.Run("bindings unreadable", func(t *testing.T) {
		r, secrets, w := reaperFixture(t)
		r.Bindings = &envBindings{err: errors.New("the database is down")}
		r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
		if hasKeys(secrets, "env_2") {
			t.Fatal("the keys stayed because the bindings couldn't be read")
		}
		if !strings.Contains(w.all(), "the database is down") {
			t.Fatalf("the failure isn't logged: %q", w.all())
		}
	})

	t.Run("a key won't delete", func(t *testing.T) {
		r, secrets, w := reaperFixture(t)
		stuck := dbGatewayKey("org_1", "env_2", "dbgw_env_2", dbGatewaySigningField)
		r.Secrets = failingDeletes{memSecrets: secrets, fail: stuck}
		r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
		if _, err := secrets.Get(ctx, dbGatewayKey("org_1", "env_2", "dbgw_env_2", dbGatewaySealingField)); !errors.Is(err, ErrNoCredential) {
			t.Fatal("one key failing kept the other")
		}
		if got := w.all(); !strings.Contains(got, "vault is down") || !strings.Contains(got, "dbgw_env_2") || !strings.Contains(got, "gw-env_2") {
			t.Fatalf("the failure or the leftover isn't logged: %q", got)
		}
	})

	t.Run("no vault", func(t *testing.T) {
		r, _, w := reaperFixture(t)
		r.Secrets = nil
		r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
		if !strings.Contains(w.all(), "gw-env_2") {
			t.Fatalf("the leftover isn't reported: %q", w.all())
		}
	})
}
