package buildrunner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"
)

// Store keeps pools and their members.
type Store interface {
	CreatePool(ctx context.Context, p *Pool) error
	GetPool(ctx context.Context, id string) (*Pool, error)
	PoolByConnection(ctx context.Context, orgID, connectionID string) (*Pool, error)
	// ListPools lists an organization's pools, or the platform's for "".
	ListPools(ctx context.Context, orgID string) ([]Pool, error)
	// UpdatePool writes p's pins, settings and name if its revision is
	// still expected, and advances it when the pins changed.
	UpdatePool(ctx context.Context, p *Pool, expected int64) error
	DeletePool(ctx context.Context, id string) error
	AddMember(ctx context.Context, m *Member) error
	GetMember(ctx context.Context, nodeID string) (*Member, error)
	ListMembers(ctx context.Context, poolID string) ([]Member, error)
	UpdateMember(ctx context.Context, m *Member) error
	DeleteMember(ctx context.Context, nodeID string) error
	RecordBuild(ctx context.Context, nodeID string, at time.Time) error
}

// PgStore is the Postgres Store.
type PgStore struct{ db *pgdriver.PgDB }

// NewPgStore returns a store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const poolColumns = `id,org_id,scope,project_id,name,connection_id,mode,architecture,runtime_identity,settings,revision,created_by,created_at,updated_at`
const memberColumns = `node_id,pool_id,org_id,architecture,runtime_identity,evidence,reason,checked_at,builds,last_build_at,created_at`

type scanner interface{ Scan(...any) error }

func storeError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrInUse
		case "23514":
			return ErrInvalid
		}
	}
	return err
}

