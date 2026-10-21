package workloadidentity

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	domain "github.com/xraph/loomplane/pkg/federation"
)

const EnrollmentLifetime = 5 * time.Minute

// EnrollmentRecord contains only public identity and server-owned attestation
// bounds. Possessing its ID alone cannot consume it; the exact SVID is required.
type EnrollmentRecord struct {
	ID           string                      `json:"id"`
	Binding      Binding                     `json:"binding"`
	Registration runtimecatalog.Registration `json:"registration"`
	CreatedAt    time.Time                   `json:"created_at"`
	ExpiresAt    time.Time                   `json:"expires_at"`
}

type EnrollmentIssuer interface {
	Register(context.Context, Binding) (*spiretypes.Entry, error)
	Revoke(context.Context, string) error
}

type EnrollmentStore struct {
	db     *pgdriver.PgDB
	domain string
	now    func() time.Time
}

func NewEnrollmentStore(db *pgdriver.PgDB, trustDomain string) *EnrollmentStore {
	return &EnrollmentStore{db: db, domain: trustDomain, now: time.Now}
}

// Reserve is a management-controller operation. Callers supply a freshly checked
// binding, never request selectors. Epoch, agent ID and issuer entry ID are ours.
func (s *EnrollmentStore) Reserve(ctx context.Context, b Binding, exports map[string]runtimecatalog.ExportBounds) (record *EnrollmentRecord, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	if stepErr := s.checkDestination(ctx, tx, b); stepErr != nil {
		return nil, stepErr
	}
	var epoch uint64
	if stepErr := tx.QueryRow(ctx, `INSERT INTO loomplane_federation_enrollment_epochs(org_id,destination_id,epoch) VALUES($1,$2,1) ON CONFLICT(org_id,destination_id) DO UPDATE SET epoch=loomplane_federation_enrollment_epochs.epoch+1 WHERE loomplane_federation_enrollment_epochs.epoch<$3 RETURNING epoch`, b.Destination.OrgID, b.Destination.ID, int64(math.MaxInt64)).Scan(&epoch); stepErr != nil {
		return nil, stepErr
	}
	var count int
	if stepErr := tx.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_enrollments WHERE org_id=$1 AND destination_id=$2`, b.Destination.OrgID, b.Destination.ID).Scan(&count); stepErr != nil {
		return nil, stepErr
	}
	if count >= 128 {
		return nil, runtimecatalog.ErrLimit
	}
	b.Agent = domain.AgentIdentity{Scope: b.Destination.Scope, DestinationID: b.Destination.ID, AgentID: uuid.NewString(), Epoch: epoch}
	b.Agent.URI, err = AgentURI(s.domain, b.Agent)
	if err != nil {
		return nil, err
	}
	b.EntryID = uuid.NewString()
	validator := &SPIREIssuer{domain: s.domain, now: s.now}
	if _, validationErr := validator.entry(b); validationErr != nil {
		return nil, validationErr
	}
	now := s.now().UTC()
	record = &EnrollmentRecord{ID: uuid.NewString(), Binding: b, Registration: runtimecatalog.Registration{Agent: b.Agent, DestinationGeneration: b.Destination.Generation, Active: true, Exports: exports}, CreatedAt: now, ExpiresAt: now.Add(EnrollmentLifetime)}
	if stepErr := runtimecatalog.ValidateRegistration(record.Registration); stepErr != nil {
		return nil, stepErr
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO loomplane_federation_enrollments(id,org_id,project_id,environment_id,destination_id,destination_generation,identity_revision,epoch,agent_id,entry_id,phase,body,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'pending',$11,$12,$13)`, record.ID, b.Agent.OrgID, b.Agent.ProjectID, b.Agent.EnvironmentID, b.Agent.DestinationID, b.Destination.Generation, b.Destination.Identity.Revision(), epoch, b.Agent.AgentID, b.EntryID, raw, now, record.ExpiresAt); err != nil {
		return nil, err
	}
	return record, tx.Commit()
}

