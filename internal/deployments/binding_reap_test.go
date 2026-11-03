package deployments

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// Keys makes memSecrets a ResourceSecretKeys.
func (s *memSecrets) Keys(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.m {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

// plainSecrets hides Keys, as a ResourceSecrets that can't list.
type plainSecrets struct{ ResourceSecrets }

// unlistable is memSecrets whose Keys fails.
type unlistable struct{ *memSecrets }

func (unlistable) Keys(context.Context, string) ([]string, error) {
	return nil, errors.New("vault can't list")
}

// extraKeys are what an environment keeps in Vault besides its resources'
// credentials: a service's runtime values on a server of yours, the
// variables a deployment pinned, and the address a deployment that failed
// before it recorded a binding left behind.
func extraKeys(env string) []string {
	return []string{
		resourceKey("org_1", env, "api", "runtime_01"),
		resourceKey("org_1", env, "applied-inputs/dep_1", "vars_01"),
		resourceKey("org_1", env, "cache", ResourceURL),
	}
}

// neighbourKeys belong to environments the fixture never deletes, one of
// them named so that env_2 is a prefix of it, and one in another
// organization.
var neighbourKeys = []string{
	resourceKey("org_1", "env_20", "orders", ResourceURL),
	resourceKey("org_2", "env_2", "orders", ResourceURL),
}

// kept reports whether every key is still in Vault, and whether any is.
func kept(s *memSecrets, keys []string) (all, some bool) {
	all = true
	for _, k := range keys {
		if _, err := s.Get(context.Background(), k); err == nil {
			some = true
		} else {
			all = false
		}
	}
	return all, some
}

// bindingReaperFixture is two environments, each with a deployed app, a
// provisioned database whose credentials are in Vault, and a database
// gateway, and each keeping more in Vault than its bindings name.
func bindingReaperFixture(t *testing.T) (*BindingReaper, *memSecrets, *warnings) {
	t.Helper()
	secrets := &memSecrets{m: map[string][]byte{}}
	for _, k := range neighbourKeys {
		secrets.m[k] = []byte("secret")
	}
	bindings := &envBindings{}
	for _, env := range []string{"env_1", "env_2"} {
		for _, field := range resourceCredentialFields {
			secrets.m[resourceKey("org_1", env, "orders", field)] = []byte("secret")
		}
		for _, k := range extraKeys(env) {
			secrets.m[k] = []byte("secret")
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
	keys := make([]string, 0, len(resourceCredentialFields))
	for _, field := range resourceCredentialFields {
		keys = append(keys, resourceKey("org_1", env, "orders", field))
	}
	return kept(s, keys)
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
	if _, some := kept(secrets, extraKeys("env_2")); some {
		t.Fatal("the deleted environment's runtime values, pinned variables or failed deployment's address are still in Vault")
	}
	if all, _ := hasCredentials(secrets, "env_1"); !all {
		t.Fatal("another environment's database credentials went too")
	}
	if all, _ := kept(secrets, append(extraKeys("env_1"), neighbourKeys...)); !all {
		t.Fatal("a key outside the deleted environment went too")
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
		if _, some := kept(secrets, extraKeys(env)); some {
			t.Fatalf("the other keys in %s are still in Vault", env)
		}
	}
	if all, _ := kept(secrets, neighbourKeys); !all {
		t.Fatal("a key outside the deleted project went too")
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

// A deployment that failed before it recorded a binding still kept things
// in Vault, so an environment with nothing bound is swept too.
func TestBindingReaper_nothingBound(t *testing.T) {
	ctx := context.Background()
	r, secrets, w := bindingReaperFixture(t)
	leftover := resourceKey("org_1", "env_3", "cache", ResourceURL)
	secrets.m[leftover] = []byte("secret")
	r.Environments = projectEnvironments{envs: []*projects.Environment{{ID: "env_3", Name: "preview"}}}
	r.Reap(ctx, "org_1", "proj_1", "env_3")(ctx)
	if _, some := kept(secrets, []string{leftover}); some {
		t.Fatal("what a failed deployment left in Vault is still there")
	}
	if w.all() != "" {
		t.Fatalf("warned about nothing running: %q", w.all())
	}

	r.Secrets = plainSecrets{secrets}
	if finish := r.Reap(ctx, "org_1", "proj_1", "env_3"); finish != nil {
		t.Fatal("an environment with nothing deployed, and a Vault that can't list, has something to clean up")
	}
}

// Without a Vault that lists its keys, only what the bindings name goes.
func TestBindingReaper_withoutListing(t *testing.T) {
	ctx := context.Background()
	for name, wrap := range map[string]func(*memSecrets) ResourceSecrets{
		"can't list":     func(s *memSecrets) ResourceSecrets { return plainSecrets{s} },
		"listing failed": func(s *memSecrets) ResourceSecrets { return unlistable{s} },
	} {
		t.Run(name, func(t *testing.T) {
			r, secrets, w := bindingReaperFixture(t)
			r.Secrets = wrap(secrets)
			r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
			if _, some := hasCredentials(secrets, "env_2"); some {
				t.Fatal("the deleted environment's database credentials are still in Vault")
			}
			if all, _ := kept(secrets, extraKeys("env_2")); !all {
				t.Fatal("keys no binding names went without a listing to find them")
			}
			if name == "listing failed" && !strings.Contains(w.all(), "vault can't list") {
				t.Fatalf("the failed listing isn't logged: %q", w.all())
			}
		})
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
		r.Reap(ctx, "org_1", "proj_1", "env_2")(ctx)
		if _, some := kept(secrets, extraKeys("env_2")); some {
			t.Fatal("the keys stayed because the bindings couldn't be read")
		}
		if !strings.Contains(w.all(), "the database is down") {
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
