//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestCapacityPolicyMigration(t *testing.T) {
	clouds, sql, db := cloudSyncDatabase(t)
	f := &infraFixture{sql: sql, p: security.Principal{OrgID: "org", UserID: "manager"}, svc: cloudinfra.NewService(cloudinfra.Deps{Store: cloudinfra.NewPgStore(db), Clouds: clouds, Authz: &infraAuth{}})}
	ctx := t.Context()
	legacy, err := f.svc.GetCapacityPolicy(ctx, f.p, "cloud")
	if err != nil || legacy.Enabled || legacy.Revision != 0 || legacy.OrgID != "org" || legacy.CloudID != "cloud" {
		t.Fatalf("legacy defaults: %+v %v", legacy, err)
	}
	var migration *migrate.Migration
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "cloud_capacity" {
			migration = m
		}
	}
	if migration == nil {
		t.Fatal("capacity migration missing")
	}
	for range 2 {
		if err = migration.Up(ctx, pgmigrate.New(db)); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"loomplane_cloud_capacity_policies", "loomplane_cloud_capacity_inventory", "loomplane_cloud_capacity_reservations", "loomplane_cloud_expansions", "loomplane_cloud_expansion_demands", "loomplane_frontend_slots", "loomplane_deployment_capacity_waits"} {
		var count int
		if err = f.sql.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("legacy capacity rows in %s: %d %v", table, count, err)
		}
	}
	if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,enabled,servers,cpu_millis,memory_bytes,storage_bytes,body) VALUES('org','cloud',1,true,1,-1,1,1,'{}')`); err == nil {
		t.Fatal("negative SQL resource accepted")
	}
	if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,body) VALUES('foreign','cloud',1,'{}')`); err == nil {
		t.Fatal("foreign Cloud SQL identity accepted")
	}
}

func TestCapacityPolicyAuthorization(t *testing.T) {
	f := newInfraFixture(t)
	ctx := t.Context()
	zero := int64(0)
	input := cloudinfra.CapacityPolicyInput{Policy: cloudinfra.CapacityPolicy{OrgID: "forged", CloudID: "forged", Revision: 42, FrontendLimits: []cloudinfra.FrontendLimit{{TargetID: "target", Maximum: &zero}}}}
	saved, err := f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", input)
	if err != nil || saved.Revision != 1 || saved.OrgID != "org" || saved.CloudID != "cloud" {
		t.Fatalf("save identity: %+v %v", saved, err)
	}
	read, err := f.svc.GetCapacityPolicy(ctx, f.p, "cloud")
	if err != nil || len(read.FrontendLimits) != 1 || read.FrontendLimits[0].Maximum == nil || *read.FrontendLimits[0].Maximum != 0 {
		t.Fatalf("frontend zero lost: %+v %v", read, err)
	}
	for _, revision := range []int64{0, 2} {
		input.ExpectedRevision = revision
		if _, err = f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", input); !errors.Is(err, cloudinfra.ErrConflict) {
			t.Fatalf("stale revision %d: %v", revision, err)
		}
	}
	input.ExpectedRevision = 1
	readonly := f.p
	readonly.ReadOnly = true
	if _, err = f.svc.SaveCapacityPolicy(ctx, readonly, "cloud", input); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read-only write: %v", err)
	}
	for _, principal := range []security.Principal{{OrgID: "foreign", UserID: "manager"}, {OrgID: "org", UserID: "reader"}, {}} {
		if _, err = f.svc.GetCapacityPolicy(ctx, principal, "cloud"); err == nil {
			t.Fatalf("foreign read: %+v", principal)
		}
		if _, err = f.svc.SaveCapacityPolicy(ctx, principal, "cloud", input); err == nil {
			t.Fatalf("foreign write: %+v", principal)
		}
	}
	if _, err = f.store.GetCapacityPolicy(ctx, "foreign", "cloud"); !errors.Is(err, cloudinfra.ErrNotFound) {
		t.Fatalf("store foreign Cloud: %v", err)
	}
	input.Policy.FrontendLimits[0].TargetID = "foreign-target"
	if _, err = f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", input); !errors.Is(err, cloudinfra.ErrInvalid) || !strings.Contains(err.Error(), "target foreign-target names no managed target") {
		t.Fatalf("foreign target: %v", err)
	}
	input.Policy.FrontendLimits = nil
	input.Policy.Limits = cloudinfra.ResourceVector{Servers: 1, CPUMillis: 1, MemoryBytes: 1, StorageBytes: 1}
	saved, err = f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", input)
	if err != nil || saved.Revision != 2 {
		t.Fatalf("lower ceiling rejected: %+v %v", saved, err)
	}
	if f.provider.creates != 0 || f.workflow.calls != 0 {
		t.Fatal("saving policy triggered provisioning")
	}
}

type capacityPolicyProvider struct{ *infraProvider }

