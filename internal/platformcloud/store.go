package platformcloud

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// platformWriteSetting is the transaction setting the projection guard
// trigger checks before it lets a platform-owned Cloud row change. This file
// is the only code that names it.
const platformWriteSetting = "loomplane.platform_write"

// driverTx is the transaction the store hands to callbacks.
type driverTx = driver.Tx

// querier is what the pool and a transaction share.
type querier interface {
	Exec(ctx context.Context, query string, args ...any) (driver.Result, error)
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) driver.Row
}

type PgStore struct{ db *pgdriver.PgDB }

func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

func storeError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503" || pg.Code == "23514") {
		return ErrConflict
	}
	return err
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // rollback is a no-op after commit

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// WithTenantLock runs fn in a transaction that holds the organization's
// tenant row. Placement decisions for one organization serialize on it. A
// tenant that has suspend reasons still passes: callers check suspension.
// Inside fn, read through the *Tx methods so the reads share fn's connection
// and see its writes.
func (s *PgStore) WithTenantLock(ctx context.Context, orgID string, fn func(driverTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM loomplane_platform_tenants WHERE org_id=$1 FOR UPDATE`, orgID).Scan(&status); err != nil {
		if errors.Is(storeError(err), ErrNotFound) {
			return ErrNotEnabled
		}
		return err
	}
	if status != "enabled" {
		return ErrNotEnabled
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// WithPlatformWrite runs fn in a transaction in which the projection guard
// lets platform-owned Cloud rows change.
func (s *PgStore) WithPlatformWrite(ctx context.Context, fn func(driverTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL `+platformWriteSetting+` = 'on'`); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PgStore) CreateRegion(ctx context.Context, r *Region) error {
	if r.ID == "" {
		r.ID = newID("reg_")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_platform_regions(id,name,display_name,status,revision,created_at,updated_at) VALUES($1,$2,$3,$4,1,now(),now())`,
		r.ID, r.Name, r.DisplayName, r.Status)
	if err != nil {
		return storeError(err)
	}
	r.Revision = 1
	return nil
}

func (s *PgStore) UpdateRegion(ctx context.Context, r *Region) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_platform_regions SET name=$2,display_name=$3,status=$4,revision=revision+1,updated_at=now() WHERE id=$1 AND revision=$5`,
		r.ID, r.Name, r.DisplayName, r.Status, r.Revision)
	if err != nil {
		return storeError(err)
	}
	return s.afterRevisionUpdate(ctx, res, "loomplane_platform_regions", "id", r.ID, &r.Revision)
}

// afterRevisionUpdate tells a stale revision from a missing row when an
// optimistic update touched nothing, and bumps the caller's revision when it
// touched one.
func (s *PgStore) afterRevisionUpdate(ctx context.Context, res driver.Result, table, key, id string, revision *int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		*revision++
		return nil
	}
	var one int
	if err = s.db.QueryRow(ctx, `SELECT 1 FROM `+table+` WHERE `+key+`=$1`, id).Scan(&one); err != nil {
		return storeError(err)
	}
	return ErrConflict
}

func (s *PgStore) ListRegions(ctx context.Context) ([]Region, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,display_name,status,revision FROM loomplane_platform_regions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Region{}
	for rows.Next() {
		var r Region
		if err = rows.Scan(&r.ID, &r.Name, &r.DisplayName, &r.Status, &r.Revision); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PgStore) CreateHost(ctx context.Context, h *Host) error {
	if h.ID == "" {
		h.ID = newID("host_")
	}
	ev, al, err := hostJSON(h)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_platform_hosts(id,region_id,endpoint,host_id,status,evidence,allotment,client_fingerprint,generation,revision,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,1,now(),now())`,
		h.ID, h.RegionID, h.Endpoint, h.HostID, h.Status, ev, al, h.ClientFingerprint, h.Generation)
	if err != nil {
		return storeError(err)
	}
	h.Revision = 1
	return nil
}

func hostJSON(h *Host) (evidence, allotment []byte, err error) {
	if evidence, err = json.Marshal(h.Evidence); err != nil {
		return nil, nil, err
	}
	if allotment, err = json.Marshal(h.Allotment); err != nil {
		return nil, nil, err
	}
	return evidence, allotment, nil
}

// UpdateHost writes the host's mutable fields if its revision is still the
// stored one.
func (s *PgStore) UpdateHost(ctx context.Context, h *Host) error {
	ev, al, err := hostJSON(h)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE loomplane_platform_hosts SET endpoint=$2,status=$3,evidence=$4,allotment=$5,client_fingerprint=$6,generation=$7,revision=revision+1,updated_at=now() WHERE id=$1 AND revision=$8`,
		h.ID, h.Endpoint, h.Status, ev, al, h.ClientFingerprint, h.Generation, h.Revision)
	if err != nil {
		return storeError(err)
	}
	return s.afterRevisionUpdate(ctx, res, "loomplane_platform_hosts", "id", h.ID, &h.Revision)
}

