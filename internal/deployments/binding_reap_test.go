package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// bindingReaperFixture is two environments, each with a deployed app, a
// provisioned database whose credentials are in Vault, and a database
// gateway.
func bindingReaperFixture(t *testing.T) (*BindingReaper, *memSecrets, *warnings) {
	t.Helper()
	secrets := &memSecrets{m: map[string][]byte{}}
	bindings := &envBindings{}
	for _, env := range []string{"env_1", "env_2"} {
		for _, field := range resourceCredentialFields {
			secrets.m[resourceKey("org_1", env, "orders", field)] = []byte("secret")
		}
		bindings.bindings = append(bindings.bindings,
			&planning.Binding{
				ID: "app_" + env, OrgID: "org_1", EnvironmentID: env, Subject: planning.ServiceSubject,
				Name: "api", Provider: "cloud-run", Region: "us-central1", ExternalID: "api-" + env,
			},
			&planning.Binding{
				ID: "db_" + env, OrgID: "org_1", EnvironmentID: env, Subject: planning.ResourceSubject,
				Name: "orders", Provider: "cloud-sql", Region: "us-central1", ExternalID: "orders-" + env,
			},
			reapedGatewayBinding(t, env, "gw-"+env))
	}
	w := &warnings{}
	return &BindingReaper{
		Bindings: bindings, Secrets: secrets, Warn: w.warn,
		Environments: projectEnvironments{envs: []*projects.Environment{{ID: "env_1", Name: "production"}, {ID: "env_2", Name: "staging"}}},
	}, secrets, w
}

// hasCredentials reports whether every credential field of the orders
// database in env is still in Vault, and whether any of them is.
func hasCredentials(s *memSecrets, env string) (all, some bool) {
	all = true
	for _, field := range resourceCredentialFields {
		if _, err := s.Get(context.Background(), resourceKey("org_1", env, "orders", field)); err == nil {
			some = true
		} else {
			all = false
		}
	}
	return all, some
}

func TestBindingReaper_environment(t *testing.T) {
	ctx := context.Background()
	r, secrets, w := bindingReaperFixture(t)
	finish := r.Reap(ctx, "org_1", "proj_1", "env_2")
	if finish == nil {
		t.Fatal("an environment with deployments had nothing to clean up")
	}
	// Nothing changes until the environment is gone.
	if all, _ := hasCredentials(secrets, "env_2"); !all || w.all() != "" {
		t.Fatalf("reading changed something: credentials kept %v, warnings %q", all, w.all())
	}
	finish(ctx)
	if _, some := hasCredentials(secrets, "env_2"); some {
		t.Fatal("the deleted environment's database credentials are still in Vault")
	}
	if all, _ := hasCredentials(secrets, "env_1"); !all {
		t.Fatal("another environment's database credentials went too")
	}
	got := w.all()
	for _, want := range []string{
		"the service api went with the environment staging",
		"api-env_2 on cloud-run in us-central1",
		"the resource orders went with the environment staging",
		"orders-env_2 on cloud-sql in us-central1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q isn't reported: %q", want, got)
		}
	}
	if strings.Contains(got, "env_1") {
		t.Fatalf("another environment's deployments are reported: %q", got)
	}
	if strings.Contains(got, "gw-env_2") {
		t.Fatalf("the database gateway is reported here as well as by its own reaper: %q", got)
	}
}

func TestBindingReaper_project(t *testing.T) {
	ctx := context.Background()
	r, secrets, w := bindingReaperFixture(t)
	finish := r.Reap(ctx, "org_1", "proj_1", "")
	if finish == nil {
		t.Fatal("a project with deployments had nothing to clean up")
	}
	finish(ctx)
	for _, env := range []string{"env_1", "env_2"} {
		if _, some := hasCredentials(secrets, env); some {
			t.Fatalf("the credentials in %s are still in Vault", env)
		}
	}
	got := w.all()
	for _, want := range []string{"api-env_1", "orders-env_1", "api-env_2", "orders-env_2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s isn't reported: %q", want, got)
		}
	}
}

