package access

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	domain "github.com/xraph/loomplane/pkg/federation"
)

var (
	ErrPolicyNotFound = errors.New("federation policy resource not found")
	ErrPolicyConflict = errors.New("federation policy resource changed")
)

// DecisionGuard rechecks the current caller's permission after acquiring the
// policy lock. Management services supply it; request bodies cannot supply one.
type DecisionGuard func(context.Context) error

type ExportRecord struct {
	domain.ServiceExport
	Revision  string     `json:"revision"`
	RetiredAt *time.Time `json:"retired_at,omitempty"`
}
type GrantRecord struct {
	domain.AccessGrant
	Revision         string     `json:"revision"`
	ProposedBy       string     `json:"proposed_by"`
	ApprovedBy       string     `json:"approved_by,omitempty"`
	ReviewedRevision string     `json:"reviewed_revision,omitempty"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty"`
	RevokedBy        string     `json:"revoked_by,omitempty"`
}

// PolicyStore serializes policy decisions within an organization. Every change,
// audit record and wake event commits with the same monotonic generation.
type PolicyStore struct{ db *pgdriver.PgDB }

func NewPolicyStore(db *pgdriver.PgDB) *PolicyStore { return &PolicyStore{db: db} }

func (s *PolicyStore) mutate(ctx context.Context, org, actor string, guard DecisionGuard, fn func(driver.Tx) error) (err error) {
	if !domain.ValidIdentitySegment(org) || !domain.ValidIdentitySegment(actor) || guard == nil {
		return ErrPolicy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = finishPolicyTx(tx, err) }()
	if stepErr := lockPolicyOrganization(ctx, tx, org); stepErr != nil {
		return stepErr
	}
	if stepErr := guard(ctx); stepErr != nil {
		return stepErr
	}
	if err = fn(tx); err != nil {
		return policyStoreError(err)
	}
	if stepErr := guard(ctx); stepErr != nil {
		return stepErr
	}
	return tx.Commit()
}
func finishPolicyTx(tx driver.Tx, err error) error {
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return errors.Join(err, rollbackErr)
		}
	}
	return err
}
func policyStoreError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPolicyNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return ErrPolicyNotFound
		case "23505":
			return ErrPolicyConflict
		case "23514":
			return ErrPolicy
		}
	}
	return err
}
func recordPolicyChange(ctx context.Context, tx driver.Tx, org, kind, id, actor string, state any) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT loomplane_federation_policy_change($1,$2,$3,$4,$5)`, org, kind, id, actor, raw)
	return err
}

type policyScanner interface{ Scan(...any) error }

func readPolicyRecord[T any](row policyScanner) (*T, error) {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return nil, policyStoreError(err)
	}
	var record T
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}
func (s *PolicyStore) GetExport(ctx context.Context, org, id string) (*ExportRecord, error) {
	return loadExport(ctx, s.db, org, id)
}
func (s *PolicyStore) GetGrant(ctx context.Context, org, id string) (*GrantRecord, error) {
	return loadGrant(ctx, s.db, org, id)
}

type policyReader interface {
	QueryRow(context.Context, string, ...any) driver.Row
}

func loadExport(ctx context.Context, tx policyReader, org, id string) (*ExportRecord, error) {
	return readPolicyRecord[ExportRecord](tx.QueryRow(ctx, `SELECT state FROM loomplane_federation_exports WHERE org_id=$1 AND id=$2`, org, id))
}
func loadGrant(ctx context.Context, tx policyReader, org, id string) (*GrantRecord, error) {
	return readPolicyRecord[GrantRecord](tx.QueryRow(ctx, `SELECT state FROM loomplane_federation_grants WHERE org_id=$1 AND id=$2`, org, id))
}
func validExpected(expected int64) bool { return expected >= 0 && expected < math.MaxInt64 }
func activeDestination(ctx context.Context, tx driver.Tx, scope domain.Scope, id string) error {
	var quarantined bool
	if err := tx.QueryRow(ctx, `SELECT quarantined FROM loomplane_federation_destinations WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND id=$4 FOR SHARE`, scope.OrgID, scope.ProjectID, scope.EnvironmentID, id).Scan(&quarantined); err != nil {
		return policyStoreError(err)
	}
	if quarantined {
		return ErrPolicyConflict
	}
	return nil
}
func activeExport(ctx context.Context, tx driver.Tx, export *ExportRecord) error {
	if export.RetiredAt != nil {
		return ErrPolicyConflict
	}
	for _, id := range export.DestinationIDs {
		if err := activeDestination(ctx, tx, export.Scope, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *PolicyStore) PutExport(ctx context.Context, in domain.ServiceExport, expected int64, actor string, guard DecisionGuard) (*ExportRecord, error) {
	if !validExpected(expected) {
		return nil, ErrPolicyConflict
	}
	in.Generation = expected + 1
	canonical, err := canonicalExport(in)
	if err != nil {
		return nil, err
	}
	result := &ExportRecord{ServiceExport: canonical}
	err = s.mutate(ctx, in.OrgID, actor, guard, func(tx driver.Tx) error {
		old, loadErr := loadExport(ctx, tx, in.OrgID, in.ID)
		if expected == 0 {
			if loadErr == nil {
				return ErrPolicyConflict
			}
			if !errors.Is(loadErr, ErrPolicyNotFound) {
				return loadErr
			}
		} else {
			if loadErr != nil {
				return loadErr
			}
			if old.Scope != in.Scope {
				return ErrPolicyNotFound
			}
			if old.Generation != expected || old.RetiredAt != nil {
				return ErrPolicyConflict
			}
			result.CreatedAt = old.CreatedAt
		}
		now := time.Now().UTC()
		if result.CreatedAt.IsZero() {
			result.CreatedAt = now
		}
		result.UpdatedAt = now
		if checkErr := activeExport(ctx, tx, result); checkErr != nil {
			return checkErr
		}
		return saveExport(ctx, tx, result, actor, "export.put")
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (s *PolicyStore) ProposeGrant(ctx context.Context, in domain.AccessGrant, expected int64, actor string, guard DecisionGuard) (*GrantRecord, error) {
	if !validExpected(expected) {
		return nil, ErrPolicyConflict
	}
	in.Generation = expected + 1
	in.OwnerID = actor
	in.ApprovedRevision = ""
	in.RevokedAt = nil
	canonical, err := canonicalGrant(in)
	if err != nil {
		return nil, err
	}
	result := &GrantRecord{AccessGrant: canonical, ProposedBy: actor}
	err = s.mutate(ctx, in.OrgID, actor, guard, func(tx driver.Tx) error {
		old, loadErr := loadGrant(ctx, tx, in.OrgID, in.ID)
		if expected == 0 {
			if loadErr == nil {
				return ErrPolicyConflict
			}
			if !errors.Is(loadErr, ErrPolicyNotFound) {
				return loadErr
			}
		} else {
			if loadErr != nil {
				return loadErr
			}
			if old.Scope != in.Scope {
				return ErrPolicyNotFound
			}
			if old.Generation != expected || old.ExportID != in.ExportID {
				return ErrPolicyConflict
			}
			result.CreatedAt = old.CreatedAt
		}
		export, loadErr := loadExport(ctx, tx, in.OrgID, in.ExportID)
		if loadErr != nil {
			return loadErr
		}
		if export.Scope != in.Scope {
			return ErrPolicyNotFound
		}
		if export.Revision != in.ExportRevision || export.Port != in.Port || export.Protocol != in.Protocol {
			return ErrPolicyConflict
		}
		if checkErr := activeExport(ctx, tx, export); checkErr != nil {
			return checkErr
		}
		if checkErr := activeDestination(ctx, tx, in.Source.Scope, in.Source.DestinationID); checkErr != nil {
			return checkErr
		}
		now := time.Now().UTC()
		if result.CreatedAt.IsZero() {
			result.CreatedAt = now
		}
		result.UpdatedAt = now
		if result.ExpiresAt != nil && !result.ExpiresAt.After(now) {
			return ErrPolicy
		}
		return saveGrant(ctx, tx, result, actor, "grant.propose")
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func saveGrant(ctx context.Context, tx driver.Tx, record *GrantRecord, actor, kind string) error {
	revision, err := GrantRevision(record.AccessGrant)
	if err != nil {
		return err
	}
	record.Revision = revision
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_federation_grants(org_id,project_id,environment_id,id,export_id,source_project_id,source_environment_id,source_destination_id,generation,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(org_id,id) DO UPDATE SET source_project_id=EXCLUDED.source_project_id,source_environment_id=EXCLUDED.source_environment_id,source_destination_id=EXCLUDED.source_destination_id,generation=EXCLUDED.generation,state=EXCLUDED.state`, record.OrgID, record.ProjectID, record.EnvironmentID, record.ID, record.ExportID, record.Source.ProjectID, record.Source.EnvironmentID, record.Source.DestinationID, record.Generation, raw); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_federation_grant_sources(org_id,grant_id,project_id,environment_id,destination_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, record.OrgID, record.ID, record.Source.ProjectID, record.Source.EnvironmentID, record.Source.DestinationID); err != nil {
		return err
	}
	return recordPolicyChange(ctx, tx, record.OrgID, kind, record.ID, actor, record)
}
func (s *PolicyStore) ApproveGrant(ctx context.Context, org, id string, expected int64, reviewed, actor string, guard DecisionGuard) (*GrantRecord, error) {
	if !validExpected(expected) || expected == 0 {
		return nil, ErrPolicyConflict
	}
	var result *GrantRecord
	err := s.mutate(ctx, org, actor, guard, func(tx driver.Tx) error {
		record, err := loadGrant(ctx, tx, org, id)
		if err != nil {
			return err
		}
		if record.Generation != expected || record.Revision != reviewed || record.ApprovedRevision != "" || record.RevokedAt != nil {
			return ErrPolicyConflict
		}
		now := time.Now().UTC()
		if record.ExpiresAt != nil && !record.ExpiresAt.After(now) {
			return ErrPolicyConflict
		}
		export, err := loadExport(ctx, tx, org, record.ExportID)
		if err != nil {
			return err
		}
		if export.Scope != record.Scope || export.Revision != record.ExportRevision || export.Port != record.Port || export.Protocol != record.Protocol {
			return ErrPolicyConflict
		}
		if checkErr := activeExport(ctx, tx, export); checkErr != nil {
			return checkErr
		}
		if checkErr := activeDestination(ctx, tx, record.Source.Scope, record.Source.DestinationID); checkErr != nil {
			return checkErr
		}
		record.Generation++
		record.UpdatedAt = now
		record.ApprovedBy = actor
		record.ApprovedAt = &now
		record.ReviewedRevision = reviewed
		record.ApprovedRevision, err = GrantRevision(record.AccessGrant)
		if err != nil {
			return err
		}
		if err := saveGrant(ctx, tx, record, actor, "grant.approve"); err != nil {
			return err
		}
		result = record
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (s *PolicyStore) RevokeGrant(ctx context.Context, org, id string, expected int64, actor string, guard DecisionGuard) (*GrantRecord, error) {
	if !validExpected(expected) || expected == 0 {
		return nil, ErrPolicyConflict
	}
	var result *GrantRecord
	err := s.mutate(ctx, org, actor, guard, func(tx driver.Tx) error {
		record, err := loadGrant(ctx, tx, org, id)
		if err != nil {
			return err
		}
		if record.Generation != expected || record.RevokedAt != nil {
			return ErrPolicyConflict
		}
		now := time.Now().UTC()
		record.Generation++
		record.UpdatedAt = now
		record.RevokedAt = &now
		record.RevokedBy = actor
		record.ApprovedRevision = ""
		if err := saveGrant(ctx, tx, record, actor, "grant.revoke"); err != nil {
			return err
		}
		result = record
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RetireExport stops future authorization and retains references needed to drain
// previously issued policy. A retired ID cannot be reused for another service.
func (s *PolicyStore) RetireExport(ctx context.Context, org, id string, expected int64, actor string, guard DecisionGuard) (*ExportRecord, error) {
	if !validExpected(expected) || expected == 0 {
		return nil, ErrPolicyConflict
	}
	var result *ExportRecord
	err := s.mutate(ctx, org, actor, guard, func(tx driver.Tx) error {
		record, err := loadExport(ctx, tx, org, id)
		if err != nil {
			return err
		}
		if record.Generation != expected || record.RetiredAt != nil {
			return ErrPolicyConflict
		}
		now := time.Now().UTC()
		record.Generation++
		record.UpdatedAt = now
		record.RetiredAt = &now
		if err := saveExport(ctx, tx, record, actor, "export.retire"); err != nil {
			return err
		}
		result = record
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func saveExport(ctx context.Context, tx driver.Tx, record *ExportRecord, actor, kind string) error {
	revision, err := ExportRevision(record.ServiceExport)
	if err != nil {
		return err
	}
	record.Revision = revision
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_federation_exports(org_id,project_id,environment_id,id,generation,state) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(org_id,id) DO UPDATE SET generation=EXCLUDED.generation,state=EXCLUDED.state`, record.OrgID, record.ProjectID, record.EnvironmentID, record.ID, record.Generation, raw); err != nil {
		return err
	}
	for _, id := range record.DestinationIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO loomplane_federation_export_destinations(org_id,project_id,environment_id,export_id,destination_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, record.OrgID, record.ProjectID, record.EnvironmentID, record.ID, id); err != nil {
			return err
		}
	}
	return recordPolicyChange(ctx, tx, record.OrgID, kind, record.ID, actor, record)
}

func lockPolicyOrganization(ctx context.Context, tx driver.Tx, org string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO loomplane_federation_policy_versions(org_id) VALUES($1) ON CONFLICT DO NOTHING`, org); err != nil {
		return err
	}
	var generation int64
	return tx.QueryRow(ctx, `SELECT generation FROM loomplane_federation_policy_versions WHERE org_id=$1 FOR UPDATE`, org).Scan(&generation)
}