const hostColumns = `id,region_id,endpoint,host_id,status,client_fingerprint,evidence,allotment,generation,revision`

func scanHost(row interface{ Scan(...any) error }) (*Host, error) {
	var h Host
	var ev, al []byte
	if err := row.Scan(&h.ID, &h.RegionID, &h.Endpoint, &h.HostID, &h.Status, &h.ClientFingerprint, &ev, &al, &h.Generation, &h.Revision); err != nil {
		return nil, storeError(err)
	}
	if err := json.Unmarshal(ev, &h.Evidence); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(al, &h.Allotment); err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *PgStore) GetHost(ctx context.Context, id string) (*Host, error) {
	return scanHost(s.db.QueryRow(ctx, `SELECT `+hostColumns+` FROM loomplane_platform_hosts WHERE id=$1`, id))
}

// LockHost reads a host and holds its row until tx ends, so placements on it
// serialize.
func (s *PgStore) LockHost(ctx context.Context, tx driverTx, hostID string) (*Host, error) {
	return scanHost(tx.QueryRow(ctx, `SELECT `+hostColumns+` FROM loomplane_platform_hosts WHERE id=$1 FOR UPDATE`, hostID))
}

// ListHosts returns every host, or the hosts of one region when regionID is set.
func (s *PgStore) ListHosts(ctx context.Context, regionID string) ([]Host, error) {
	return listHosts(ctx, s.db, regionID)
}

// ListHostsTx is ListHosts on tx.
func (s *PgStore) ListHostsTx(ctx context.Context, tx driverTx, regionID string) ([]Host, error) {
	return listHosts(ctx, tx, regionID)
}