func (p capacityPolicyProvider) ServerOptions(context.Context, provider.InfrastructureCredential) (provider.ServerOptions, error) {
	return provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}}, Sizes: []provider.ServerSize{{ID: "small", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Regions: []string{"region"}}}, Images: []provider.ServerImage{{ID: "ubuntu", Bootstrap: true, Regions: []string{"region"}}}}, nil
}

type capacityPolicyAudit struct{ events []projects.AuditEvent }

func (a *capacityPolicyAudit) Record(_ context.Context, event projects.AuditEvent) {
	a.events = append(a.events, event)
}

func TestCapacityPolicyEnabledPersistence(t *testing.T) {
	f := newInfraFixture(t)
	ctx := t.Context()
	if _, err := f.sql.Exec(ctx, `INSERT INTO loomplane_server_pools(id,org_id,cloud_id,revision,body) VALUES('pool','org','cloud',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", capacityPolicyProvider{f.provider}); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	audit := &capacityPolicyAudit{}
	f.svc.Audit = audit
	policy := cloudinfra.CapacityPolicy{Enabled: true, RequireExpansionApproval: true, Limits: cloudinfra.ResourceVector{Servers: 4, CPUMillis: 8000, MemoryBytes: 8 << 30, StorageBytes: 100_000_000_000}, Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "pool", ConnectionID: "infra-account", Region: "region", Architecture: "amd64", Image: "ubuntu", AllowedSizes: []string{"small"}, MinServers: 1, MaxServers: 4, BootstrapSize: "forged"}}}
	saved, err := f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", cloudinfra.CapacityPolicyInput{Policy: policy})
	if err != nil || saved.Revision != 1 || saved.Pools[0].BootstrapSize != "small" || !saved.RequireExpansionApproval {
		t.Fatalf("enabled save: %+v %v", saved, err)
	}
	read, err := f.svc.GetCapacityPolicy(ctx, f.p, "cloud")
	if err != nil || !read.Enabled || read.Pools[0].BootstrapSize != "small" || read.Limits.StorageBytes != 100_000_000_000 {
		t.Fatalf("enabled round trip: %+v %v", read, err)
	}
	if len(audit.events) != 1 || audit.events[0].Action != "cloud.capacity_policy.write" || audit.events[0].Principal.UserID != "manager" || audit.events[0].Attributes["revision"] != int64(1) {
		t.Fatalf("missing policy audit: %+v", audit.events)
	}
	// Existing resource overage does not prevent reducing the saved ceiling.
	if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_inventory(org_id,cloud_id,id,provider,account_id,resource_id,servers,cpu_millis,memory_bytes,storage_bytes) VALUES('org','cloud','inventory','digitalocean','account','resource',1,2000,2147483648,20000000000)`); err != nil {
		t.Fatal(err)
	}
	saved.Enabled = true
	saved.Limits = cloudinfra.ResourceVector{Servers: 1, CPUMillis: 1000, MemoryBytes: 1 << 30, StorageBytes: 10_000_000_000}
	saved.Pools[0].MinServers = 0
	saved.Pools[0].MaxServers = 1
	saved, err = f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", cloudinfra.CapacityPolicyInput{ExpectedRevision: 1, Policy: *saved})
	if err != nil || !saved.Enabled || saved.Revision != 2 {
		t.Fatalf("overage update: %+v %v", saved, err)
	}
	saved.Enabled = false
	disabled, disableErr := f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", cloudinfra.CapacityPolicyInput{ExpectedRevision: 2, Policy: *saved})
	if disableErr != nil || disabled.Enabled || disabled.Limits.CPUMillis != 1000 {
		t.Fatalf("disable lost ceilings: %+v %v", disabled, disableErr)
	}
	if f.provider.creates != 0 || f.workflow.calls != 0 {
		t.Fatal("policy mutation provisioned capacity")
	}
}

func TestCapacityPolicyFrontendTargets(t *testing.T) {
	f := newInfraFixture(t)
	ctx := t.Context()
	for _, query := range []string{
		`INSERT INTO loomplane_server_pools(id,org_id,cloud_id,revision,body) VALUES('pool','org','cloud',1,'{}')`,
		`INSERT INTO loomplane_cloud_targets(id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,runtime,pool_id,revision,created_at,updated_at) VALUES('pool-target','org','proj','cloud','pool-target','Pool target','server',NULL,'server','pool',1,now(),now())`,
		`INSERT INTO loomplane_cloud_targets(id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,revision,created_at,updated_at) VALUES('database-target','org','proj','cloud','database-target','Database target','rds','old',1,now(),now())`,
	} {
		if _, err := f.sql.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"database-target", "pool-target"} {
		input := cloudinfra.CapacityPolicyInput{Policy: cloudinfra.CapacityPolicy{FrontendLimits: []cloudinfra.FrontendLimit{{TargetID: target}}}}
		if _, err := f.svc.SaveCapacityPolicy(ctx, f.p, "cloud", input); !errors.Is(err, cloudinfra.ErrInvalid) || !strings.Contains(err.Error(), target) {
			t.Fatalf("non-frontend target %s: %v", target, err)
		}
	}
}
