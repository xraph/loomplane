package workloadidentity

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// ReservePrepared binds one consumer enrollment to the saved preparation. The
// receipt survives trust cleanup, so a lost reply cannot cause another issuance.
func (s *EnrollmentStore) ReservePrepared(ctx context.Context, b Binding, exports map[string]runtimecatalog.ExportBounds, preparationID, actor string, guard func(context.Context) error) (record *EnrollmentRecord, err error) {
	if b.Method != "kubernetes" || len(exports) != 0 || !domain.ValidIdentitySegment(preparationID) || !domain.ValidIdentitySegment(actor) || guard == nil {
		return nil, ErrIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	if checkErr := s.checkDestination(ctx, tx, b); checkErr != nil {
		return nil, checkErr
	}
	var receipt string
	var generation int64
	err = tx.QueryRow(ctx, `SELECT p.enrollment_id,p.generation FROM loomplane_federation_preparations p
 JOIN loomplane_federation_installations i ON i.org_id=p.org_id AND i.destination_id=p.destination_id
 WHERE p.org_id=$1 AND p.destination_id=$2 AND p.id=$3 AND p.state='prepared'
 AND p.project_id=$4 AND p.environment_id=$5 AND p.identity_revision=$6 AND p.destination_generation=$7-1
 AND p.intent->>'namespace'=$8 AND p.intent->>'namespace_uid'=$9 AND p.pod_uid=$10 AND 'loomplane-agent-'||p.id=$11
 AND i.generation=$7 AND i.profile->>'pod_name'=$11 AND i.profile->>'pod_uid'=$10 AND i.profile->>'service_account'=$11
 AND i.profile->>'container'=$12 AND i.profile->>'connection_id'=$13 AND i.profile->>'identity_revision'=$6
 FOR UPDATE OF p`, b.Destination.OrgID, b.Destination.ID, preparationID, b.Destination.ProjectID, b.Destination.EnvironmentID, b.Destination.Identity.Revision(), b.Destination.Generation, b.Namespace, b.NamespaceUID, b.PodUID, b.ServiceAccount, b.Container, b.Destination.ConnectionID).Scan(&receipt, &generation)
	if err != nil {
		return nil, enrollmentError(err)
	}
	if generation == math.MaxInt64 {
		return nil, ErrIdentity
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if receipt != "" {
		record, err = s.recoverPreparedEnrollment(ctx, tx, b, receipt)
		if err != nil {
			return nil, err
		}
	} else {
		record, err = s.reserveTx(ctx, tx, b, nil)
		if err != nil {
			return nil, err
		}
		if _, writeErr := tx.Exec(ctx, `UPDATE loomplane_federation_preparations SET enrollment_id=$3,generation=generation+1,recorded_by=$4,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, b.Destination.OrgID, preparationID, record.ID, actor); writeErr != nil {
			return nil, writeErr
		}
		event, marshalErr := json.Marshal(map[string]any{"preparation_id": preparationID, "enrollment_id": record.ID, "destination_id": b.Destination.ID, "generation": generation + 1, "recorded_by": actor, "agent_id": record.Binding.Agent.AgentID, "epoch": record.Binding.Agent.Epoch})
		if marshalErr != nil {
			return nil, marshalErr
		}
		if _, eventErr := tx.Exec(ctx, `SELECT loomplane_append_event($1,$2,$3,$4,$5)`, b.Destination.OrgID, b.Destination.ProjectID, "federation.preparation.enrollment_reserved", preparationID, event); eventErr != nil {
			return nil, eventErr
		}
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return record, tx.Commit()
}
func (s *EnrollmentStore) recoverPreparedEnrollment(ctx context.Context, tx driver.Tx, b Binding, id string) (*EnrollmentRecord, error) {
	// Do not take an enrollment row lock while holding preparation and destination.
	// Issue locks enrollment first. The body is immutable; Issue/Ready recheck phase.
	r, phase, err := scanEnrollment(tx.QueryRow(ctx, `SELECT body,phase,created_at,expires_at FROM loomplane_federation_enrollments WHERE org_id=$1 AND id=$2`, b.Destination.OrgID, id), b.Destination.OrgID, id)
	if err != nil {
		return nil, err
	}
	saved := r.Binding
	if (phase != "pending" && phase != "ready") || !r.ExpiresAt.After(s.now()) || saved.Destination.Scope != b.Destination.Scope || saved.Destination.ID != b.Destination.ID || saved.Destination.Generation != b.Destination.Generation || saved.Destination.Identity != b.Destination.Identity || saved.Destination.ConnectionID != b.Destination.ConnectionID || saved.Method != b.Method || saved.ParentURI != b.ParentURI || saved.Namespace != b.Namespace || saved.NamespaceUID != b.NamespaceUID || saved.ServiceAccount != b.ServiceAccount || saved.PodUID != b.PodUID || saved.Container != b.Container || saved.TTL != b.TTL || len(r.Registration.Exports) != 0 {
		return nil, ErrIdentity
	}
	candidate := b
	candidate.Agent = r.Binding.Agent
	candidate.EntryID = r.Binding.EntryID
	validator := &SPIREIssuer{domain: s.domain, now: s.now}
	if _, validationErr := validator.entry(candidate); validationErr != nil {
		return nil, validationErr
	}
	if checkErr := s.checkEpoch(ctx, tx, r); checkErr != nil {
		return nil, checkErr
	}
	return r, nil
}
