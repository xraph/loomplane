package cloudinfra

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

const CapacityFreshness = 5 * time.Minute

// CapacityRecord identifies one attributed physical resource. Pool membership
// does not create a second record. ResourceID includes the provider kind.
type CapacityRecord struct {
	Kind                string               `json:"kind"`
	Provider            string               `json:"provider"`
	AccountID           string               `json:"account_id"`
	ResourceID          string               `json:"resource_id"`
	ServerID            string               `json:"server_id,omitempty"`
	ServerStateRevision int64                `json:"server_state_revision"`
	Resources           CapacityResources    `json:"resources"`
	Observation         *CapacityObservation `json:"observation,omitempty"`
	CheckedAt           time.Time            `json:"checked_at"`
	// GPUKind and GPUs count a server's GPUs. A nil GPUs is a count
	// Loomplane couldn't read, which is never taken as zero.
	GPUKind string `json:"gpu_kind,omitempty"`
	GPUs    *int64 `json:"gpus,omitempty"`
}
type CapacityInventoryStore interface {
	BeginCapacityRefresh(context.Context, string, string) (int64, error)
	ListCapacityInventory(context.Context, string, string) ([]CapacityRecord, error)
	SaveCapacityInventory(context.Context, string, string, int64, []CapacityRecord, []CapacityAccountSnapshot) error
	GetCapacityAccount(context.Context, string, string, string) (*CapacityAccountSnapshot, error)
}
type CapacityScope struct{ OrgID, CloudID string }
type capacityRecoveryStore interface {
	CapacityScopes(context.Context) ([]CapacityScope, error)
}

