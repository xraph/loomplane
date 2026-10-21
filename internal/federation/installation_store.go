package federation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// KubernetesInstallation retains a verified pod binding. Generation is the
// destination generation assigned by this write, including after retirement.
type KubernetesInstallation struct {
	Profile    KubernetesAttestationProfile `json:"profile"`
	Generation int64                        `json:"generation"`
	RecordedBy string                       `json:"recorded_by"`
	CheckedAt  time.Time                    `json:"checked_at"`
	CreatedAt  time.Time                    `json:"created_at"`
	UpdatedAt  time.Time                    `json:"updated_at"`
}

type InstallationStore interface {
	GetInstallation(context.Context, string, string) (*KubernetesInstallation, error)
	PutInstallation(context.Context, domain.Destination, KubernetesAttestationProfile, int64, string, time.Time, func(context.Context) error) (*KubernetesInstallation, error)
	RetireInstallation(context.Context, string, string, int64, string, func(context.Context) error) error
}

const installationColumns = `profile,generation,recorded_by,checked_at,created_at,updated_at`

func (s *PgStore) GetInstallation(ctx context.Context, org, id string) (*KubernetesInstallation, error) {
	return scanInstallation(s.db.QueryRow(ctx, `SELECT `+installationColumns+` FROM loomplane_federation_installations WHERE org_id=$1 AND destination_id=$2`, org, id), org, id)
}
func scanInstallation(row rowScanner, org, id string) (*KubernetesInstallation, error) {
	var r KubernetesInstallation
	var raw []byte
	if err := row.Scan(&raw, &r.Generation, &r.RecordedBy, &r.CheckedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, storeError(err)
	}
	if err := json.Unmarshal(raw, &r.Profile); err != nil {
		return nil, err
	}
	if !validAttestationProfile(r.Profile) || r.Profile.OrgID != org || r.Profile.DestinationID != id || r.Generation < 1 {
		return nil, ErrInvalid
	}
	return &r, nil
}

// PutInstallation serializes replacement with enrollment through the destination
// lock. Every change advances its generation, fencing old pending and active agents.
func (s *PgStore) PutInstallation(ctx context.Context, d domain.Destination, p KubernetesAttestationProfile, expected int64, actor string, checkedAt time.Time, guard func(context.Context) error) (out *KubernetesInstallation, err error) {
	now := time.Now().UTC()
	if !profileMatchesDestination(p, d) || d.Generation < 1 || d.Generation == math.MaxInt64 || expected < 0 || !domain.ValidIdentitySegment(actor) || guard == nil || checkedAt.IsZero() || checkedAt.Before(now.Add(-5*time.Minute)) || checkedAt.After(now.Add(30*time.Second)) {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = finishInstallation(tx, err) }()
	current, err := scanDestination(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_federation_destinations WHERE org_id=$1 AND id=$2 FOR UPDATE`, d.OrgID, d.ID))
	if err != nil {
		return nil, err
	}
	if current.Generation != d.Generation {
		return nil, ErrConflict
	}
	if current.Scope != d.Scope || current.ConnectionID != d.ConnectionID || current.Identity != d.Identity || current.Quarantined {
		return nil, ErrIdentityChanged
	}
	previous, readErr := scanInstallation(tx.QueryRow(ctx, `SELECT `+installationColumns+` FROM loomplane_federation_installations WHERE org_id=$1 AND destination_id=$2`, d.OrgID, d.ID), d.OrgID, d.ID)
	if readErr != nil && !errors.Is(readErr, ErrNotFound) {
		return nil, readErr
	}
	if (previous == nil && expected != 0) || (previous != nil && previous.Generation != expected) {
		return nil, ErrConflict
	}
	if guardErr := guard(ctx); guardErr != nil {
		return nil, guardErr
	}
	out = &KubernetesInstallation{Profile: p, Generation: d.Generation + 1, RecordedBy: actor, CheckedAt: checkedAt.UTC(), CreatedAt: now, UpdatedAt: now}
	if previous != nil {
		out.CreatedAt = previous.CreatedAt
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if _, execErr := tx.Exec(ctx, `INSERT INTO loomplane_federation_installations(org_id,project_id,environment_id,destination_id,generation,profile,recorded_by,checked_at,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(org_id,destination_id) DO UPDATE SET generation=EXCLUDED.generation,profile=EXCLUDED.profile,recorded_by=EXCLUDED.recorded_by,checked_at=EXCLUDED.checked_at,updated_at=EXCLUDED.updated_at`, p.OrgID, p.ProjectID, p.EnvironmentID, p.DestinationID, out.Generation, raw, actor, out.CheckedAt, out.CreatedAt, out.UpdatedAt); execErr != nil {
		return nil, storeError(execErr)
	}
	if stepErr := advanceInstallationDestination(ctx, tx, current, out, "federation.installation.recorded"); stepErr != nil {
		return nil, stepErr
	}
	if guardErr := guard(ctx); guardErr != nil {
		return nil, guardErr
	}
	return out, tx.Commit()
}

// RetireInstallation fences the runtime immediately. Enrollment trust records
// remain until the cleanup worker confirms SPIRE deletion and policy lease expiry.
func (s *PgStore) RetireInstallation(ctx context.Context, org, id string, expected int64, actor string, guard func(context.Context) error) (err error) {
	if expected < 1 || !domain.ValidIdentitySegment(actor) || guard == nil {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = finishInstallation(tx, err) }()
	d, err := scanDestination(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_federation_destinations WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if err != nil {
		return err
	}
	r, err := scanInstallation(tx.QueryRow(ctx, `SELECT `+installationColumns+` FROM loomplane_federation_installations WHERE org_id=$1 AND destination_id=$2`, org, id), org, id)
	if err != nil {
		return err
	}
	if r.Generation != expected || d.Generation == math.MaxInt64 {
		return ErrConflict
	}
	if guardErr := guard(ctx); guardErr != nil {
		return guardErr
	}
	if _, execErr := tx.Exec(ctx, `DELETE FROM loomplane_federation_installations WHERE org_id=$1 AND destination_id=$2`, org, id); execErr != nil {
		return storeError(execErr)
	}
	r.Generation = d.Generation + 1
	r.RecordedBy = actor
	r.UpdatedAt = time.Now().UTC()
	if stepErr := advanceInstallationDestination(ctx, tx, d, r, "federation.installation.retired"); stepErr != nil {
		return stepErr
	}
	if guardErr := guard(ctx); guardErr != nil {
		return guardErr
	}
	return tx.Commit()
}
func advanceInstallationDestination(ctx context.Context, tx driver.Tx, d *domain.Destination, r *KubernetesInstallation, event string) error {
	if _, err := tx.Exec(ctx, `UPDATE loomplane_federation_destinations SET generation=$3,updated_at=$4 WHERE org_id=$1 AND id=$2`, d.OrgID, d.ID, r.Generation, r.UpdatedAt); err != nil {
		return storeError(err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT loomplane_append_event($1,$2,$3,$4,$5)`, d.OrgID, d.ProjectID, event, d.ID, raw)
	return err
}
func finishInstallation(tx driver.Tx, err error) error {
	if rollback := tx.Rollback(); rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) {
		return errors.Join(err, rollback)
	}
	return err
}
