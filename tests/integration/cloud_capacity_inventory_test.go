//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type capacityInventoryProvider struct {
	*infraProvider
	snapshot provider.CapacityInventory
	calls    int
	failure  error
}

func (p *capacityInventoryProvider) CapacityInventory(context.Context, provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	p.calls++
	p.snapshot.CheckedAt = time.Now().UTC()
	return p.snapshot, p.failure
}
func inventoryNumber(n int64) *int64 { return &n }

func TestCapacityInventoryPersistenceAndAttribution(t *testing.T) {
	f := newInfraFixture(t)
	ctx := t.Context()
	adapter := &capacityInventoryProvider{infraProvider: f.provider, snapshot: provider.CapacityInventory{Provider: "digitalocean", AccountID: "account", Servers: []provider.CapacityServer{{ID: "1", CPUMillis: inventoryNumber(1000), MemoryBytes: inventoryNumber(1 << 30), StorageBytes: inventoryNumber(20_000_000_000)}, {ID: "2", CPUMillis: inventoryNumber(1000), MemoryBytes: inventoryNumber(1 << 30), StorageBytes: inventoryNumber(20_000_000_000)}, {ID: "foreign", CPUMillis: inventoryNumber(10000)}}, Volumes: []provider.CapacityVolume{{ID: "1", ServerIDs: []string{"1"}, StorageBytes: inventoryNumber(5_000_000_000)}, {ID: "foreign", StorageBytes: inventoryNumber(100_000_000_000)}}, Quota: provider.CapacityQuota{Servers: inventoryNumber(25)}}}
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", adapter); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	now := time.Now().UTC()
	if err := f.connections.CreateConnection(ctx, &connections.Connection{ID: "alias", OrgID: "org", Cloud: "digitalocean", Name: "Alias", Account: "Account", Details: map[string]string{"account_uuid": "account"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := f.secrets.Put(ctx, "org", "alias", []byte("dop_v1_fixture")); err != nil {
		t.Fatal(err)
	}
	for _, v := range []*cloudinfra.Server{
		{ID: "host", OrgID: "org", CloudID: "cloud", ConnectionID: "infra-account", Provider: "digitalocean", AccountID: "account", ResourceID: "1", Ownership: "managed", Status: "provisioning", Revision: 1},
		{ID: "host-alias", OrgID: "org", CloudID: "cloud", ConnectionID: "alias", Provider: "digitalocean", AccountID: "account", ResourceID: "2", Ownership: "managed", Status: "provisioning", Revision: 1},
	} {
		if err := f.store.SaveServer(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := f.svc.RefreshCapacity(ctx, f.p, "cloud")
	if err != nil || *usage.Provisioned.Servers != 2 || *usage.Provisioned.CPUMillis != 2000 || *usage.Provisioned.StorageBytes != 45_000_000_000 || adapter.calls != 1 {
		t.Fatalf("initial scope/dedup: %+v calls=%d %v", usage, adapter.calls, err)
	}
	if len(usage.Accounts) != 1 || *usage.Accounts[0].Provisioned.Servers != 3 || *usage.Accounts[0].Quota.Servers != 25 {
		t.Fatal("account facts missing")
	}
	account, e := f.store.GetCapacityAccount(ctx, "org", "digitalocean", "account")
	if e != nil || *account.Provisioned.Servers != 3 || *account.Quota.Servers != 25 || account.Quota.StorageBytes != nil {
		t.Fatalf("account reload: %+v %v", account, e)
	}
	var accounts int
	if e = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_capacity_accounts WHERE org_id='org'`).Scan(&accounts); e != nil || accounts != 1 {
		t.Fatalf("account aliases duplicated: %d %v", accounts, e)
	}
	if _, e = f.store.GetCapacityAccount(ctx, "foreign", "digitalocean", "account"); !errors.Is(e, cloudinfra.ErrNotFound) {
		t.Fatalf("foreign account exposed: %v", e)
	}

	stored, err := f.store.ListCapacityInventory(ctx, "org", "cloud")
	if err != nil || len(stored) != 3 {
		t.Fatalf("persistence: %+v %v", stored, err)
	}
	// Same native ID must be valid in the distinct server and volume namespaces.
	var count int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_cloud_capacity_inventory WHERE org_id='org' AND cloud_id='cloud' AND resource_id IN ('server:1','volume:1')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("resource namespaces: %d %v", count, err)
	}
	adapter.snapshot.Volumes[0].ServerIDs = nil
	usage, err = f.svc.RefreshCapacity(ctx, f.p, "cloud")
	if err != nil || *usage.Provisioned.StorageBytes != 45_000_000_000 {
		t.Fatalf("detached volume disappeared: %+v %v", usage, err)
	}
	// A failing refresh preserves the last complete snapshot.
	adapter.failure = provider.ErrInfrastructureScope
	if _, err = f.svc.RefreshCapacity(ctx, f.p, "cloud"); !errors.Is(err, provider.ErrInfrastructureScope) {
		t.Fatalf("permission failure: %v", err)
	}
	stored, err = f.store.ListCapacityInventory(ctx, "org", "cloud")
	if err != nil || len(stored) != 3 {
		t.Fatal("failed refresh replaced inventory")
	}
	adapter.failure = nil
	adapter.snapshot.Volumes = nil
	usage, err = f.svc.RefreshCapacity(ctx, f.p, "cloud")
	if err != nil || *usage.Provisioned.StorageBytes != 40_000_000_000 {
		t.Fatalf("authoritative deletion retained: %+v %v", usage, err)
	}
	readonly := f.p
	readonly.ReadOnly = true
	for _, principal := range []security.Principal{readonly, {OrgID: "org", UserID: "reader"}, {OrgID: "foreign", UserID: "manager"}, {}} {
		if _, err = f.svc.RefreshCapacity(ctx, principal, "cloud"); err == nil {
			t.Fatal("unauthorized refresh")
		}
	}
	// Nullable resource facts survive JSONB and nullable SQL columns.
	adapter.snapshot.Servers[0].StorageBytes = nil
	usage, err = f.svc.RefreshCapacity(ctx, f.p, "cloud")
	if err != nil || usage.Provisioned.StorageBytes != nil {
		t.Fatalf("missing disk became zero: %+v %v", usage, err)
	}
	stored, err = f.store.ListCapacityInventory(ctx, "org", "cloud")
	if err != nil {
		t.Fatalf("null persistence: %+v %v", stored, err)
	}
	for _, record := range stored {
		if record.ResourceID == "server:1" && record.Resources.StorageBytes != nil {
			t.Fatal("null resource storage did not persist")
		}
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["provisioned"].(map[string]any)["storage_bytes"] != nil {
		t.Fatal("JSON null lost")
	}
	// An older refresh cannot replace a newer account sample or its resource rows.
	account, e = f.store.GetCapacityAccount(ctx, "org", "digitalocean", "account")
	if e != nil {
		t.Fatal(e)
	}
	account.CheckedAt = account.CheckedAt.Add(time.Hour)
	generation, e := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.SaveCapacityInventory(ctx, "org", "cloud", generation, stored, []cloudinfra.CapacityAccountSnapshot{*account}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.RefreshCapacity(ctx, f.p, "cloud"); !errors.Is(e, cloudinfra.ErrConflict) {
		t.Fatalf("regressive snapshot accepted: %v", e)
	}
	persisted, e := f.store.GetCapacityAccount(ctx, "org", "digitalocean", "account")
	if e != nil || !persisted.CheckedAt.Equal(account.CheckedAt) {
		t.Fatalf("newer snapshot replaced: %+v %v", persisted, e)
	}
}

func TestCapacityInventoryRejectsRegressiveDockerSnapshots(t *testing.T) {
	for _, state := range []string{"reduced capacity", "unhealthy", "absent"} {
		t.Run(state, func(t *testing.T) {
			f := newInfraFixture(t)
			ctx := t.Context()
			oldTime := time.Now().UTC().Add(-time.Minute)
			newTime := oldTime.Add(time.Second)
			older := cloudinfra.CapacityRecord{Kind: "server", Provider: "server", AccountID: "daemon", ResourceID: "server:daemon", Resources: cloudinfra.CapacityResources{Servers: inventoryNumber(1), CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(1 << 30)}, Observation: &cloudinfra.CapacityObservation{CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(1 << 30), CheckedAt: oldTime}, CheckedAt: oldTime}
			newer := older
			newer.Resources.CPUMillis = inventoryNumber(1000)
			newer.Observation = &cloudinfra.CapacityObservation{CPUMillis: inventoryNumber(1000), MemoryBytes: inventoryNumber(1 << 30), CheckedAt: newTime}
			newer.CheckedAt = newTime
			next := []cloudinfra.CapacityRecord{newer}
			if state == "unhealthy" {
				newer.Observation = nil
				next = []cloudinfra.CapacityRecord{newer}
			}
			if state == "absent" {
				next = nil
			}
			olderGeneration, err := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			newerGeneration, err := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", newerGeneration, next, nil); err != nil {
				t.Fatal(err)
			}
			if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", olderGeneration, []cloudinfra.CapacityRecord{older}, nil); !errors.Is(err, cloudinfra.ErrConflict) {
				t.Fatalf("older Docker-only snapshot replaced newer observation: %v", err)
			}
			stored, err := f.store.ListCapacityInventory(ctx, "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			if state == "absent" {
				if len(stored) != 0 {
					t.Fatalf("deleted observation resurrected: %+v", stored)
				}
				return
			}
			if len(stored) != 1 || !stored[0].CheckedAt.Equal(newTime) {
				t.Fatalf("newer resource snapshot lost: %+v", stored)
			}
			if state == "unhealthy" {
				if stored[0].Observation != nil {
					t.Fatal("old healthy observation replaced unhealthy snapshot")
				}
				return
			}
			if stored[0].Observation == nil || *stored[0].Observation.CPUMillis != 1000 || !stored[0].Observation.CheckedAt.Equal(newTime) {
				t.Fatalf("newer independent observation lost: %+v", stored)
			}
		})
	}
}

func TestCapacityInventoryFailedGenerationPreservesCommittedFacts(t *testing.T) {
	f := newInfraFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	record := cloudinfra.CapacityRecord{Kind: "server", Provider: "server", AccountID: "daemon", ResourceID: "server:daemon", Resources: cloudinfra.CapacityResources{Servers: inventoryNumber(1), CPUMillis: inventoryNumber(1000)}, Observation: &cloudinfra.CapacityObservation{CPUMillis: inventoryNumber(1000), CheckedAt: now}, CheckedAt: now}
	saved, err := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", saved, []cloudinfra.CapacityRecord{record}, nil); err != nil {
		t.Fatal(err)
	}
	// Starting work never removes the previously committed inventory.
	older, err := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.store.ListCapacityInventory(ctx, "org", "cloud")
	if err != nil || len(stored) != 1 {
		t.Fatalf("issuance erased committed facts: %+v %v", stored, err)
	}
	invalid := record
	invalid.Kind = "invalid"
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", failed, []cloudinfra.CapacityRecord{invalid}, nil); !errors.Is(err, cloudinfra.ErrInvalid) {
		t.Fatalf("invalid snapshot: %v", err)
	}
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", older, nil, nil); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("superseded refresh accepted after failure: %v", err)
	}
	stored, err = f.store.ListCapacityInventory(ctx, "org", "cloud")
	if err != nil || len(stored) != 1 || stored[0].Observation == nil || *stored[0].Observation.CPUMillis != 1000 || !stored[0].Observation.CheckedAt.Equal(now) {
		t.Fatalf("failed refresh lost committed facts: %+v %v", stored, err)
	}
	// Failed saves roll back generation consumption, so the current refresh can retry.
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", failed, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", failed, []cloudinfra.CapacityRecord{record}, nil); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("consumed generation reused: %v", err)
	}
	stored, err = f.store.ListCapacityInventory(ctx, "org", "cloud")
	if err != nil || len(stored) != 0 {
		t.Fatalf("empty snapshot resurrected: %+v %v", stored, err)
	}
}

func TestCapacityInventoryRejectsSupersededHostState(t *testing.T) {
	f := newInfraFixture(t)
	ctx := t.Context()
	oldTime := time.Now().UTC().Add(-time.Minute)
	host := &cloudinfra.Server{ID: "docker-host", Revision: 1, OrgID: "org", CloudID: "cloud", Provider: "server", ConnectionID: "infra-account", AccountID: "daemon", ResourceID: "daemon", Ownership: "adopted", Status: "ready", Health: cloudinfra.RuntimeHealth{DockerID: "daemon", EffectiveCPUMillis: 2000, EffectiveMemoryBytes: 1 << 30, CheckedAt: oldTime}}
	if err := f.store.SaveServer(ctx, host); err != nil {
		t.Fatal(err)
	}
	generation, err := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	record := cloudinfra.CapacityRecord{Kind: "server", Provider: "server", AccountID: "daemon", ResourceID: "server:daemon", ServerID: host.ID, ServerStateRevision: host.StateRevision, Resources: cloudinfra.CapacityResources{Servers: inventoryNumber(1), CPUMillis: inventoryNumber(2000)}, Observation: &cloudinfra.CapacityObservation{CPUMillis: inventoryNumber(2000), CheckedAt: oldTime}, CheckedAt: oldTime}
	// A separate pinned runtime check supersedes the sample without issuing a
	// Cloud refresh generation. This observation must not restore its old state.
	host.Health.EffectiveCPUMillis = 1000
	host.Health.CheckedAt = oldTime.Add(time.Second)
	if err = f.store.SaveServer(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", generation, []cloudinfra.CapacityRecord{record}, nil); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("superseded host revision accepted: %v", err)
	}
	stored, err := f.store.ListCapacityInventory(ctx, "org", "cloud")
	if err != nil || len(stored) != 0 {
		t.Fatalf("stale host created inventory: %+v %v", stored, err)
	}
	record.ServerStateRevision = host.StateRevision
	record.Observation = &cloudinfra.CapacityObservation{CPUMillis: inventoryNumber(1000), CheckedAt: host.Health.CheckedAt}
	record.CheckedAt = host.Health.CheckedAt
	record.Resources.CPUMillis = inventoryNumber(1000)
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", generation, []cloudinfra.CapacityRecord{record}, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = f.store.ListCapacityInventory(ctx, "org", "cloud")
	if err != nil || len(stored) != 1 || stored[0].ServerStateRevision != host.StateRevision || !stored[0].Observation.CheckedAt.Equal(host.Health.CheckedAt) {
		t.Fatalf("independent current host state lost: %+v %v", stored, err)
	}
}