func (s *Service) RefreshCapacity(ctx context.Context, p security.Principal, cloudID string) (*CapacityUsage, error) {
	if _, err := s.authorize(ctx, p, cloudID); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	return s.refreshCapacity(ctx, p.OrgID, cloudID)
}
func (s *Service) refreshCapacity(ctx context.Context, org, cloud string) (*CapacityUsage, error) {
	st, ok := s.Store.(CapacityInventoryStore)
	if !ok {
		return nil, ErrInvalid
	}
	ps, ok := s.Store.(CapacityPolicyStore)
	if !ok {
		return nil, ErrInvalid
	}
	pools, ok := s.Store.(PoolStore)
	if !ok {
		return nil, ErrInvalid
	}
	generation, err := st.BeginCapacityRefresh(ctx, org, cloud)
	if err != nil {
		return nil, err
	}
	policy, err := ps.GetCapacityPolicy(ctx, org, cloud)
	if err != nil {
		return nil, err
	}
	members, err := pools.ListPools(ctx, org, cloud)
	if err != nil {
		return nil, err
	}
	servers, err := s.Store.ListServers(ctx, org, cloud)
	if err != nil {
		return nil, err
	}
	prior, err := st.ListCapacityInventory(ctx, org, cloud)
	if err != nil {
		return nil, err
	}
	// Scope reads to connections already bound to this Cloud's servers or policy.
	connectionIDs := map[string]bool{}
	for _, v := range servers {
		if v.Provider != "server" && v.ConnectionID != "" {
			connectionIDs[v.ConnectionID] = true
		}
	}
	for _, pool := range policy.Pools {
		if pool.ConnectionID != "" {
			connectionIDs[pool.ConnectionID] = true
		}
	}
	accounts := map[string]provider.CapacityInventory{}
	catalogs := map[string]provider.ServerOptions{}
	// Only an account holding a managed server without a current price has its
	// catalog read, so a Cloud whose servers are priced makes no catalog calls.
	unpriced := map[string]bool{}
	for _, v := range servers {
		if needsPrice(v) {
			unpriced[v.Provider+"/"+v.AccountID] = true
		}
	}
	ids := make([]string, 0, len(connectionIDs))
	for id := range connectionIDs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		conn, raw, e := s.Connections.Credential(ctx, org, id)
		if e != nil {
			return nil, e
		}
		credential, e := Credential(conn, raw)
		if e != nil {
			return nil, e
		}
		key := credential.Provider + "/" + credential.AccountID
		if _, exists := accounts[key]; exists {
			continue
		}
		adapter, e := s.Adapters.Server(conn.Cloud, provider.ServerDiscovery)
		if e != nil {
			return nil, e
		}
		inventory, available := adapter.(provider.CapacityInventoryAdapter)
		if !available {
			return nil, provider.ErrInfrastructureUnsupported
		}
		snapshot, e := inventory.CapacityInventory(ctx, credential)
		if e != nil {
			return nil, e
		}
		if snapshot.Provider != credential.Provider || snapshot.AccountID != credential.AccountID {
			return nil, provider.ErrInfrastructureIdentity
		}
		accounts[key] = snapshot
		if unpriced[key] {
			if options, ok := s.catalog(ctx, key, adapter, credential); ok {
				catalogs[key] = options
			}
		}
	}
	records := make([]CapacityRecord, 0, len(servers))
	for _, v := range servers {
		if v.OrgID != org || v.CloudID != cloud {
			return nil, provider.ErrInfrastructureIdentity
		}
		if v.Status == "deleted" || v.Status == "disconnected" || (v.Provider != "server" && v.ResourceID == "") {
			continue
		}
		// Recheck and persist using the same scoped runtime-health path as CheckServer.
		if v.RuntimeConnectionID != "" && (v.Status == "ready" || v.Status == "unhealthy") {
			checked, e := s.checkServer(ctx, org, cloud, v.ID)
			if e != nil {
				return nil, e
			}
			v = checked
		}
		if options, ok := catalogs[v.Provider+"/"+v.AccountID]; ok && needsPrice(v) && catalogPrice(v, options) {
			// A price is a display estimate. A server that changed under the
			// refresh keeps its stored facts, and the next refresh prices it.
			if e := s.Store.SaveServer(ctx, v); errors.Is(e, ErrConflict) {
				s.warn("Couldn't save a server's catalog price; the next refresh tries again", e)
				current, getErr := s.Store.GetServer(ctx, org, cloud, v.ID)
				if getErr != nil {
					return nil, getErr
				}
				v = current
			} else if e != nil {
				return nil, e
			}
		}
		record := CapacityRecord{Kind: "server", Provider: v.Provider, AccountID: v.AccountID, ResourceID: "server:" + v.ResourceID, ServerID: v.ID, ServerStateRevision: v.StateRevision, CheckedAt: v.Health.CheckedAt, Resources: CapacityResources{Servers: number(1)}, Observation: capacityObservation(v.Health)}
		var created *Operation
		if len(v.Health.GPUs) == 0 && v.Health.GPURuntime != provider.GPUStatusNone && v.CreationOperationID != "" {
			o, e := s.Store.GetOperation(ctx, org, cloud, v.CreationOperationID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return nil, e
			}
			created = o
		}
		record.GPUKind, record.GPUs = serverGPUFacts(v, created)
		if v.Status != "ready" {
			record.Observation = nil
		}
		if v.Provider == "server" {
			record.AccountID = v.Health.DockerID
			record.ResourceID = "server:" + v.Health.DockerID
			if record.AccountID == "" {
				return nil, provider.ErrInfrastructureIdentity
			}
			if record.Observation != nil {
				record.Resources.CPUMillis = record.Observation.CPUMillis
				record.Resources.MemoryBytes = record.Observation.MemoryBytes
			}
		} else {
			snapshot, exists := accounts[v.Provider+"/"+v.AccountID]
			if !exists {
				return nil, provider.ErrInfrastructureIdentity
			}
			record.CheckedAt = snapshot.CheckedAt
			for _, remote := range snapshot.Servers {
				if remote.ID == v.ResourceID {
					record.Resources = CapacityResources{Servers: number(1), CPUMillis: remote.CPUMillis, MemoryBytes: remote.MemoryBytes, StorageBytes: remote.StorageBytes}
					break
				}
			}
		}
		records = append(records, record)
	}
	// A volume stays attributed after detach. A complete provider listing is the
	// only evidence that can remove it from the Cloud's provisioned footprint.
	for _, snapshot := range accounts {
		for _, volume := range snapshot.Volumes {
			attributed := false
			for _, v := range servers {
				if v.Provider == snapshot.Provider && v.AccountID == snapshot.AccountID {
					for _, id := range volume.ServerIDs {
						attributed = attributed || id == v.ResourceID
					}
				}
			}
			for _, old := range prior {
				attributed = attributed || (old.Kind == "volume" && old.Provider == snapshot.Provider && old.AccountID == snapshot.AccountID && old.ResourceID == "volume:"+volume.ID)
			}
			if attributed {
				records = append(records, CapacityRecord{Kind: "volume", Provider: snapshot.Provider, AccountID: snapshot.AccountID, ResourceID: "volume:" + volume.ID, Resources: CapacityResources{Servers: number(0), CPUMillis: number(0), MemoryBytes: number(0), StorageBytes: volume.StorageBytes}, CheckedAt: snapshot.CheckedAt})
			}
		}
	}
	// If an old volume's connection is removed, retain its last facts as stale.
	for _, old := range prior {
		if old.Kind == "volume" {
			if _, refreshed := accounts[old.Provider+"/"+old.AccountID]; !refreshed {
				records = append(records, old)
			}
		}
	}
	now := s.now().UTC()
	usage, err := summarizeCapacity(records, members, *policy, now)
	if err != nil {
		return nil, err
	}
	for _, snapshot := range accounts {
		summary, e := summarizeAccount(snapshot)
		if e != nil {
			return nil, e
		}
		usage.Accounts = append(usage.Accounts, summary)
		usage.Fresh = usage.Fresh && freshCapacity(snapshot.CheckedAt, now)
	}
	slices.SortFunc(usage.Accounts, func(a, b CapacityAccountSnapshot) int {
		if n := cmp.Compare(a.Provider, b.Provider); n != 0 {
			return n
		}
		return cmp.Compare(a.AccountID, b.AccountID)
	})
	if err := st.SaveCapacityInventory(ctx, org, cloud, generation, records, usage.Accounts); err != nil {
		return nil, err
	}
	s.wakeCapacityCloud(ctx, org, cloud, "inventory:"+now.Format(time.RFC3339Nano))
	if ledger, ok := s.Store.(interface {
		GetCapacityUsage(context.Context, string, string) (*CapacityUsage, error)
	}); ok {
		current, e := ledger.GetCapacityUsage(ctx, org, cloud)
		if e != nil {
			return nil, e
		}
		// The refresh also reports accounts reached through configured empty pools.
		if len(current.Accounts) == 0 {
			current.Accounts = usage.Accounts
		}
		return current, nil
	}
	return usage, nil
}