func (s *EnrollmentStore) checkDestination(ctx context.Context, tx driver.Tx, b Binding) error {
	var generation int64
	var revision string
	err := tx.QueryRow(ctx, `SELECT generation,identity_revision FROM loomplane_federation_destinations WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND id=$4 AND NOT quarantined FOR SHARE`, b.Destination.OrgID, b.Destination.ProjectID, b.Destination.EnvironmentID, b.Destination.ID).Scan(&generation, &revision)
	if err != nil {
		return enrollmentError(err)
	}
	if generation != b.Destination.Generation || revision != b.Destination.Identity.Revision() {
		return ErrIdentity
	}
	return nil
}
func loadEnrollment(ctx context.Context, tx driver.Tx, org, id string) (*EnrollmentRecord, string, error) {
	query := `SELECT body,phase,created_at,expires_at FROM loomplane_federation_enrollments WHERE org_id=$1 AND id=$2 FOR UPDATE`
	var raw []byte
	var phase string
	var created, expires time.Time
	if err := tx.QueryRow(ctx, query, org, id).Scan(&raw, &phase, &created, &expires); err != nil {
		return nil, "", enrollmentError(err)
	}
	var r EnrollmentRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", err
	}
	r.CreatedAt = created
	r.ExpiresAt = expires
	if r.ID != id || r.Binding.Agent.OrgID != org {
		return nil, "", ErrIdentity
	}
	return &r, phase, nil
}

// Issue holds the enrollment lock across the bounded SPIRE call. Revoke cannot
// delete an entry and then race a delayed issuer into recreating it. An unknown
// outcome keeps the durable pending record and its trust cleanup reference.
func (s *EnrollmentStore) Issue(ctx context.Context, org, id string, issuer EnrollmentIssuer) (err error) {
	if issuer == nil {
		return ErrIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	r, phase, err := loadEnrollment(ctx, tx, org, id)
	if err != nil {
		return err
	}
	if (phase != "pending" && phase != "ready") || !r.ExpiresAt.After(s.now()) {
		return ErrIdentity
	}
	if stepErr := s.checkDestination(ctx, tx, r.Binding); stepErr != nil {
		return stepErr
	}
	if phase == "ready" {
		return tx.Commit()
	}
	entry, err := issuer.Register(ctx, r.Binding)
	if err != nil {
		return err
	}
	if entry == nil || entry.Id != r.Binding.EntryID {
		return ErrIdentity
	}
	var ready string
	if stepErr := tx.QueryRow(ctx, `UPDATE loomplane_federation_enrollments SET phase='ready',updated_at=now() WHERE org_id=$1 AND id=$2 AND expires_at>clock_timestamp() RETURNING id`, org, id).Scan(&ready); stepErr != nil {
		return stepErr
	}
	return tx.Commit()
}

// PendingRegistry is used only on the one-use enrollment endpoint. Runtime
// endpoints use the catalog's active Registry and cannot use this authority.
type PendingRegistry struct {
	Store *EnrollmentStore
	ID    string
}

func (p PendingRegistry) CheckAgent(ctx context.Context, a domain.AgentIdentity) error {
	return p.Store.CheckPending(ctx, p.ID, a)
}
func (s *EnrollmentStore) CheckPending(ctx context.Context, id string, a domain.AgentIdentity) error {
	r, err := s.Ready(ctx, a.OrgID, id)
	if err != nil {
		return err
	}
	if r.Binding.Agent != a {
		return ErrIdentity
	}
	return nil
}

// Consume rechecks the authenticated identity and consumes the handle in the
// same transaction that replaces the observer. Neither half can commit alone.
func (s *EnrollmentStore) Consume(ctx context.Context, id string, a domain.AgentIdentity) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	r, phase, err := loadEnrollment(ctx, tx, a.OrgID, id)
	if err != nil {
		return err
	}
	if r.Binding.Agent != a {
		return ErrIdentity
	}
	if stepErr := s.checkReady(ctx, tx, r, phase); stepErr != nil {
		return stepErr
	}
	if stepErr := runtimecatalog.DeactivateTx(ctx, tx, a.OrgID, a.DestinationID, ""); stepErr != nil {
		return stepErr
	}
	if stepErr := runtimecatalog.RegisterTx(ctx, tx, r.Registration); stepErr != nil {
		return stepErr
	}
	var consumed string
	if stepErr := tx.QueryRow(ctx, `UPDATE loomplane_federation_enrollments SET phase='consumed',consumed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2 AND expires_at>clock_timestamp() RETURNING id`, a.OrgID, id).Scan(&consumed); stepErr != nil {
		return stepErr
	}
	return tx.Commit()
}

