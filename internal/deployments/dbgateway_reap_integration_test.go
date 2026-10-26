//go:build integration

package deployments

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// allowAll lets every caller do everything.
type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// reapDatabase is a database of its own, migrated like production and
// holding one project with two environments.
func reapDatabase(t *testing.T) *pgdriver.PgDB {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the database gateway reaper test")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_dbgateway_reap_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	if err = ops.Migrate(ctx, u.String(), migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	for _, q := range []string{
		`INSERT INTO loomplane_projects (id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES ('proj_1','org_1','P','p','active','usr_1',now(),now())`,
		`INSERT INTO loomplane_environments (id,org_id,project_id,name,kind,created_at,updated_at) VALUES ('env_1','org_1','proj_1','production','production',now(),now())`,
		`INSERT INTO loomplane_environments (id,org_id,project_id,name,kind,created_at,updated_at) VALUES ('env_2','org_1','proj_1','staging','staging',now(),now())`,
	} {
		if _, err = pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return pg
}

// TestDatabaseGatewayReaper_environmentDeletion deletes an environment with
// a deployed database gateway through the projects service, and checks the
// gateway's keys leave Vault and its cloud service is reported, though the
// cascade took both records with the environment.
func TestDatabaseGatewayReaper_environmentDeletion(t *testing.T) {
	ctx := t.Context()
	pg := reapDatabase(t)
	gateways, plans, prj := dbgateway.NewStore(pg), planning.NewPgStore(pg), projects.NewPgStore(pg)
	secrets := &memSecrets{m: map[string][]byte{}}
	for _, env := range []string{"env_1", "env_2"} {
		signing, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		gw, _, err := gateways.EnsureGateway(ctx, dbgateway.Gateway{
			OrgID: "org_1", ID: "dbgw_" + env, ProjectID: "proj_1", EnvironmentID: env, Name: "db-gateway",
			Provider: "cloud-run", Region: "us-central1", Suffix: "db.example.com",
			SigningPublicKey: signing, SealingPublicKey: sealing.PublicKey(),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{dbGatewaySigningField, dbGatewaySealingField} {
			secrets.m[dbGatewayKey("org_1", env, gw.ID, field)] = []byte("key")
		}
		b := reapedGatewayBinding(t, env, "gw-"+env)
		b.CreatedAt, b.UpdatedAt, b.Engine = time.Now(), time.Now(), "container"
		if err = plans.PutBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	w := &warnings{}
	svc := projects.NewService(prj, allowAll{}, nil, nil)
	svc.AddReaper(&DatabaseGatewayReaper{Gateways: gateways, Bindings: plans, Environments: prj, Secrets: secrets, Warn: w.warn})

	if err := svc.DeleteEnvironment(ctx, security.Principal{UserID: "usr_1", OrgID: "org_1"}, "proj_1", "env_2"); err != nil {
		t.Fatal(err)
	}
	if left, err := gateways.Gateways(ctx, "org_1", "env_2"); err != nil || len(left) != 0 {
		t.Fatalf("the cascade left %d gateways: %v", len(left), err)
	}
	if hasKeys(secrets, "env_2") {
		t.Fatal("the deleted environment's gateway keys are still in Vault")
	}
	if !hasKeys(secrets, "env_1") {
		t.Fatal("the other environment's gateway keys went too")
	}
	got := w.all()
	if !strings.Contains(got, "gw-env_2 on cloud-run in us-central1") || !strings.Contains(got, "staging") {
		t.Fatalf("the gateway service left running isn't reported: %q", got)
	}
	if strings.Contains(got, "gw-env_1") {
		t.Fatalf("the other environment's gateway is reported: %q", got)
	}

	// The last environment can't go, so nothing of it is touched.
	*w = warnings{}
	if err := svc.DeleteEnvironment(ctx, security.Principal{UserID: "usr_1", OrgID: "org_1"}, "proj_1", "env_1"); err == nil {
		t.Fatal("the last environment was deleted")
	}
	if !hasKeys(secrets, "env_1") || w.all() != "" {
		t.Fatalf("a refused deletion cleaned up: keys %v, warnings %q", hasKeys(secrets, "env_1"), w.all())
	}
}