// catalogTTL bounds how often a provider catalog is read for prices. DigitalOcean
// serves it over several paginated requests on the same token as inventory.
const catalogTTL = time.Hour

type catalogEntry struct {
	options provider.ServerOptions
	ok      bool
	at      time.Time
}

// catalogCache keeps one catalog read per account, failed reads included, so
// a rate-limited provider isn't asked again before the TTL runs out.
type catalogCache struct {
	mu      sync.Mutex
	entries map[string]catalogEntry
}

// catalog returns the account's server catalog, read at most once per catalogTTL.
func (s *Service) catalog(ctx context.Context, key string, adapter provider.ServerAdapter, credential provider.InfrastructureCredential) (provider.ServerOptions, bool) {
	reader, ok := adapter.(provider.ServerOptionsAdapter)
	if !ok {
		return provider.ServerOptions{}, false
	}
	now := s.now()
	s.catalogs.mu.Lock()
	entry, cached := s.catalogs.entries[key]
	s.catalogs.mu.Unlock()
	if cached && now.Sub(entry.at) < catalogTTL {
		return entry.options, entry.ok
	}
	options, err := reader.ServerOptions(ctx, credential)
	if ctx.Err() != nil {
		return provider.ServerOptions{}, false
	}
	entry = catalogEntry{options: options, ok: err == nil, at: now}
	if err != nil {
		s.warn("Couldn't read the server catalog for prices; the next read is in an hour", err)
	}
	s.catalogs.mu.Lock()
	s.catalogs.entries[key] = entry
	s.catalogs.mu.Unlock()
	return entry.options, entry.ok
}

// needsPrice reports whether a managed provider server lacks a price for its current size.
func needsPrice(v *Server) bool {
	if v.Ownership != "managed" || v.Provider == "server" || v.Status == "deleted" || v.Status == "disconnected" {
		return false
	}
	return !v.HourlyPriceKnown || v.PricedSize != v.Size
}