func TestBindingReaper_reportsWhatAMoveLeft(t *testing.T) {
	ctx := context.Background()
	r, _, w := bindingReaperFixture(t)
	bindings := r.Bindings.(*envBindings)
	bindings.bindings[3].Replaced = []planning.Binding{{Provider: "render", ExternalID: "srv-old"}}
	r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
	if got := w.all(); !strings.Contains(got, "api-env_2") || !strings.Contains(got, "srv-old on render") {
		t.Fatalf("what a move left running isn't reported: %q", got)
	}
}

// located is an executor that names where a binding runs in its own terms.
type located struct{ Executor }

func (located) Locate(b *planning.Binding) string {
	return "the workload " + b.ExternalID + " in your cluster"
}

func TestBindingReaper_locatesThroughTheExecutor(t *testing.T) {
	ctx := context.Background()
	r, _, w := bindingReaperFixture(t)
	r.Executors = map[string]Executor{"cloud-run": located{}}
	r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
	if got := w.all(); !strings.Contains(got, "the workload api-env_2 in your cluster") {
		t.Fatalf("the executor doesn't say where the service runs: %q", got)
	}
}

func TestBindingReaper_nothingToDo(t *testing.T) {
	ctx := context.Background()
	r, _, w := bindingReaperFixture(t)
	r.Environments = projectEnvironments{envs: []*projects.Environment{{ID: "env_3", Name: "preview"}}}
	if finish := r.Reap(ctx, "org_1", "proj_1", "env_3"); finish != nil {
		t.Fatal("an environment with nothing deployed has something to clean up")
	}
	if w.all() != "" {
		t.Fatalf("warned about nothing: %q", w.all())
	}
}

func TestBindingReaper_bestEffort(t *testing.T) {
	ctx := context.Background()

	t.Run("environments unreadable", func(t *testing.T) {
		r, secrets, w := bindingReaperFixture(t)
		r.Environments = projectEnvironments{err: errors.New("the database is down")}
		if finish := r.Reap(ctx, "org_1", "proj_1", "env_2"); finish != nil {
			t.Fatal("cleaned up without knowing what")
		}
		if all, _ := hasCredentials(secrets, "env_2"); !all || !strings.Contains(w.all(), "the database is down") {
			t.Fatalf("the failure isn't logged: %q", w.all())
		}
	})

	t.Run("bindings unreadable", func(t *testing.T) {
		r, secrets, w := bindingReaperFixture(t)
		r.Bindings = &envBindings{err: errors.New("the database is down")}
		if finish := r.Reap(ctx, "org_1", "proj_1", "env_2"); finish != nil {
			t.Fatal("cleaned up without knowing what")
		}
		if all, _ := hasCredentials(secrets, "env_2"); !all || !strings.Contains(w.all(), "the database is down") {
			t.Fatalf("the failure isn't logged: %q", w.all())
		}
	})

	t.Run("a credential won't delete", func(t *testing.T) {
		r, secrets, w := bindingReaperFixture(t)
		r.Secrets = failingDeletes{memSecrets: secrets, fail: resourceKey("org_1", "env_2", "orders", passwordField)}
		r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
		if _, err := secrets.Get(ctx, resourceKey("org_1", "env_2", "orders", ResourceURL)); !errors.Is(err, ErrNoCredential) {
			t.Fatal("one credential failing kept the others")
		}
		if got := w.all(); !strings.Contains(got, "vault is down") || !strings.Contains(got, "orders-env_2") {
			t.Fatalf("the failure or the leftover isn't logged: %q", got)
		}
	})

	t.Run("no vault", func(t *testing.T) {
		r, _, w := bindingReaperFixture(t)
		r.Secrets = nil
		r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
		if !strings.Contains(w.all(), "orders-env_2") {
			t.Fatalf("the leftover isn't reported: %q", w.all())
		}
	})
}
