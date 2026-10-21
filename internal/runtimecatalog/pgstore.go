package runtimecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	domain "github.com/xraph/loomplane/pkg/federation"
)

const MaxAgents = 1000
const MaxViewsPerAgent = 4

type PgStore struct{ db *pgdriver.PgDB }

func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

// Register is for the authorized enrollment controller. No HTTP observer may
// choose its identity, allowed exports or CIDRs. The FK retains the destination.
func (s *PgStore) Register(ctx context.Context, reg Registration) (err error) {
	if validationErr := ValidateRegistration(reg); validationErr != nil {
		return validationErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = finish(tx, err) }()
	if stepErr := RegisterTx(ctx, tx, reg); stepErr != nil {
		return stepErr
	}
	return tx.Commit()
}

// RegisterTx joins enrollment activation to the caller's transaction.
func RegisterTx(ctx context.Context, tx driver.Tx, reg Registration) error {
	if stepErr := ValidateRegistration(reg); stepErr != nil {
		return stepErr
	}
	var err error
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT generation FROM loomplane_federation_destinations WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND id=$4 AND NOT quarantined FOR SHARE`, reg.Agent.OrgID, reg.Agent.ProjectID, reg.Agent.EnvironmentID, reg.Agent.DestinationID).Scan(&generation); err != nil {
		return catalogError(err)
	}
	if generation != reg.DestinationGeneration {
		return ErrStale
	}
	state := AgentState{Registration: reg, Records: map[string]Record{}}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO loomplane_federation_observers(org_id,project_id,environment_id,agent_id,destination_id,destination_generation,active,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, reg.Agent.OrgID, reg.Agent.ProjectID, reg.Agent.EnvironmentID, reg.Agent.AgentID, reg.Agent.DestinationID, generation, reg.Active, raw); err != nil {
		return catalogError(err)
	}
	return appendChange(ctx, tx, &state)
}

// ValidateRegistration checks server-owned export bounds before issuing credentials.
func ValidateRegistration(reg Registration) error {
	state := AgentState{Registration: reg}
	if stepErr := checkAgent(&state, reg.Agent); stepErr != nil {
		return stepErr
	}
	if reg.DestinationGeneration < 1 || len(reg.Exports) > MaxBatch {
		return ErrInvalid
	}
	for id, b := range reg.Exports {
		if !validID(id) || b.Scope != reg.Agent.Scope || b.DestinationID != reg.Agent.DestinationID || !validID(b.PlacementID) || !validID(b.BindingID) || b.Port == 0 || len(b.CIDRs) == 0 || len(b.CIDRs) > 64 {
			return ErrInvalid
		}
		for _, raw := range b.CIDRs {
			p, err := netip.ParsePrefix(raw)
			if err != nil || p != p.Masked() || p.Bits() == 0 || p.Addr().Is4In6() {
				return ErrInvalid
			}
		}
	}
	return nil
}

const observerSelect = `SELECT a.state FROM loomplane_federation_observers a JOIN loomplane_federation_destinations d ON d.org_id=a.org_id AND d.project_id=a.project_id AND d.environment_id=a.environment_id AND d.id=a.destination_id WHERE a.org_id=$1 AND a.agent_id=$2 AND a.active AND NOT d.quarantined AND a.destination_generation=d.generation`

func loadAgent(ctx context.Context, tx driver.Tx, agent domain.AgentIdentity, lock bool) (*AgentState, error) {
	query := observerSelect
	if lock {
		query += ` FOR UPDATE OF a FOR SHARE OF d`
	}
	var raw []byte
	if err := tx.QueryRow(ctx, query, agent.OrgID, agent.AgentID).Scan(&raw); err != nil {
		return nil, catalogError(err)
	}
	var state AgentState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decode observer: %w", err)
	}
	if stepErr := checkAgent(&state, agent); stepErr != nil {
		return nil, stepErr
	}
	return &state, nil
}

// Update validates and commits the entire batch under the observer lock. An
// invalid record rolls back both endpoint writes and the monotonic sequence.
func (s *PgStore) Update(ctx context.Context, agent domain.AgentIdentity, change func(*AgentState) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = finish(tx, err) }()
	state, err := loadAgent(ctx, tx, agent, true)
	if err != nil {
		return err
	}
	if changeErr := change(state); changeErr != nil {
		return changeErr
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE loomplane_federation_observers SET state=$3,updated_at=now() WHERE org_id=$1 AND agent_id=$2`, agent.OrgID, agent.AgentID, raw); err != nil {
		return err
	}
	if stepErr := appendChange(ctx, tx, state); stepErr != nil {
		return stepErr
	}
	return tx.Commit()
}

