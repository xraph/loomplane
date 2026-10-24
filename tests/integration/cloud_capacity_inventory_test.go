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
	if e = f.store.SaveCapacityInventory(ctx, "org", "cloud", stored, []cloudinfra.CapacityAccountSnapshot{*account}); e != nil {
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