// catalogPrice updates a managed server's hourly price and GPUs from the
// catalog row for its size. It reports whether anything changed. A size the
// catalog no longer lists keeps its last known facts.
func catalogPrice(v *Server, options provider.ServerOptions) bool {
	for _, size := range options.Sizes {
		if size.ID != v.Size {
			continue
		}
		if v.PricedSize == size.ID && v.HourlyUSD == size.HourlyUSD && v.HourlyPriceKnown == size.HourlyPriceKnown && reflect.DeepEqual(v.Accelerator, size.Accelerator) {
			return false
		}
		v.HourlyUSD, v.HourlyPriceKnown, v.Accelerator, v.PricedSize = size.HourlyUSD, size.HourlyPriceKnown, cloneAccelerator(size.Accelerator), size.ID
		return true
	}
	return false
}
func (s *Service) recoverCapacity(ctx context.Context) error {
	st, ok := s.Store.(capacityRecoveryStore)
	if !ok {
		return nil
	}
	scopes, err := st.CapacityScopes(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, scope := range scopes {
		_, e := s.refreshCapacity(ctx, scope.OrgID, scope.CloudID)
		result = errors.Join(result, e)
	}
	return result
}
func capacityObservation(h RuntimeHealth) *CapacityObservation {
	if h.CheckedAt.IsZero() {
		return nil
	}
	out := &CapacityObservation{StorageBytes: h.StorageBytes, FreeStorageBytes: h.FreeStorageBytes, CheckedAt: h.CheckedAt}
	if h.EffectiveCPUMillis > 0 {
		out.CPUMillis = number(h.EffectiveCPUMillis)
	}
	if h.EffectiveMemoryBytes > 0 {
		out.MemoryBytes = number(h.EffectiveMemoryBytes)
	}
	return out
}
func freshCapacity(checked, now time.Time) bool {
	return !checked.IsZero() && !checked.After(now) && now.Sub(checked) <= CapacityFreshness
}
func number(n int64) *int64 { return &n }
func capacityResources(v ResourceVector) CapacityResources {
	return CapacityResources{number(v.Servers), number(v.CPUMillis), number(v.MemoryBytes), number(v.StorageBytes)}
}
func sumFact(a, b *int64) (*int64, error) {
	if (a != nil && *a < 0) || (b != nil && *b < 0) {
		return nil, ErrInvalid
	}
	if a == nil || b == nil {
		return nil, nil
	}
	v, e := addCapacity(ResourceVector{CPUMillis: *a}, ResourceVector{CPUMillis: *b})
	if e != nil {
		return nil, e
	}
	return number(v.CPUMillis), nil
}
func sumResources(a, b CapacityResources) (CapacityResources, error) {
	var out CapacityResources
	var err error
	for _, pair := range []struct {
		a, b *int64
		out  **int64
	}{{a.Servers, b.Servers, &out.Servers}, {a.CPUMillis, b.CPUMillis, &out.CPUMillis}, {a.MemoryBytes, b.MemoryBytes, &out.MemoryBytes}, {a.StorageBytes, b.StorageBytes, &out.StorageBytes}} {
		*pair.out, err = sumFact(pair.a, pair.b)
		if err != nil {
			return CapacityResources{}, err
		}
	}
	return out, nil
}
func recordKey(r CapacityRecord) string {
	return r.Provider + "\x00" + r.AccountID + "\x00" + r.ResourceID
}
func inventoryID(r CapacityRecord) string {
	sum := sha256.Sum256([]byte(recordKey(r)))
	return "inventory_" + hex.EncodeToString(sum[:])
}

func summarizeCapacity(records []CapacityRecord, pools []*Pool, policy CapacityPolicy, now time.Time) (*CapacityUsage, error) {
	out := &CapacityUsage{Provisioned: capacityResources(ResourceVector{}), Observed: capacityResources(ResourceVector{}), SystemReserve: capacityResources(ResourceVector{}), Allocatable: capacityResources(ResourceVector{}), CheckedAt: now, Fresh: true}
	seen := map[string]bool{}
	hosts := map[string]bool{}
	for _, r := range records {
		key := recordKey(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		var err error
		out.Provisioned, err = sumResources(out.Provisioned, r.Resources)
		if err != nil {
			return nil, err
		}
		out.Fresh = out.Fresh && freshCapacity(r.CheckedAt, now)
		if r.Kind != "server" {
			continue
		}
		obs := CapacityResources{Servers: number(1)}
		if r.Observation != nil && freshCapacity(r.Observation.CheckedAt, now) {
			obs.CPUMillis = r.Observation.CPUMillis
			obs.MemoryBytes = r.Observation.MemoryBytes
			obs.StorageBytes = r.Observation.StorageBytes
		} else {
			out.Fresh = false
		}
		// Provider aliases for one daemon cannot multiply allocatable host totals.
		if hosts[r.ServerID] {
			continue
		}
		hosts[r.ServerID] = true
		out.Observed, err = sumResources(out.Observed, obs)
		if err != nil {
			return nil, err
		}
		reserve := ResourceVector{}
		budget := ResourceVector{}
		member := false
		for _, pool := range pools {
			for _, m := range pool.Members {
				if m.ServerID == r.ServerID {
					if !member {
						budget = ResourceVector{CPUMillis: m.CPUMillis, MemoryBytes: m.MemoryBytes}
						member = true
					} else {
						budget.CPUMillis = min(budget.CPUMillis, m.CPUMillis)
						budget.MemoryBytes = min(budget.MemoryBytes, m.MemoryBytes)
					}
					for _, p := range policy.Pools {
						if p.PoolID == pool.ID {
							reserve.CPUMillis = max(reserve.CPUMillis, p.SystemReserve.CPUMillis)
							reserve.MemoryBytes = max(reserve.MemoryBytes, p.SystemReserve.MemoryBytes)
							reserve.StorageBytes = max(reserve.StorageBytes, p.SystemReserve.StorageBytes)
						}
					}
				}
			}
		}
		out.SystemReserve, err = sumResources(out.SystemReserve, capacityResources(reserve))
		if err != nil {
			return nil, err
		}
		available := CapacityResources{Servers: number(0)}
		if member {
			available.Servers = number(1)
			if obs.CPUMillis != nil {
				available.CPUMillis = number(max(0, min(*obs.CPUMillis, budget.CPUMillis)-reserve.CPUMillis))
			}
			if obs.MemoryBytes != nil {
				available.MemoryBytes = number(max(0, min(*obs.MemoryBytes, budget.MemoryBytes)-reserve.MemoryBytes))
			}
			if r.Observation != nil && freshCapacity(r.Observation.CheckedAt, now) && r.Observation.FreeStorageBytes != nil {
				available.StorageBytes = number(max(0, *r.Observation.FreeStorageBytes-reserve.StorageBytes))
			}
		} else {
			available = capacityResources(ResourceVector{})
		}
		out.Allocatable, err = sumResources(out.Allocatable, available)
		if err != nil {
			return nil, err
		}
	}
	out.Records = records
	out.ObservedStorageBytes = out.Observed.StorageBytes
	var err error
	if out.GPUs, err = summarizeGPUs(records, policy); err != nil {
		return nil, err
	}
	return out, nil
}

// CapacityHostFits evaluates one fresh host. Callers bound observations by pool
// budgets and system reserve, then pass the durable reservations. Aggregate
// Cloud totals never admit a workload.
func CapacityHostFits(observation CapacityObservation, reserved ResourceVector, demand CapacityDemand, now time.Time) bool {
	if !freshCapacity(observation.CheckedAt, now) || !nonnegativeCapacity(demand.Resources) || !nonnegativeCapacity(reserved) || demand.Resources.Servers != 0 || observation.CPUMillis == nil || observation.MemoryBytes == nil {
		return false
	}
	if demand.LocalData && demand.Resources.StorageBytes > 0 {
		return false
	}
	requested, err := addCapacity(reserved, demand.Resources)
	if err != nil {
		return false
	}
	if requested.CPUMillis > *observation.CPUMillis || requested.MemoryBytes > *observation.MemoryBytes {
		return false
	}
	return requested.StorageBytes == 0 || (observation.FreeStorageBytes != nil && requested.StorageBytes <= *observation.FreeStorageBytes)
}

func (s *PgStore) ListCapacityInventory(ctx context.Context, org, cloud string) ([]CapacityRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_cloud_capacity_inventory WHERE org_id=$1 AND cloud_id=$2 ORDER BY id`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CapacityRecord
	for rows.Next() {
		var raw []byte
		var r CapacityRecord
		if e := rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e := json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BeginCapacityRefresh issues an ordering token before any provider or runtime
// collection. Issuing a token never replaces the last committed inventory.
func (s *PgStore) BeginCapacityRefresh(ctx context.Context, org, cloud string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return 0, checkErr
	}
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_cloud_capacity_refreshes(org_id,cloud_id,issued_generation) VALUES($1,$2,1) ON CONFLICT(org_id,cloud_id) DO UPDATE SET issued_generation=loomplane_cloud_capacity_refreshes.issued_generation+1 WHERE loomplane_cloud_capacity_refreshes.issued_generation<$3 RETURNING issued_generation`, org, cloud, int64(math.MaxInt64)).Scan(&generation)
	if err != nil {
		if errors.Is(storeError(err), ErrNotFound) {
			return 0, ErrConflict
		}
		return 0, storeError(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return generation, nil
}

// SaveCapacityInventory consumes only the current uncommitted generation. The
// Cloud lock orders every replacement, including snapshots with no resources
// or account summaries. Independent runtime timestamps are preserved.
func (s *PgStore) SaveCapacityInventory(ctx context.Context, org, cloud string, generation int64, records []CapacityRecord, accounts []CapacityAccountSnapshot) error {
	if generation <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return checkErr
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_refreshes SET saved_generation=$3 WHERE org_id=$1 AND cloud_id=$2 AND issued_generation=$3 AND saved_generation<$3`, org, cloud, generation)
	if err != nil {
		return err
	}
	changed, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}

	records, err = retainDisconnectedCapacity(ctx, tx, org, cloud, records)
	if err != nil {
		return err
	}

	// Runtime checks have independent timestamps and may finish in a different
	// order from provider collection. Refuse a sample superseded by a host save.
	revisions := map[string]int64{}
	for _, record := range records {
		if record.Kind != "server" || record.ServerID == "" {
			continue
		}
		if prior, exists := revisions[record.ServerID]; exists && prior != record.ServerStateRevision {
			return ErrConflict
		}
		revisions[record.ServerID] = record.ServerStateRevision
	}
	hostIDs := make([]string, 0, len(revisions))
	for id := range revisions {
		hostIDs = append(hostIDs, id)
	}
	slices.Sort(hostIDs)
	for _, id := range hostIDs {
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT coalesce((body->>'state_revision')::bigint,0) FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR SHARE`, org, cloud, id).Scan(&revision); err != nil {
			return storeError(err)
		}
		if revision != revisions[id] {
			return ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_cloud_capacity_inventory WHERE org_id=$1 AND cloud_id=$2`, org, cloud); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range records {
		if (r.Kind != "server" && r.Kind != "volume") || r.Provider == "" || r.AccountID == "" || !strings.HasPrefix(r.ResourceID, r.Kind+":") || r.ResourceID == r.Kind+":" {
			return ErrInvalid
		}
		if _, e := sumResources(capacityResources(ResourceVector{}), r.Resources); e != nil {
			return e
		}
		if !recordGPUsValid(r) {
			return ErrInvalid
		}

		key := recordKey(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		raw, e := json.Marshal(r)
		if e != nil {
			return e
		}
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_inventory(org_id,cloud_id,id,server_id,provider,account_id,resource_id,servers,cpu_millis,memory_bytes,storage_bytes,observed_storage_bytes,checked_at,body,gpu_kind,gpus) VALUES($1,$2,$3,nullif($4,''),$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, org, cloud, inventoryID(r), r.ServerID, r.Provider, r.AccountID, r.ResourceID, r.Resources.Servers, r.Resources.CPUMillis, r.Resources.MemoryBytes, r.Resources.StorageBytes, observedStorage(r), r.CheckedAt, raw, r.GPUKind, r.GPUs)
		if err != nil {
			return storeError(err)
		}
	}
	slices.SortFunc(accounts, func(a, b CapacityAccountSnapshot) int {
		if n := cmp.Compare(a.Provider, b.Provider); n != 0 {
			return n
		}
		return cmp.Compare(a.AccountID, b.AccountID)
	})
	for _, account := range accounts {
		raw, e := json.Marshal(account)
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_accounts(org_id,provider,account_id,servers,cpu_millis,memory_bytes,storage_bytes,quota_servers,quota_cpu_millis,quota_memory_bytes,quota_storage_bytes,checked_at,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(org_id,provider,account_id) DO UPDATE SET servers=excluded.servers,cpu_millis=excluded.cpu_millis,memory_bytes=excluded.memory_bytes,storage_bytes=excluded.storage_bytes,quota_servers=excluded.quota_servers,quota_cpu_millis=excluded.quota_cpu_millis,quota_memory_bytes=excluded.quota_memory_bytes,quota_storage_bytes=excluded.quota_storage_bytes,checked_at=excluded.checked_at,body=excluded.body WHERE loomplane_cloud_capacity_accounts.checked_at<=excluded.checked_at`, org, account.Provider, account.AccountID, account.Provisioned.Servers, account.Provisioned.CPUMillis, account.Provisioned.MemoryBytes, account.Provisioned.StorageBytes, account.Quota.Servers, account.Quota.CPUMillis, account.Quota.MemoryBytes, account.Quota.StorageBytes, account.CheckedAt, raw)
		if e != nil {
			return e
		}
		n, e := tag.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrConflict
		}
	}

	persisted, readErr := capacityRecordsTx(ctx, tx, org, cloud)
	if readErr != nil {
		return readErr
	}
	if checkErr := reconcileCapacityReservations(ctx, tx, org, cloud, persisted); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}