// Revoke first removes local authority, even if SPIRE is unavailable. It releases
// retention only after confirmed remote deletion. Retrying is safe after restart.
func (s *EnrollmentStore) Revoke(ctx context.Context, org, id string, issuer EnrollmentIssuer) error {
	if issuer == nil {
		return ErrIdentity
	}
	r, err := s.beginRevoke(ctx, org, id, false)
	if err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	return s.completeRevoke(ctx, r, issuer)
}
func (s *EnrollmentStore) completeRevoke(ctx context.Context, r *EnrollmentRecord, issuer EnrollmentIssuer) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if stepErr := issuer.Revoke(ctx, r.Binding.EntryID); stepErr != nil {
		return stepErr
	}
	return s.finishRevoke(ctx, r)
}
func (s *EnrollmentStore) beginRevoke(ctx context.Context, org, id string, onlyEligible bool) (record *EnrollmentRecord, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	r, _, err := loadEnrollment(ctx, tx, org, id)
	if errors.Is(err, ErrIdentity) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if onlyEligible {
		var eligible bool
		if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_federation_enrollments e WHERE e.org_id=$1 AND e.id=$2 AND `+cleanupEligible+`)`, org, id).Scan(&eligible); queryErr != nil {
			return nil, queryErr
		}
		if !eligible {
			return nil, tx.Commit()
		}
	}
	if stepErr := runtimecatalog.DeactivateTx(ctx, tx, org, r.Binding.Agent.DestinationID, r.Binding.Agent.AgentID); stepErr != nil {
		return nil, stepErr
	}
	if _, err := tx.Exec(ctx, `UPDATE loomplane_federation_enrollments SET phase='revoking',updated_at=now() WHERE org_id=$1 AND id=$2`, org, id); err != nil {
		return nil, err
	}
	return r, tx.Commit()
}
func (s *EnrollmentStore) finishRevoke(ctx context.Context, r *EnrollmentRecord) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_federation_observers WHERE org_id=$1 AND agent_id=$2 AND NOT active`, r.Binding.Agent.OrgID, r.Binding.Agent.AgentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_federation_enrollments WHERE org_id=$1 AND id=$2 AND phase='revoking'`, r.Binding.Agent.OrgID, r.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func enrollmentError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdentity
	}
	return err
}
func enrollmentFinish(tx driver.Tx, err error) error {
	if rollback := tx.Rollback(); rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) {
		return errors.Join(err, rollback)
	}
	return err
}

// CleanupCandidates includes abandoned issuance and superseded observers. A live
// observer is not revoked merely because its bootstrap deadline has elapsed.
func (s *EnrollmentStore) CleanupCandidates(ctx context.Context, org string, limit int) ([]string, error) {
	if org == "" || limit < 1 || limit > 100 {
		return nil, ErrIdentity
	}
	rows, err := s.db.Query(ctx, `SELECT e.id FROM loomplane_federation_enrollments e WHERE e.org_id=$1 AND `+cleanupEligible+` ORDER BY e.updated_at,e.id LIMIT $2`, org, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if stepErr := rows.Scan(&id); stepErr != nil {
			return nil, stepErr
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