func scanPool(row scanner) (*Pool, error) {
	var p Pool
	var settings []byte
	if err := row.Scan(&p.ID, &p.OrgID, &p.Scope, &p.ProjectID, &p.Name, &p.ConnectionID, &p.Mode, &p.Architecture, &p.RuntimeIdentity, &settings, &p.Revision, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, storeError(err)
	}
	if err := json.Unmarshal(settings, &p.Settings); err != nil {
		return nil, err
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return &p, nil
}

func scanMember(row scanner) (*Member, error) {
	var m Member
	var evidence []byte
	if err := row.Scan(&m.NodeID, &m.PoolID, &m.OrgID, &m.Architecture, &m.RuntimeIdentity, &evidence, &m.Reason, &m.CheckedAt, &m.Builds, &m.LastBuildAt, &m.CreatedAt); err != nil {
		return nil, storeError(err)
	}
	if err := json.Unmarshal(evidence, &m.Evidence); err != nil {
		return nil, err
	}
	m.CreatedAt = m.CreatedAt.UTC()
	return &m, nil
}

// CreatePool implements Store.
func (s *PgStore) CreatePool(ctx context.Context, p *Pool) error {
	settings, err := json.Marshal(p.Settings)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_runner_pools(`+poolColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		p.ID, p.OrgID, p.Scope, p.ProjectID, p.Name, p.ConnectionID, p.Mode, p.Architecture, p.RuntimeIdentity, settings, p.Revision, p.CreatedBy, p.CreatedAt, p.UpdatedAt)
	return storeError(err)
}

// GetPool implements Store.
func (s *PgStore) GetPool(ctx context.Context, id string) (*Pool, error) {
	return scanPool(s.db.QueryRow(ctx, `SELECT `+poolColumns+` FROM loomplane_runner_pools WHERE id=$1`, id))
}

// PoolByConnection implements Store.
func (s *PgStore) PoolByConnection(ctx context.Context, orgID, connectionID string) (*Pool, error) {
	return scanPool(s.db.QueryRow(ctx, `SELECT `+poolColumns+` FROM loomplane_runner_pools WHERE org_id=$1 AND connection_id=$2 AND connection_id<>''`, orgID, connectionID))
}

// ListPools implements Store.
func (s *PgStore) ListPools(ctx context.Context, orgID string) ([]Pool, error) {
	rows, err := s.db.Query(ctx, `SELECT `+poolColumns+` FROM loomplane_runner_pools WHERE org_id=$1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pool{}
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpdatePool implements Store.
func (s *PgStore) UpdatePool(ctx context.Context, p *Pool, expected int64) error {
	settings, err := json.Marshal(p.Settings)
	if err != nil {
		return err
	}
	p.UpdatedAt = time.Now().UTC()
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_runner_pools SET name=$2, architecture=$3, runtime_identity=$4, settings=$5, revision=$6, updated_at=$7 WHERE id=$1 AND revision=$8`,
		p.ID, p.Name, p.Architecture, p.RuntimeIdentity, settings, p.Revision, p.UpdatedAt, expected)
	if err != nil {
		return storeError(err)
	}
	if n, rerr := tag.RowsAffected(); rerr == nil && n == 0 {
		return ErrConflict
	}
	return nil
}

// DeletePool implements Store.
func (s *PgStore) DeletePool(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM loomplane_runner_pools WHERE id=$1`, id)
	return storeError(err)
}

// AddMember implements Store.
func (s *PgStore) AddMember(ctx context.Context, m *Member) error {
	evidence, err := json.Marshal(nonNilMap(m.Evidence))
	if err != nil {
		return err
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_runner_members(`+memberColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		m.NodeID, m.PoolID, m.OrgID, m.Architecture, m.RuntimeIdentity, evidence, m.Reason, m.CheckedAt, m.Builds, m.LastBuildAt, m.CreatedAt)
	return storeError(err)
}

// GetMember implements Store.
func (s *PgStore) GetMember(ctx context.Context, nodeID string) (*Member, error) {
	return scanMember(s.db.QueryRow(ctx, `SELECT `+memberColumns+` FROM loomplane_runner_members WHERE node_id=$1`, nodeID))
}

// ListMembers implements Store. Runners that built least recently come
// first, so builds spread over the pool.
func (s *PgStore) ListMembers(ctx context.Context, poolID string) ([]Member, error) {
	rows, err := s.db.Query(ctx, `SELECT `+memberColumns+` FROM loomplane_runner_members WHERE pool_id=$1 ORDER BY last_build_at ASC NULLS FIRST, node_id`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// UpdateMember implements Store.
func (s *PgStore) UpdateMember(ctx context.Context, m *Member) error {
	evidence, err := json.Marshal(nonNilMap(m.Evidence))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE loomplane_runner_members SET architecture=$2, runtime_identity=$3, evidence=$4, reason=$5, checked_at=$6 WHERE node_id=$1`,
		m.NodeID, m.Architecture, m.RuntimeIdentity, evidence, m.Reason, m.CheckedAt)
	return storeError(err)
}

// DeleteMember implements Store.
func (s *PgStore) DeleteMember(ctx context.Context, nodeID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM loomplane_runner_members WHERE node_id=$1`, nodeID)
	return storeError(err)
}

// RecordBuild implements Store.
func (s *PgStore) RecordBuild(ctx context.Context, nodeID string, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_runner_members SET builds=builds+1, last_build_at=$2 WHERE node_id=$1`, nodeID, at)
	return storeError(err)
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

const machineColumns = `id,pool_id,org_id,name,provider_id,region,size,warm,state,node_id,in_flight,builds,reason,created_at,joined_at,active_at,gone_at`

var _ MachineStore = (*PgStore)(nil)

func scanMachine(row scanner) (*Machine, error) {
	var m Machine
	if err := row.Scan(&m.ID, &m.PoolID, &m.OrgID, &m.Name, &m.ProviderID, &m.Region, &m.Size, &m.Warm, &m.State, &m.NodeID, &m.InFlight, &m.Builds, &m.Reason, &m.CreatedAt, &m.JoinedAt, &m.ActiveAt, &m.GoneAt); err != nil {
		return nil, storeError(err)
	}
	m.CreatedAt, m.ActiveAt = m.CreatedAt.UTC(), m.ActiveAt.UTC()
	return &m, nil
}

// CreateMachine implements MachineStore.
func (s *PgStore) CreateMachine(ctx context.Context, m *Machine) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_runner_machines(`+machineColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		m.ID, m.PoolID, m.OrgID, m.Name, m.ProviderID, m.Region, m.Size, m.Warm, m.State, m.NodeID, m.InFlight, m.Builds, m.Reason, m.CreatedAt, m.JoinedAt, m.ActiveAt, m.GoneAt)
	return storeError(err)
}

// UpdateMachine implements MachineStore. In-flight and build counts are
// AdjustInFlight's, so an update never undoes one that raced it.
func (s *PgStore) UpdateMachine(ctx context.Context, m *Machine) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_runner_machines SET provider_id=$2, warm=$3, state=$4, node_id=$5, reason=$6, joined_at=$7, active_at=$8, gone_at=$9 WHERE id=$1`,
		m.ID, m.ProviderID, m.Warm, m.State, m.NodeID, m.Reason, m.JoinedAt, m.ActiveAt, m.GoneAt)
	return storeError(err)
}

// ListMachines implements MachineStore: the pool's machines that aren't gone.
func (s *PgStore) ListMachines(ctx context.Context, poolID string) ([]Machine, error) {
	rows, err := s.db.Query(ctx, `SELECT `+machineColumns+` FROM loomplane_runner_machines WHERE pool_id=$1 AND state<>'gone' ORDER BY created_at, id`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Machine{}
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// MachineByNode implements MachineStore.
func (s *PgStore) MachineByNode(ctx context.Context, nodeID string) (*Machine, error) {
	return scanMachine(s.db.QueryRow(ctx, `SELECT `+machineColumns+` FROM loomplane_runner_machines WHERE node_id=$1 AND node_id<>''`, nodeID))
}

// AdjustInFlight implements MachineStore.
func (s *PgStore) AdjustInFlight(ctx context.Context, nodeID string, delta int, finished bool, at time.Time) error {
	built := 0
	if finished {
		built = 1
	}
	_, err := s.db.Exec(ctx, `UPDATE loomplane_runner_machines SET in_flight=GREATEST(in_flight+$2,0), builds=builds+$3, active_at=$4 WHERE node_id=$1 AND node_id<>''`, nodeID, delta, built, at)
	return storeError(err)
}

// OnDemandPools implements MachineStore.
func (s *PgStore) OnDemandPools(ctx context.Context) ([]Pool, error) {
	rows, err := s.db.Query(ctx, `SELECT `+poolColumns+` FROM loomplane_runner_pools WHERE mode='on_demand' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pool{}
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
