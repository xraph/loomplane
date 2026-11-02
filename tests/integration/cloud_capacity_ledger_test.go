//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

type ledgerProvider struct{ *infraProvider }

func (p *ledgerProvider) ServerOptions(context.Context, provider.InfrastructureCredential) (provider.ServerOptions, error) {
	return provider.ServerOptions{Regions: []provider.ServerRegion{{ID: "region"}}, Images: []provider.ServerImage{{ID: "ubuntu", Bootstrap: true, Regions: []string{"region"}}}, Sizes: []provider.ServerSize{{ID: "small", CPUs: 2, MemoryMiB: 2048, DiskGB: 20, Regions: []string{"region"}}, {ID: "large", CPUs: 3, MemoryMiB: 2048, DiskGB: 20, Regions: []string{"region"}}}}, nil
}
func ledgerFixture(t *testing.T) *infraFixture {
	t.Helper()
	f := newInfraFixture(t)
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", &ledgerProvider{f.provider}); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	policy := &cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Limits: cloudinfra.ResourceVector{Servers: 10, CPUMillis: 2000, MemoryBytes: 20 << 30, StorageBytes: 200_000_000_000}}
	if err := f.store.SaveCapacityPolicy(t.Context(), policy, 0); err != nil {
		t.Fatal(err)
	}
	gen, err := f.store.BeginCapacityRefresh(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	zero := cloudinfra.CapacityResources{Servers: inventoryNumber(0), CPUMillis: inventoryNumber(0), MemoryBytes: inventoryNumber(0), StorageBytes: inventoryNumber(0)}
	if err = f.store.SaveCapacityInventory(t.Context(), "org", "cloud", gen, nil, []cloudinfra.CapacityAccountSnapshot{{Provider: "digitalocean", AccountID: "account", Provisioned: zero, CheckedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	return f
}
func ledgerPreview(t *testing.T, f *infraFixture) *cloudinfra.Operation {
	t.Helper()
	o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "host", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	o.Status = "pending"
	return o
}
func TestCapacityLedgerConcurrentCreates(t *testing.T) {
	f := ledgerFixture(t)
	a, b := ledgerPreview(t, f), ledgerPreview(t, f)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, o := range []*cloudinfra.Operation{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results <- f.store.Accept(t.Context(), o) }()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent 2-CPU creates with only 2 CPU headroom; want 1", accepted)
	}
}

func ledgerDB(t *testing.T, f *infraFixture) *pgdriver.PgDB {
	t.Helper()
	db := pgdriver.New()
	if err := db.Open(t.Context(), f.sql.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func ledgerReserve(t *testing.T, f *infraFixture, o *cloudinfra.Operation, delta cloudinfra.ResourceVector) (*cloudinfra.CapacityBlock, error) {
	t.Helper()
	db := ledgerDB(t, f)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	b, err := f.store.ReserveInfrastructureCapacity(t.Context(), tx, o.ID, cloudinfra.CapacityDemand{OrgID: o.OrgID, CloudID: o.CloudID}, delta)
	if err != nil || b != nil {
		return b, err
	}
	return nil, tx.Commit()
}
func TestCapacityLedgerRetryAndCleanupEvidence(t *testing.T) {
	f := ledgerFixture(t)
	o := ledgerPreview(t, f)
	if err := f.store.Accept(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if b, err := ledgerReserve(t, f, o, *o.CapacityTarget); err != nil || b != nil {
		t.Fatalf("retry: %+v %v", b, err)
	}
	var count int
	if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cloud_capacity_reservations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry count %d: %v", count, err)
	}
	o.Status = "cancelled"
	if err := f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	tx, err := ledgerDB(t, f).BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = f.store.ReleaseInfrastructureCapacity(t.Context(), tx, "org", o.ID)
	_ = tx.Rollback()
	if !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("status-only release: %v", err)
	}
	if err = f.store.Accept(t.Context(), ledgerPreview(t, f)); err == nil {
		t.Fatal("cancelled status concealed reserved capacity")
	}
}
func ledgerHost(t *testing.T, f *infraFixture, id string, cpu int64) (*cloudinfra.Server, cloudinfra.CapacityRecord) {
	t.Helper()
	v := &cloudinfra.Server{ID: id, OrgID: "org", CloudID: "cloud", ConnectionID: "infra-account", Provider: "digitalocean", AccountID: "account", ResourceID: id, Ownership: "managed", Status: "ready", Revision: 1, Size: "old"}
	if err := f.store.SaveServer(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	r := cloudinfra.CapacityRecord{Kind: "server", Provider: v.Provider, AccountID: v.AccountID, ResourceID: "server:" + id, ServerID: id, ServerStateRevision: v.StateRevision, CheckedAt: time.Now(), Resources: cloudinfra.CapacityResources{Servers: inventoryNumber(1), CPUMillis: inventoryNumber(cpu), MemoryBytes: inventoryNumber(1 << 30), StorageBytes: inventoryNumber(10_000_000_000)}}
	return v, r
}
func ledgerSave(t *testing.T, f *infraFixture, records []cloudinfra.CapacityRecord, accounts []cloudinfra.CapacityAccountSnapshot) {
	t.Helper()
	g, err := f.store.BeginCapacityRefresh(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveCapacityInventory(t.Context(), "org", "cloud", g, records, accounts); err != nil {
		t.Fatal(err)
	}
}
func TestCapacityLedgerResizeAndLoweredCeiling(t *testing.T) {
	f := ledgerFixture(t)
	v, r := ledgerHost(t, f, "resize-host", 1000)
	ledgerSave(t, f, []cloudinfra.CapacityRecord{r}, nil)
	// The provider fixture has no remote server until explicitly seeded.
	f.provider.remote = provider.InfrastructureServer{ID: v.ResourceID, AccountID: "account", Size: "old"}
	o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "resize", ServerID: v.ID, ServerRevision: 1, CloudRevision: 1, Size: "small"})
	if err != nil {
		t.Fatal(err)
	}
	o.Status = "pending"
	if err = f.store.Accept(t.Context(), o); err != nil {
		t.Fatalf("positive resize delta rejected: %v", err)
	}
	var cpu int64
	if err = f.sql.QueryRow(t.Context(), `SELECT cpu_millis FROM loomplane_cloud_capacity_reservations WHERE id=$1`, o.ID).Scan(&cpu); err != nil || cpu != 1000 {
		t.Fatalf("resize reservation=%d err=%v", cpu, err)
	}
	policy, err := f.store.GetCapacityPolicy(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	policy.Revision++
	policy.Limits.CPUMillis = 500
	if err = f.store.SaveCapacityPolicy(t.Context(), policy, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.GetServer(t.Context(), "org", "cloud", v.ID); err != nil {
		t.Fatal("lowered ceiling removed existing host")
	}
	if err = f.store.Accept(t.Context(), ledgerPreview(t, f)); err == nil {
		t.Fatal("manual create bypassed lowered disabled policy")
	}
}
func TestCapacityLedgerTransferAndRetainedVolume(t *testing.T) {
	f := ledgerFixture(t)
	o := ledgerPreview(t, f)
	if err := f.store.Accept(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	o.ResourceID = "materialized"
	o.Attempted = true
	if err := f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	v, r := ledgerHost(t, f, o.ResourceID, 2000)
	v.ID = o.ServerID
	// Match the accepted server identity; remove only this test's unused fixture row.
	if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_cloud_servers WHERE id=$1`, o.ResourceID); err != nil {
		t.Fatal(err)
	}
	v.StateRevision = 0
	if err := f.store.SaveServer(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	r.ServerID = v.ID
	r.ServerStateRevision = v.StateRevision
	r.Resources.MemoryBytes = inventoryNumber(2 << 30)
	r.Resources.StorageBytes = inventoryNumber(20_000_000_000)
	r.CheckedAt = time.Now()
	volume := cloudinfra.CapacityRecord{Kind: "volume", Provider: "digitalocean", AccountID: "account", ResourceID: "volume:materialized", CheckedAt: time.Now(), Resources: cloudinfra.CapacityResources{Servers: inventoryNumber(0), CPUMillis: inventoryNumber(0), MemoryBytes: inventoryNumber(0), StorageBytes: inventoryNumber(5_000_000_000)}}
	undersized := r
	undersized.Resources.CPUMillis = inventoryNumber(1000)
	ledgerSave(t, f, []cloudinfra.CapacityRecord{undersized, r, volume}, nil)
	var duplicateStatus string
	if err := f.sql.QueryRow(t.Context(), `SELECT status FROM loomplane_cloud_capacity_reservations WHERE id=$1`, o.ID).Scan(&duplicateStatus); err != nil || duplicateStatus != "reserved" {
		t.Fatalf("duplicate record transferred an unpersisted allocation: %s %v", duplicateStatus, err)
	}
	ledgerSave(t, f, []cloudinfra.CapacityRecord{r, volume}, nil)
	var status string
	if err := f.sql.QueryRow(t.Context(), `SELECT status FROM loomplane_cloud_capacity_reservations WHERE id=$1`, o.ID).Scan(&status); err != nil || status != "materialized" {
		t.Fatalf("transfer: %s %v", status, err)
	}
	if b, err := ledgerReserve(t, f, o, *o.CapacityTarget); err != nil || b != nil {
		t.Fatalf("materialized retry: %+v %v", b, err)
	}
	o.Status = "succeeded"
	if err := f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	// A recorded exact server absence frees the server, never an equal native volume ID.
	deletion := *o
	deletion.ID = "delete-receipt"
	deletion.Input.Kind = "delete"
	deletion.Status = "preview"
	deletion.StateRevision = 0
	deletion.Input.ServerRevision = v.Revision
	if err := f.store.CreatePreview(t.Context(), &deletion); err != nil {
		t.Fatal(err)
	}
	deletion.Status = "pending"
	deletion.Input.ServerRevision = v.Revision
	if err := f.store.Accept(t.Context(), &deletion); err != nil {
		t.Fatal(err)
	}
	staleGeneration, err := f.store.BeginCapacityRefresh(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	deletion.AbsenceConfirmedAt = time.Now()
	if saveErr := f.store.SaveOperation(t.Context(), &deletion); saveErr != nil {
		t.Fatal(saveErr)
	}
	if err = f.store.SaveCapacityInventory(t.Context(), "org", "cloud", staleGeneration, []cloudinfra.CapacityRecord{r, volume}, nil); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("pre-deletion collection resurrected server: %v", err)
	}
	if b, retryErr := ledgerReserve(t, f, o, *o.CapacityTarget); !errors.Is(retryErr, cloudinfra.ErrConflict) {
		t.Fatalf("released operation reused reservation: %+v %v", b, retryErr)
	}
	records, err := f.store.ListCapacityInventory(t.Context(), "org", "cloud")
	if err != nil || len(records) != 1 || records[0].Kind != "volume" {
		t.Fatalf("cleanup lost retained volume: %+v %v", records, err)
	}
	usage, err := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
	if err != nil || !usage.Fresh || usage.Provisioned.CPUMillis == nil || *usage.Provisioned.CPUMillis != 0 || usage.Headroom.CPUMillis == nil || *usage.Headroom.CPUMillis != 2000 {
		t.Fatalf("authoritative absence left unknown server footprint: %+v %v", usage, err)
	}
	tx, err := ledgerDB(t, f).BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = f.store.ReleaseInfrastructureCapacity(t.Context(), tx, "org", o.ID); err != nil {
		t.Fatal(err)
	}
}
func TestCapacityLedgerRejectsUnknownAndSupersededInventory(t *testing.T) {
	for _, mode := range []string{"unknown", "superseded", "stale"} {
		t.Run(mode, func(t *testing.T) {
			f := ledgerFixture(t)
			v, r := ledgerHost(t, f, "existing", 0)
			r.Observation = &cloudinfra.CapacityObservation{CheckedAt: time.Now()}
			if mode == "unknown" {
				r.Resources.StorageBytes = nil
			}
			if mode == "stale" {
				r.CheckedAt = time.Now().Add(-10 * time.Minute)
			}
			ledgerSave(t, f, []cloudinfra.CapacityRecord{r}, nil)
			if mode == "superseded" {
				v.Status = "unhealthy"
				if err := f.store.SaveServer(t.Context(), v); err != nil {
					t.Fatal(err)
				}
			}
			err := f.store.Accept(t.Context(), ledgerPreview(t, f))
			var blocked *cloudinfra.CapacityAdmissionError
			if !errors.As(err, &blocked) || blocked.Block.Reason != "stale_inventory" {
				t.Fatalf("%s inventory accepted: %v", mode, err)
			}
		})
	}
}

func TestCapacityLedgerSharedAccountQuota(t *testing.T) {
	f := ledgerFixture(t)
	ctx := t.Context()
	db := ledgerDB(t, f)
	if err := cloudprofiles.NewPgStore(db).CreateProfile(ctx, &cloudprofiles.Profile{ID: "other-cloud", OrgID: "org", ProjectID: "proj", Name: "Other", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	p, err := f.store.GetCapacityPolicy(ctx, "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	p.CloudID = "other-cloud"
	if err = f.store.SaveCapacityPolicy(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	account, err := f.store.GetCapacityAccount(ctx, "org", "digitalocean", "account")
	if err != nil {
		t.Fatal(err)
	}
	account.Quota.Servers = inventoryNumber(1)
	account.CheckedAt = time.Now()
	ledgerSave(t, f, nil, []cloudinfra.CapacityAccountSnapshot{*account})
	a := ledgerPreview(t, f)
	b, err := f.svc.PreviewServer(ctx, f.p, "other-cloud", a.Input)
	if err != nil {
		t.Fatal(err)
	}
	b.Status = "pending"
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, o := range []*cloudinfra.Operation{a, b} {
		go func() { <-start; results <- f.store.Accept(ctx, o) }()
	}
	close(start)
	accepted, quota := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			accepted++
			continue
		}
		var block *cloudinfra.CapacityAdmissionError
		if errors.As(err, &block) && block.Block.Reason == "provider_quota" {
			quota++
		} else {
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
	if accepted != 1 || quota != 1 {
		t.Fatalf("shared quota: accepted=%d blocked=%d", accepted, quota)
	}
}
func TestCapacityLedgerPoolEditsAndExistingWorkload(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	// Preserve just one member before imposing a one-server pool ceiling.
	f.pool.Members = f.pool.Members[:1]
	f.pool.Revision = 2
	if err := f.store.SavePool(ctx, f.pool, 1); err != nil {
		t.Fatal(err)
	}
	policy := &cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Limits: cloudinfra.ResourceVector{Servers: 2, CPUMillis: 4000, MemoryBytes: 4 << 30, StorageBytes: 40_000_000_000}, Pools: []cloudinfra.PoolExpansionPolicy{{PoolID: "pool", MaxServers: 1}}}
	if err := f.store.SaveCapacityPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	records := make([]cloudinfra.CapacityRecord, 0, 2)
	for _, id := range []string{"server-1", "server-2"} {
		v, err := f.store.GetServer(ctx, "org", "cloud", id)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, cloudinfra.CapacityRecord{Kind: "server", Provider: v.Provider, AccountID: v.AccountID, ResourceID: "server:" + id, ServerID: id, ServerStateRevision: v.StateRevision, CheckedAt: time.Now(), Resources: cloudinfra.CapacityResources{Servers: inventoryNumber(1), CPUMillis: inventoryNumber(2000), MemoryBytes: inventoryNumber(2 << 30), StorageBytes: inventoryNumber(20_000_000_000)}})
	}
	generation, err := f.store.BeginCapacityRefresh(ctx, "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveCapacityInventory(ctx, "org", "cloud", generation, records, nil); err != nil {
		t.Fatal(err)
	}
	edited := *f.pool
	edited.Revision = 3
	edited.Members = append(append([]cloudinfra.PoolMember{}, f.pool.Members...), cloudinfra.PoolMember{ServerID: "server-2", CPUMillis: 2000, MemoryBytes: 2 << 30})
	var blocked *cloudinfra.CapacityAdmissionError
	if err = f.store.SavePool(ctx, &edited, 2); !errors.As(err, &blocked) || blocked.Block.Reason != "hard_limit" {
		t.Fatalf("pool edit bypassed limit: %v", err)
	}
	placement, err := f.store.PreviewPlacement(ctx, f.request("env", "web"))
	if err != nil {
		t.Fatalf("existing capacity blocked at Cloud ceiling: %v", err)
	}
	f.plan(t, "ledger-existing-workload", f.request("env", "web"), placement)
	f.accept(t, "ledger-existing-workload")
}

func TestCapacityLedgerDisconnectRetainsFootprint(t *testing.T) {
	f := ledgerFixture(t)
	v, r := ledgerHost(t, f, "retained", 2000)
	ledgerSave(t, f, []cloudinfra.CapacityRecord{r}, nil)
	v.Status = "disconnected"
	if err := f.store.SaveServer(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	ledgerSave(t, f, nil, nil)
	records, err := f.store.ListCapacityInventory(t.Context(), "org", "cloud")
	if err != nil || len(records) != 1 || *records[0].Resources.CPUMillis != 2000 {
		t.Fatalf("disconnect concealed footprint: %+v %v", records, err)
	}
	if err = f.store.Accept(t.Context(), ledgerPreview(t, f)); err == nil {
		t.Fatal("disconnect freed capacity without authoritative absence")
	}
}
func TestCapacityLedgerUsageProjection(t *testing.T) {
	f := ledgerFixture(t)
	o := ledgerPreview(t, f)
	if err := f.store.Accept(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	usage, err := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Reserved.CPUMillis == nil || *usage.Reserved.CPUMillis != 2000 || usage.Headroom.CPUMillis == nil || *usage.Headroom.CPUMillis != 0 || usage.Pending.CPUMillis == nil || *usage.Pending.CPUMillis != 0 {
		t.Fatalf("reserved projection: %+v", usage)
	}
	o.Attempted = true
	if err = f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	usage, err = f.store.GetCapacityUsage(t.Context(), "org", "cloud")
	if err != nil || usage.Pending.CPUMillis == nil || *usage.Pending.CPUMillis != 2000 {
		t.Fatalf("pending projection: %+v %v", usage, err)
	}
}

type ledgerRuntime struct{}

func (ledgerRuntime) Inspect(context.Context, []byte, map[string]string, bool) (cloudinfra.RuntimeHealth, error) {
	return cloudinfra.RuntimeHealth{DockerID: "ledger-adopt", Architecture: "amd64", EffectiveCPUMillis: 2000, EffectiveMemoryBytes: 2 << 30, StorageBytes: inventoryNumber(20_000_000_000), CheckedAt: time.Now()}, nil
}
func TestCapacityLedgerManualAdoptionCannotBypassLimits(t *testing.T) {
	f := ledgerFixture(t)
	ctx := t.Context()
	f.svc.Runtime = ledgerRuntime{}
	conn := &connections.Connection{ID: "adopt", OrgID: "org", Cloud: "server", Name: "Pinned", Account: "ledger-adopt", Status: "ready", Details: map[string]string{"host_key": "fixture"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.connections.CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := f.secrets.Put(ctx, "org", conn.ID, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	o := ledgerPreview(t, f)
	if err := f.store.Accept(ctx, o); err != nil {
		t.Fatal(err)
	}
	adoption, err := f.svc.PreviewServer(ctx, f.p, "cloud", cloudinfra.ServerInput{Kind: "adopt", Name: "Adopt", ConnectionID: conn.ID, CloudRevision: 1, ConfirmAdoption: true})
	if err != nil {
		t.Fatal(err)
	}
	adoption.Status = "pending"
	var blocked *cloudinfra.CapacityAdmissionError
	if err = f.store.Accept(ctx, adoption); !errors.As(err, &blocked) || blocked.Block.Reason != "stale_inventory" || blocked.Block.Resource != "storage_bytes" {
		t.Fatalf("observed filesystem was treated as provisioned storage; adoption bypass: %v", err)
	}
	if _, err = f.store.GetServer(ctx, "org", "cloud", adoption.ServerID); !errors.Is(err, cloudinfra.ErrNotFound) {
		t.Fatalf("blocked adoption claimed host: %v", err)
	}
}

func TestCapacityLedgerMissingInventoryIsUnknownUsage(t *testing.T) {
	f := ledgerFixture(t)
	ledgerHost(t, f, "unobserved", 2000)
	usage, err := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Fresh || usage.Provisioned.CPUMillis != nil || usage.Headroom.CPUMillis != nil {
		t.Fatalf("unobserved host became zero usage: %+v", usage)
	}
}

func TestCapacityLedgerPoolEditCountsPendingResize(t *testing.T) {
	for _, resizeMember := range []string{"current", "new", "pool-and-member"} {
		t.Run(resizeMember, func(t *testing.T) {
			f := ledgerFixture(t)
			ctx := t.Context()
			records := make([]cloudinfra.CapacityRecord, 0, 2)
			hosts := make([]*cloudinfra.Server, 0, 2)
			for _, id := range []string{"current", "new"} {
				v, r := ledgerHost(t, f, id, 1000)
				v.RuntimeConnectionID = "runtime-" + id
				conn := &connections.Connection{ID: v.RuntimeConnectionID, OrgID: "org", Cloud: "server", Name: v.RuntimeConnectionID, Account: v.RuntimeConnectionID, Status: "ready", Details: map[string]string{"docker_id": v.RuntimeConnectionID}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if err := f.connections.CreateConnection(ctx, conn); err != nil {
					t.Fatal(err)
				}
				v.Health = cloudinfra.RuntimeHealth{DockerID: v.RuntimeConnectionID, EffectiveCPUMillis: 1000, EffectiveMemoryBytes: 1 << 30, CheckedAt: time.Now()}
				if err := f.store.SaveServer(ctx, v); err != nil {
					t.Fatal(err)
				}
				r.ServerStateRevision = v.StateRevision
				records = append(records, r)
				hosts = append(hosts, v)
			}
			pool := &cloudinfra.Pool{ID: "resize-pool", OrgID: "org", CloudID: "cloud", Revision: 1, Members: []cloudinfra.PoolMember{{ServerID: "current", CPUMillis: 1000, MemoryBytes: 1 << 30}}}
			if err := f.store.SavePool(ctx, pool, 0); err != nil {
				t.Fatal(err)
			}
			policy, err := f.store.GetCapacityPolicy(ctx, "org", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			policy.Revision++
			policy.Limits.CPUMillis = 4000
			poolLimit := policy.Limits
			poolLimit.CPUMillis = 3000
			policy.Pools = []cloudinfra.PoolExpansionPolicy{{PoolID: pool.ID, MaxServers: 2, Limits: &poolLimit}}
			if err = f.store.SaveCapacityPolicy(ctx, policy, 1); err != nil {
				t.Fatal(err)
			}
			ledgerSave(t, f, records, nil)
			v := hosts[0]
			if resizeMember == "new" {
				v = hosts[1]
			}
			f.provider.remote = provider.InfrastructureServer{ID: v.ResourceID, AccountID: "account", Size: "old"}
			o, err := f.svc.PreviewServer(ctx, f.p, "cloud", cloudinfra.ServerInput{Kind: "resize", ServerID: v.ID, ServerRevision: v.Revision, CloudRevision: 1, Size: "large"})
			if err != nil {
				t.Fatal(err)
			}
			if resizeMember == "pool-and-member" {
				tx, beginErr := ledgerDB(t, f).BeginTx(ctx, nil)
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				defer func() { _ = tx.Rollback() }()
				block, reserveErr := f.store.ReserveInfrastructureCapacity(ctx, tx, o.ID, cloudinfra.CapacityDemand{OrgID: "org", CloudID: "cloud", PoolID: pool.ID}, cloudinfra.ResourceVector{CPUMillis: 2000, MemoryBytes: 1 << 30, StorageBytes: 10_000_000_000})
				if reserveErr != nil || block != nil {
					t.Fatalf("pool reservation: %+v %v", block, reserveErr)
				}
				if commitErr := tx.Commit(); commitErr != nil {
					t.Fatal(commitErr)
				}
			}
			o.Status = "pending"
			if err = f.store.Accept(ctx, o); err != nil {
				t.Fatal(err)
			}
			pool.Revision++
			pool.Members = append(pool.Members, cloudinfra.PoolMember{ServerID: "new", CPUMillis: 1000, MemoryBytes: 1 << 30})
			var blocked *cloudinfra.CapacityAdmissionError
			if err = f.store.SavePool(ctx, pool, 1); !errors.As(err, &blocked) || blocked.Block.Reason != "hard_limit" || blocked.Block.Resource != "cpu_millis" {
				t.Fatalf("pool edit ignored pending resize on %s member: %v", resizeMember, err)
			}
			policy.Revision++
			policy.Pools[0].Limits.CPUMillis = 4000
			if err = f.store.SaveCapacityPolicy(ctx, policy, 2); err != nil {
				t.Fatal(err)
			}
			if err = f.store.SavePool(ctx, pool, 1); err != nil {
				t.Fatalf("exact 4-CPU limit double-counted pending resize: %v", err)
			}
		})
	}
}

func TestCapacityLedgerLegacyDisconnectKeepsUnknownFootprint(t *testing.T) {
	f := newInfraFixture(t)
	ctx := t.Context()
	f.svc.Runtime = ledgerRuntime{}
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", &ledgerProvider{f.provider}); err != nil {
		t.Fatal(err)
	}
	f.svc.Adapters = adapters
	conn := &connections.Connection{ID: "legacy-adopt", OrgID: "org", Cloud: "server", Name: "Legacy", Account: "ledger-adopt", Status: "ready", Details: map[string]string{"host_key": "fixture"}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.connections.CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := f.secrets.Put(ctx, "org", conn.ID, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	o, err := f.svc.PreviewServer(ctx, f.p, "cloud", cloudinfra.ServerInput{Kind: "adopt", Name: "Legacy", ConnectionID: conn.ID, CloudRevision: 1, ConfirmAdoption: true})
	if err != nil {
		t.Fatal(err)
	}
	o, err = f.svc.ApplyServerOperation(ctx, f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.reconcile(t, o); got.Status != "succeeded" {
		t.Fatalf("legacy adoption: %s", got.Status)
	}
	policy := &cloudinfra.CapacityPolicy{OrgID: "org", CloudID: "cloud", Revision: 1, Limits: cloudinfra.ResourceVector{Servers: 10, CPUMillis: 2000, MemoryBytes: 20 << 30, StorageBytes: 200_000_000_000}}
	if err = f.store.SaveCapacityPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	zero := cloudinfra.CapacityResources{Servers: inventoryNumber(0), CPUMillis: inventoryNumber(0), MemoryBytes: inventoryNumber(0), StorageBytes: inventoryNumber(0)}
	ledgerSave(t, f, nil, []cloudinfra.CapacityAccountSnapshot{{Provider: "digitalocean", AccountID: "account", Provisioned: zero, CheckedAt: time.Now()}})
	assertUnknown := func(t *testing.T) {
		t.Helper()
		usage, usageErr := f.store.GetCapacityUsage(ctx, "org", "cloud")
		if usageErr != nil {
			t.Fatal(usageErr)
		}
		if usage.Fresh || usage.Provisioned.CPUMillis != nil || usage.Headroom.CPUMillis != nil {
			t.Errorf("never-sampled host became zero usage: %+v", usage)
		}
		var blocked *cloudinfra.CapacityAdmissionError
		if acceptErr := f.store.Accept(ctx, ledgerPreview(t, f)); !errors.As(acceptErr, &blocked) || blocked.Block.Reason != "stale_inventory" {
			t.Errorf("never-sampled host allowed infrastructure admission: %v", acceptErr)
		}
	}
	assertUnknown(t)
	v, err := f.store.GetServer(ctx, "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	disconnect, err := f.svc.PreviewServer(ctx, f.p, "cloud", cloudinfra.ServerInput{Kind: "disconnect", ServerID: v.ID, ServerRevision: v.Revision, CloudRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	disconnect, err = f.svc.ApplyServerOperation(ctx, f.p, "cloud", disconnect.ID, cloudinfra.ApplyInput{Review: disconnect.Review})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.reconcile(t, disconnect); got.Status != "succeeded" {
		t.Fatalf("disconnect: %s", got.Status)
	}
	if _, err = f.svc.RefreshCapacity(ctx, f.p, "cloud"); err != nil {
		t.Fatal(err)
	}
	assertUnknown(t)
}

func TestCapacityLedgerRetiredStatusDoesNotProveAbsence(t *testing.T) {
	for _, status := range []string{"disconnected", "deleted"} {
		t.Run(status, func(t *testing.T) {
			f := ledgerFixture(t)
			v, _ := ledgerHost(t, f, "unknown-retired", 2000)
			v.Status = status
			if err := f.store.SaveServer(t.Context(), v); err != nil {
				t.Fatal(err)
			}
			ledgerSave(t, f, nil, nil)
			usage, err := f.store.GetCapacityUsage(t.Context(), "org", "cloud")
			if err != nil || usage.Fresh || usage.Provisioned.CPUMillis != nil || usage.Headroom.CPUMillis != nil {
				t.Fatalf("status %s concealed unknown footprint: %+v %v", status, usage, err)
			}
		})
	}
}