// Read uses repeatable read so endpoints and the revision share one MVCC view.
func (s *PgStore) Read(ctx context.Context, agent domain.AgentIdentity) (out []AgentState, sequence uint64, err error) {
	tx, err := s.db.BeginTx(ctx, &driver.TxOptions{IsolationLevel: driver.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer func() { err = finish(tx, err) }()
	if _, err = loadAgent(ctx, tx, agent, false); err != nil {
		return nil, 0, err
	}
	if queryErr := tx.QueryRow(ctx, `SELECT coalesce((SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1),0)`, agent.OrgID).Scan(&sequence); queryErr != nil {
		return nil, 0, queryErr
	}
	rows, err := tx.Query(ctx, `SELECT a.state FROM loomplane_federation_observers a JOIN loomplane_federation_destinations d ON d.org_id=a.org_id AND d.project_id=a.project_id AND d.environment_id=a.environment_id AND d.id=a.destination_id WHERE a.org_id=$1 AND a.active AND NOT d.quarantined AND a.destination_generation=d.generation ORDER BY a.agent_id LIMIT $2`, agent.OrgID, MaxAgents+1)
	if err != nil {
		return nil, 0, err
	}
	out = []AgentState{}
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			rows.Close()
			return nil, 0, scanErr
		}
		var state AgentState
		if decodeErr := json.Unmarshal(raw, &state); decodeErr != nil {
			rows.Close()
			return nil, 0, decodeErr
		}
		out = append(out, state)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) > MaxAgents {
		return nil, 0, ErrLimit
	}
	return out, sequence, tx.Commit()
}

func (s *PgStore) SaveView(ctx context.Context, view *View) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = finish(tx, err) }()
	if _, err = loadAgent(ctx, tx, view.Agent, true); err != nil {
		return err
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return err
	}
	if len(raw) > 16<<20 {
		return ErrLimit
	}
	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_federation_catalog_views WHERE org_id=$1 AND agent_id=$2 AND expires_at<=$3`, view.Agent.OrgID, view.Agent.AgentID, view.IssuedAt); err != nil {
		return err
	}
	// Retain at most four immutable cursors per agent, including the new one.
	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_federation_catalog_views WHERE id IN (SELECT id FROM loomplane_federation_catalog_views WHERE org_id=$1 AND agent_id=$2 ORDER BY issued_at DESC,id DESC OFFSET $3)`, view.Agent.OrgID, view.Agent.AgentID, MaxViewsPerAgent-1); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO loomplane_federation_catalog_views(id,org_id,agent_id,body,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, view.ID, view.Agent.OrgID, view.Agent.AgentID, raw, view.IssuedAt, view.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PgStore) GetView(ctx context.Context, agent domain.AgentIdentity, id string) (view *View, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = finish(tx, err) }()
	if _, err := loadAgent(ctx, tx, agent, true); err != nil {
		return nil, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT body FROM loomplane_federation_catalog_views WHERE org_id=$1 AND agent_id=$2 AND id=$3`, agent.OrgID, agent.AgentID, id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCursorExpired
		}
		return nil, err
	}
	if stepErr := json.Unmarshal(raw, &view); stepErr != nil {
		return nil, stepErr
	}
	if view.Agent != agent {
		return nil, ErrCursorExpired
	}
	return view, tx.Commit()
}

func appendChange(ctx context.Context, tx driver.Tx, state *AgentState) error {
	agent := state.Registration.Agent
	raw, err := json.Marshal(struct {
		DestinationID string `json:"destination_id"`
		ObserverID    string `json:"observer_id"`
		Sequence      uint64 `json:"sequence"`
	}{agent.DestinationID, agent.AgentID, state.Sequence})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT loomplane_append_event($1,$2,'federation.catalog.changed',$3,$4)`, agent.OrgID, agent.ProjectID, agent.DestinationID, raw)
	return err
}
func catalogError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthorized
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return ErrUnauthorized
		case "23505":
			return ErrStale
		}
	}
	return err
}
func finish(tx driver.Tx, err error) error {
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return errors.Join(err, rollbackErr)
		}
	}
	return err
}

// CheckAgent rechecks the current observer and destination for every credential use.
func (s *PgStore) CheckAgent(ctx context.Context, agent domain.AgentIdentity) (err error) {
	tx, err := s.db.BeginTx(ctx, &driver.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { err = finish(tx, err) }()
	if _, err = loadAgent(ctx, tx, agent, false); err != nil {
		return err
	}
	return tx.Commit()
}

// DeactivateTx retires an observer and publishes the invalidation atomically.
// An empty agent ID retires whichever observer is active at the destination.
func DeactivateTx(ctx context.Context, tx driver.Tx, org, destination, agent string) error {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT state FROM loomplane_federation_observers WHERE org_id=$1 AND destination_id=$2 AND active AND ($3='' OR agent_id=$3) FOR UPDATE`, org, destination, agent).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var state AgentState
	if stepErr := json.Unmarshal(raw, &state); stepErr != nil {
		return stepErr
	}
	state.Registration.Active = false
	raw, err = json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_federation_observers SET active=false,state=$3,updated_at=now() WHERE org_id=$1 AND agent_id=$2`, org, state.Registration.Agent.AgentID, raw); err != nil {
		return err
	}
	return appendChange(ctx, tx, &state)
}

// CheckAgentTx holds the observer and destination while a controller records a
// delivery. It shares the catalog's exact registration and quarantine checks.
func CheckAgentTx(ctx context.Context, tx driver.Tx, agent domain.AgentIdentity) error {
	_, err := loadAgent(ctx, tx, agent, true)
	return err
}
