//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

func TestServerPoolSizingPublicPlanApproval(t *testing.T) {
	for _, tc := range []struct {
		name, size  string
		cpu, memory int64
	}{
		{name: "nondefault", size: "    size: {cpu: 0.2, memory: 128}\n", cpu: 200, memory: 128 << 20},
		{name: "default", cpu: 1000, memory: 512 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, r, c, org, project := cloudAPISetup(t)
			defer r.stop()
			base := "/api/v1/projects/" + project
			analyze(t, cfg, c, project)
			db := pgdriver.New()
			if err := db.Open(t.Context(), cfg.Database.URL); err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			sql, err := pgx.Connect(t.Context(), cfg.Database.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer sql.Close(t.Context())
			var cloud cloudprofiles.Profile
			c.must(http.StatusCreated, http.MethodPost, base+"/clouds", cloudprofiles.CreateInput{Name: "Sizing"}, &cloud)
			// Seed verified capacity only. Planning, assignment and approval use
			// public HTTP routes. The stored test credential permits placement
			// revalidation but cannot connect to an SSH runtime.
			now := time.Now().UTC()
			connection := &connections.Connection{ID: "sizing-runtime", OrgID: org, Cloud: "server", Name: "Sizing runtime", Account: "sizing-daemon", Status: "ready", Details: map[string]string{"docker_id": "sizing-daemon", "architecture": "amd64"}, CreatedAt: now, UpdatedAt: now}
			if err = connections.NewPgStore(db).CreateConnection(t.Context(), connection); err != nil {
				t.Fatal(err)
			}
			key, err := security.DeriveKey(cfg.SecretKey, connections.VaultKeyLabel)
			if err != nil {
				t.Fatal(err)
			}
			enc, err := crypto.NewEncryptor(key)
			if err != nil {
				t.Fatal(err)
			}
			vaultDB, err := grove.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			secrets := connections.NewVaultSecrets(secret.NewService(vaultpg.New(vaultDB), enc, secret.WithAppID(connections.VaultAppID)))
			if err = secrets.Put(t.Context(), org, connection.ID, []byte("sizing-test-only")); err != nil {
				t.Fatal(err)
			}
			store := cloudinfra.NewPgStore(db)
			server := &cloudinfra.Server{ID: "sizing-server", OrgID: org, CloudID: cloud.ID, Name: "Sizing server", ConnectionID: connection.ID, RuntimeConnectionID: connection.ID, Provider: "generic", AccountID: "sizing-daemon", Ownership: "adopted", Status: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now, Health: cloudinfra.RuntimeHealth{DockerID: "sizing-daemon", Architecture: "amd64", CPUs: 2, MemoryBytes: 2 << 30, EffectiveCPUMillis: 2000, EffectiveMemoryBytes: 2 << 30, CheckedAt: now}}
			if err = store.SaveServer(t.Context(), server); err != nil {
				t.Fatal(err)
			}
			var pool cloudinfra.Pool
			c.must(http.StatusOK, http.MethodPost, "/api/v1/clouds/"+cloud.ID+"/pools", cloudinfra.PoolInput{Name: "Sizing", Members: []cloudinfra.PoolMember{{ServerID: server.ID, CPUMillis: 2000, MemoryBytes: 2 << 30}}}, &pool)
			var target cloudprofiles.Target
			c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "sizing", Provider: "server", Runtime: "server", PoolID: pool.ID}, &target)
			var envs struct{ Environments []struct{ ID, Name string } }
			c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
			env := ""
			for _, candidate := range envs.Environments {
				if candidate.Name == "production" {
					env = candidate.ID
				}
			}
			if env == "" {
				t.Fatal("production environment missing")
			}
			var preview cloudprofiles.Preview
			c.must(http.StatusOK, http.MethodPost, base+"/environments/"+env+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID}, &preview)
			if result := waitCloudAPI(t, c, base, applyCloudAPI(t, c, base, &preview, "sizing-assignment").ID); result.Status != cloudprofiles.SyncSucceeded {
				t.Fatalf("assignment: %+v", result)
			}
			// A pooled host can't build untrusted source (build isolation), so
			// the service ships a pinned image and the test stays on sizing.
			manifest := fmt.Sprintf("version: 1\nservices:\n  api:\n    source: go:services/api\n    cloud_target: %s\n    public: false\n    build: {image: traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab}\n%signore:\n  resources: [postgres, redis]\n", target.Name, tc.size)
			c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)
			var plan planBody
			c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
			waitPlanned(t, c, &plan)
			if plan.Status != "ready" || !plan.Executable {
				t.Fatalf("plan not executable: %+v", plan)
			}
			persisted, err := planning.NewPgStore(db).GetPlan(t.Context(), org, plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			var service *planning.ServiceConfig
			for _, action := range persisted.Actions {
				if action.Name == "api" {
					service = action.Service
				}
			}
			if service == nil || service.CloudPlacement == nil || service.CloudPlacement.PoolID != pool.ID {
				t.Fatalf("pool runtime config missing: %+v", persisted.Actions)
			}
			cpu, memory, err := planning.ServerResources(service.Size)
			pin := service.CloudPlacement
			if err != nil || cpu != tc.cpu || memory != tc.memory || pin.CPUMillis != cpu || pin.MemoryBytes != memory {
				t.Fatalf("persisted runtime limits %dm/%d differ from requested %dm/%d or pin %dm/%d: %v", cpu, memory, tc.cpu, tc.memory, pin.CPUMillis, pin.MemoryBytes, err)
			}
			c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
			var deployment deploymentBody
			for deadline := time.Now().Add(readyTimeout); ; time.Sleep(50 * time.Millisecond) {
				status := c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &deployment)
				if status == http.StatusOK {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("approved plan never created a deployment: %+v", plan)
				}
			}
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+plan.ID, nil, &plan)
			if plan.Status != "approved" || deployment.ID == "" {
				t.Fatalf("approval did not persist: %+v %+v", plan, deployment)
			}
			var reservedCPU, reservedMemory int64
			if err = sql.QueryRow(t.Context(), `SELECT cpu_millis,memory_bytes FROM loomplane_capacity_reservations WHERE plan_id=$1 AND service='api' AND server_id=$2`, plan.ID, server.ID).Scan(&reservedCPU, &reservedMemory); err != nil || reservedCPU != tc.cpu || reservedMemory != tc.memory {
				t.Fatalf("admitted reservation %dm/%d differs from request %dm/%d: %v", reservedCPU, reservedMemory, tc.cpu, tc.memory, err)
			}
		})
	}
}