func observedStorage(r CapacityRecord) *int64 {
	if r.Observation == nil {
		return nil
	}
	return r.Observation.StorageBytes
}
func (s *PgStore) CapacityScopes(ctx context.Context) ([]CapacityScope, error) {
	rows, err := s.db.Query(ctx, `SELECT org_id,cloud_id FROM loomplane_cloud_capacity_policies UNION SELECT org_id,cloud_id FROM loomplane_cloud_servers WHERE status IN ('ready','unhealthy') ORDER BY 1,2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CapacityScope
	for rows.Next() {
		var scope CapacityScope
		if e := rows.Scan(&scope.OrgID, &scope.CloudID); e != nil {
			return nil, e
		}
		out = append(out, scope)
	}
	return out, rows.Err()
}

func summarizeAccount(snapshot provider.CapacityInventory) (CapacityAccountSnapshot, error) {
	out := CapacityAccountSnapshot{Provider: snapshot.Provider, AccountID: snapshot.AccountID, Provisioned: capacityResources(ResourceVector{}), Quota: snapshot.Quota, CheckedAt: snapshot.CheckedAt}
	seen := map[string]bool{}
	for _, host := range snapshot.Servers {
		if host.ID == "" || seen["server:"+host.ID] {
			return out, provider.ErrInfrastructurePagination
		}
		seen["server:"+host.ID] = true
		var err error
		out.Provisioned, err = sumResources(out.Provisioned, CapacityResources{Servers: number(1), CPUMillis: host.CPUMillis, MemoryBytes: host.MemoryBytes, StorageBytes: host.StorageBytes})
		if err != nil {
			return out, err
		}
	}
	for _, volume := range snapshot.Volumes {
		if volume.ID == "" || seen["volume:"+volume.ID] {
			return out, provider.ErrInfrastructurePagination
		}
		seen["volume:"+volume.ID] = true
		var err error
		out.Provisioned, err = sumResources(out.Provisioned, CapacityResources{Servers: number(0), CPUMillis: number(0), MemoryBytes: number(0), StorageBytes: volume.StorageBytes})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
func (s *PgStore) GetCapacityAccount(ctx context.Context, org, providerName, account string) (*CapacityAccountSnapshot, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_cloud_capacity_accounts WHERE org_id=$1 AND provider=$2 AND account_id=$3`, org, providerName, account).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	var out CapacityAccountSnapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