func listHosts(ctx context.Context, q querier, regionID string) ([]Host, error) {
	rows, err := q.Query(ctx, `SELECT `+hostColumns+` FROM loomplane_platform_hosts WHERE $1='' OR region_id=$1 ORDER BY created_at,id`, regionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// HostUse adds up what live placements take from a host, builds included.
func (s *PgStore) HostUse(ctx context.Context, hostID string) (Allotment, error) {
	return hostUse(ctx, s.db, hostID)
}

// HostUseTx is HostUse on tx.
func (s *PgStore) HostUseTx(ctx context.Context, tx driverTx, hostID string) (Allotment, error) {
	return hostUse(ctx, tx, hostID)
}

func hostUse(ctx context.Context, q querier, hostID string) (Allotment, error) {
	var a Allotment
	err := q.QueryRow(ctx, `SELECT coalesce(sum(vcpus),0)::int,coalesce(sum(memory_mib),0)::int,coalesce(sum(disk_mib),0)::int,count(*)::int
FROM loomplane_platform_placements WHERE host_id=$1 AND state<>'released'`, hostID).Scan(&a.VCPUs, &a.MemoryMiB, &a.DiskMiB, &a.VMs)
	return a, err
}

func (s *PgStore) GetTenant(ctx context.Context, orgID string) (*Tenant, error) {
	return scanTenant(s.db.QueryRow(ctx, `SELECT `+tenantColumns+` FROM loomplane_platform_tenants WHERE org_id=$1`, orgID))
}

const tenantColumns = `org_id,status,limits,suspend_reasons,suspended_at,revision`

func scanTenant(row interface{ Scan(...any) error }) (*Tenant, error) {
	var t Tenant
	var limits []byte
	var reasons []string
	if err := row.Scan(&t.OrgID, &t.Status, &limits, &reasons, &t.SuspendedAt, &t.Revision); err != nil {
		return nil, storeError(err)
	}
	if len(limits) > 0 && string(limits) != "null" {
		t.Limits = &Limits{}
		if err := json.Unmarshal(limits, t.Limits); err != nil {
			return nil, err
		}
	}
	t.SuspendReasons = make([]SuspendReason, 0, len(reasons))
	for _, r := range reasons {
		t.SuspendReasons = append(t.SuspendReasons, SuspendReason(r))
	}
	return &t, nil
}

// UpsertTenant creates the tenant or replaces it. A Revision of zero
// replaces whatever is stored. Otherwise the stored revision must match.
func (s *PgStore) UpsertTenant(ctx context.Context, t *Tenant) error {
	var limits []byte
	if t.Limits != nil {
		var err error
		if limits, err = json.Marshal(t.Limits); err != nil {
			return err
		}
	}
	reasons := make([]string, 0, len(t.SuspendReasons))
	for _, r := range t.SuspendReasons {
		reasons = append(reasons, string(r))
	}
	if t.Revision == 0 {
		return storeError(s.db.QueryRow(ctx, `INSERT INTO loomplane_platform_tenants(org_id,status,limits,suspend_reasons,suspended_at,revision,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,1,now(),now())
ON CONFLICT(org_id) DO UPDATE SET status=EXCLUDED.status,limits=EXCLUDED.limits,suspend_reasons=EXCLUDED.suspend_reasons,suspended_at=EXCLUDED.suspended_at,revision=loomplane_platform_tenants.revision+1,updated_at=now()
RETURNING revision`, t.OrgID, t.Status, limits, reasons, t.SuspendedAt).Scan(&t.Revision))
	}
	res, err := s.db.Exec(ctx, `UPDATE loomplane_platform_tenants SET status=$2,limits=$3,suspend_reasons=$4,suspended_at=$5,revision=revision+1,updated_at=now() WHERE org_id=$1 AND revision=$6`,
		t.OrgID, t.Status, limits, reasons, t.SuspendedAt, t.Revision)
	if err != nil {
		return storeError(err)
	}
	return s.afterRevisionUpdate(ctx, res, "loomplane_platform_tenants", "org_id", t.OrgID, &t.Revision)
}

func (s *PgStore) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.Query(ctx, `SELECT `+tenantColumns+` FROM loomplane_platform_tenants ORDER BY org_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Tenant{}
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// TenantUse adds up an organization's live placements. Services count toward
// vCPUs, memory and VMs, whatever their state short of released (a suspended
// service still holds its place). Reserved and running builds count as concurrent.
func (s *PgStore) TenantUse(ctx context.Context, orgID string) (Use, error) {
	return tenantUse(ctx, s.db, orgID)
}

// TenantUseTx is TenantUse on tx.
func (s *PgStore) TenantUseTx(ctx context.Context, tx driverTx, orgID string) (Use, error) {
	return tenantUse(ctx, tx, orgID)
}

func tenantUse(ctx context.Context, q querier, orgID string) (Use, error) {
	var u Use
	err := q.QueryRow(ctx, `SELECT
coalesce(sum(vcpus) FILTER (WHERE kind='service' AND state<>'released'),0)::int,
coalesce(sum(memory_mib) FILTER (WHERE kind='service' AND state<>'released'),0)::int,
count(*) FILTER (WHERE kind='service' AND state<>'released')::int,
count(*) FILTER (WHERE kind='build' AND state IN ('reserved','running'))::int
FROM loomplane_platform_placements WHERE org_id=$1`, orgID).Scan(&u.VCPUs, &u.MemoryMiB, &u.VMs, &u.ConcurrentBuilds)
	return u, err
}

const placementColumns = `id,org_id,project_id,environment_id,resource,kind,region_id,host_id,vcpus,memory_mib,disk_mib,state,restore_running,waiting_capacity,instance_id,generation,suspended_at`

func (s *PgStore) listPlacements(ctx context.Context, where string, arg string) ([]Placement, error) {
	rows, err := s.db.Query(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements WHERE `+where+` ORDER BY created_at,id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Placement{}
	for rows.Next() {
		var p Placement
		var suspended *time.Time
		if err = rows.Scan(&p.ID, &p.OrgID, &p.ProjectID, &p.EnvironmentID, &p.Resource, &p.Kind, &p.RegionID, &p.HostID, &p.VCPUs, &p.MemoryMiB, &p.DiskMiB,
			&p.State, &p.RestoreRunning, &p.WaitingCapacity, &p.InstanceID, &p.Generation, &suspended); err != nil {
			return nil, err
		}
		p.SuspendedAt = suspended
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPlacements returns an organization's placements that are not released.
func (s *PgStore) ListPlacements(ctx context.Context, orgID string) ([]Placement, error) {
	return s.listPlacements(ctx, `org_id=$1 AND state<>'released'`, orgID)
}

// PlacementsOnHost returns the placements on a host that are not released.
func (s *PgStore) PlacementsOnHost(ctx context.Context, hostID string) ([]Placement, error) {
	return s.listPlacements(ctx, `host_id=$1 AND state<>'released'`, hostID)
}
