//go:build integration

package deployments

import (
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// TestBindingReaper_environmentDeletion deletes an environment with a
// deployed app and a provisioned database through the projects service,
// beside the database gateway's own reaper, and checks the database's
// credentials leave Vault and both are reported where they run, though the
// cascade took their bindings with the environment.
func TestBindingReaper_environmentDeletion(t *testing.T) {
	ctx := t.Context()
	pg := reapDatabase(t)
	plans, prj := planning.NewPgStore(pg), projects.NewPgStore(pg)
	secrets := &memSecrets{m: map[string][]byte{}}
	for _, env := range []string{"env_1", "env_2"} {
		for _, field := range resourceCredentialFields {
			secrets.m[resourceKey("org_1", env, "orders", field)] = []byte("secret")
		}
		for _, b := range []*planning.Binding{
			{
				ID: "app_" + env, Subject: planning.ServiceSubject, Name: "api", Engine: "container",
				Provider: "cloud-run", Region: "us-central1", ExternalID: "api-" + env,
			},
			{
				ID: "db_" + env, Subject: planning.ResourceSubject, Name: "orders", Engine: "postgres",
				Provider: "cloud-sql", Region: "us-central1", ExternalID: "orders-" + env,
			},
		} {
			b.OrgID, b.ProjectID, b.EnvironmentID = "org_1", "proj_1", env
			b.CreatedAt, b.UpdatedAt = time.Now(), time.Now()
			if err := plans.PutBinding(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
	}
	w := &warnings{}
	svc := projects.NewService(prj, allowAll{}, nil, nil)
	svc.AddReaper(&BindingReaper{Bindings: plans, Environments: prj, Secrets: secrets, Warn: w.warn})

	if err := svc.DeleteEnvironment(ctx, security.Principal{UserID: "usr_1", OrgID: "org_1"}, "proj_1", "env_2"); err != nil {
		t.Fatal(err)
	}
	if left, err := plans.ListBindings(ctx, "org_1", "env_2"); err != nil || len(left) != 0 {
		t.Fatalf("the cascade left %d bindings: %v", len(left), err)
	}
	if _, some := hasCredentials(secrets, "env_2"); some {
		t.Fatal("the deleted environment's database credentials are still in Vault")
	}
	if all, _ := hasCredentials(secrets, "env_1"); !all {
		t.Fatal("the other environment's database credentials went too")
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
		t.Fatalf("the other environment's deployments are reported: %q", got)
	}

	// The last environment can't go, so nothing of it is touched.
	*w = warnings{}
	if err := svc.DeleteEnvironment(ctx, security.Principal{UserID: "usr_1", OrgID: "org_1"}, "proj_1", "env_1"); err == nil {
		t.Fatal("the last environment was deleted")
	}
	if all, _ := hasCredentials(secrets, "env_1"); !all || w.all() != "" {
		t.Fatalf("a refused deletion cleaned up: credentials kept %v, warnings %q", all, w.all())
	}
}

// TestBindingReaper_projectDeletion deletes a whole project and checks
// every environment's credentials leave Vault and every deployment in it
// is reported.
func TestBindingReaper_projectDeletion(t *testing.T) {
	ctx := t.Context()
	pg := reapDatabase(t)
	plans, prj := planning.NewPgStore(pg), projects.NewPgStore(pg)
	secrets := &memSecrets{m: map[string][]byte{}}
	for _, env := range []string{"env_1", "env_2"} {
		for _, field := range resourceCredentialFields {
			secrets.m[resourceKey("org_1", env, "orders", field)] = []byte("secret")
		}
		b := &planning.Binding{
			ID: "db_" + env, OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: env,
			Subject: planning.ResourceSubject, Name: "orders", Engine: "postgres",
			Provider: "cloud-sql", Region: "us-central1", ExternalID: "orders-" + env,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if err := plans.PutBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	w := &warnings{}
	svc := projects.NewService(prj, allowAll{}, nil, nil)
	svc.AddReaper(&BindingReaper{Bindings: plans, Environments: prj, Secrets: secrets, Warn: w.warn})

	if err := svc.Delete(ctx, security.Principal{UserID: "usr_1", OrgID: "org_1"}, "proj_1"); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"env_1", "env_2"} {
		if left, err := plans.ListBindings(ctx, "org_1", env); err != nil || len(left) != 0 {
			t.Fatalf("the cascade left %d bindings in %s: %v", len(left), env, err)
		}
		if _, some := hasCredentials(secrets, env); some {
			t.Fatalf("the credentials in %s are still in Vault", env)
		}
	}
	got := w.all()
	for _, want := range []string{"orders-env_1", "orders-env_2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s isn't reported: %q", want, got)
		}
	}
}
