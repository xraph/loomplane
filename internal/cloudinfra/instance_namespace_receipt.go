package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/currentauthority"
)

// NamespaceKnownOutcome records observed identities. It grants no permission to
// create, renew, publish or repeat a provider operation.
type NamespaceKnownOutcome struct {
	NamespaceRevision int64                      `json:"namespace_revision"`
	Reason            string                     `json:"reason,omitempty"`
	AuthorizedAt      time.Time                  `json:"authorized_at"`
	AttemptID         string                     `json:"attempt_id"`
	InputDigest       string                     `json:"input_digest"`
	Generation        int64                      `json:"generation"`
	OwnershipEpoch    int64                      `json:"ownership_epoch"`
	State             kubernetes.EnrollmentState `json:"state"`
}

func (s *Service) preserveInstanceNamespaceOutcome(ctx context.Context, o *ClusterOperation) error {
	if s.InstanceNamespaces == nil {
		return currentauthority.ErrUnavailable
	}
	return s.InstanceNamespaces.preserveOutcome(ctx, o)
}

func (s *InstanceNamespaceTransactions) preserveOutcome(ctx context.Context, o *ClusterOperation) (outErr error) {
	if s == nil || s.store == nil || s.coordinator == nil || o == nil || o.Kind != InstanceNamespaceKind || o.Enrollment == nil || len(o.Steps) != 2 || o.NamespaceAttemptID == "" || len(o.NamespaceInputDigest) != 64 {
		return ErrInvalid
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if len(raw) > 1024*1024 {
		return ErrInvalid
	}
	var observed ClusterOperation
	if observedDecodeErr := json.Unmarshal(raw, &observed); observedDecodeErr != nil {
		return observedDecodeErr
	}
	owned, finish, err := s.beginNamespaceWork(ctx)
	if err != nil {
		return err
	}
	defer finish()
	call, cancel := context.WithTimeout(owned, currentauthority.AttemptLimit)
	defer cancel()
	schema, err := s.coordinator.FleetSchemaForApp(s.store.db, s.appID)
	if err != nil {
		return err
	}
	tx, err := s.store.db.AcquireConn(call)
	if err != nil {
		return err
	}
	s.retainReceipt()

	var written *NamespaceKnownOutcome
	defer func() {
		cleanupErr := s.cleanupReceipt(tx)
		outErr = errors.Join(outErr, cleanupErr)
		if cleanupErr != nil || written == nil || !errors.Is(outErr, currentauthority.ErrCommitUnknown) {
			return
		}
		check, cancel := context.WithTimeout(owned, currentauthority.AttemptLimit)
		defer cancel()
		var primary bool
		if primaryErr := s.store.db.QueryRow(check, `SELECT NOT pg_catalog.pg_is_in_recovery()`).Scan(&primary); primaryErr != nil || !primary {
			outErr = errors.Join(outErr, primaryErr)
			return
		}
		saved, recoveryErr := s.store.getNamespaceAttemptOutcomeInSchema(check, schema, observed.OrgID, observed.ID, written.AttemptID)
		if recoveryErr == nil && namespaceEqual(saved, written) {
			outErr = nil
		} else {
			outErr = errors.Join(outErr, recoveryErr)
		}
	}()
	if _, err = tx.Exec(call, "BEGIN"); err != nil {
		return err
	}
	if _, err = tx.Exec(call, `SELECT pg_catalog.set_config('lock_timeout','2000ms',true),pg_catalog.set_config('statement_timeout','5000ms',true),pg_catalog.set_config('idle_in_transaction_session_timeout','5000ms',true)`); err != nil {
		return err
	}
	var actualSchema, replicationRole string
	var primary bool
	if sessionErr := tx.QueryRow(call, `SELECT pg_catalog.current_schema(),NOT pg_catalog.pg_is_in_recovery(),pg_catalog.current_setting('session_replication_role')`).Scan(&actualSchema, &primary, &replicationRole); sessionErr != nil {
		return sessionErr
	}
	if actualSchema != schema || !primary || replicationRole != "origin" {
		return currentauthority.ErrUnavailable
	}
	table := func(name string) string { return namespaceTable(schema, name) }
	// The original attempt, not a fresh grant, is this receipt's authority.
	var input, result []byte
	var digest, attemptID string
	var at time.Time
	var nanos int16
	if err = tx.QueryRow(call, `SELECT id,input_digest,input,result,authorized_at,authorized_at_ns FROM `+table("loomplane_fleet_namespace_attempts")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2 AND id OPERATOR(pg_catalog.=) $3 AND input_digest OPERATOR(pg_catalog.=) $4`, observed.OrgID, observed.ID, observed.NamespaceAttemptID, observed.NamespaceInputDigest).Scan(&attemptID, &digest, &input, &result, &at, &nanos); err != nil {
		return storeError(err)
	}

	var saved namespaceEvidence[namespaceAction]
	if evidenceDecodeErr := json.Unmarshal(input, &saved); evidenceDecodeErr != nil {
		return evidenceDecodeErr
	}
	if saved.Value.AppID != s.appID {
		return ErrConflict
	}
	confirmed, err := decodeNamespaceAttempt(saved.Value, digest, input, result, at, nanos)
	if err != nil {
		return err
	}
	before := confirmed.Operation
	if before.NamespaceAttemptID != observed.NamespaceAttemptID || before.NamespaceInputDigest != observed.NamespaceInputDigest || before.ID != observed.ID || before.OrgID != observed.OrgID || before.CloudID != observed.CloudID || before.ResourceID != observed.ResourceID || before.Digest != observed.Digest || before.Revision != observed.Revision || before.FencingEpoch != observed.FencingEpoch || before.LeaseOwner != observed.LeaseOwner || !namespaceEqual(before.Input, observed.Input) || before.Enrollment == nil || before.Enrollment.AttachmentID != observed.Enrollment.AttachmentID || before.Enrollment.RuntimeConnectionID != observed.Enrollment.RuntimeConnectionID || before.Enrollment.TargetID != observed.Enrollment.TargetID || len(before.Steps) != 2 || before.Steps[1].AttemptedAt == nil || before.Steps[1].Name != observed.Steps[1].Name || before.Steps[1].RequestDigest != observed.Steps[1].RequestDigest || !namespaceEqual(before.Steps[1].AttemptedAt, observed.Steps[1].AttemptedAt) {
		return ErrConflict
	}
	if observed.Enrollment.State.Isolation != nil {
		want, policyErr := observed.Input.Attachment.InstancePolicy.Digest()
		if policyErr != nil || observed.Enrollment.State.Isolation.Digest != want {
			return ErrConflict
		}
	}
	next, changed, err := namespaceOutcomeAdvance(before.Enrollment.State, observed.Enrollment.State)
	if err != nil {
		return err
	}
	reason := ""
	switch observed.Status {
	case "needs_attention", "failed":
		reason = "operator_recovery_required"
	case "waiting_provider":
		reason = "provider_observation_pending"
	}
	if !changed && reason == "" {
		return nil
	}
	var stepBody []byte
	var stepState, requestDigest string
	if err = tx.QueryRow(call, `SELECT state,request_digest,body FROM `+table("loomplane_cluster_operation_steps")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2 AND name OPERATOR(pg_catalog.=) $3 FOR UPDATE`, observed.OrgID, observed.ID, before.Steps[1].Name).Scan(&stepState, &requestDigest, &stepBody); err != nil {
		return storeError(err)
	}
	var step struct {
		ClusterStep
		Receipts map[string]NamespaceKnownOutcome `json:"namespace_receipts,omitempty"`
	}
	if len(stepBody) > 256*1024 {
		return ErrInvalid
	}
	if stepDecodeErr := json.Unmarshal(stepBody, &step); stepDecodeErr != nil {
		return stepDecodeErr
	}
	if requestDigest != before.Digest || (stepState != "attempted" && stepState != "observed" && stepState != "completed") || step.Name != before.Steps[1].Name || step.RequestDigest != before.Steps[1].RequestDigest || !namespaceEqual(step.AttemptedAt, before.Steps[1].AttemptedAt) {
		return ErrConflict
	}
	if step.Receipts == nil {
		step.Receipts = make(map[string]NamespaceKnownOutcome)
	}
	if previous, ok := step.Receipts[attemptID]; ok {
		if previous.InputDigest != digest || previous.Generation != confirmed.Record.CurrentGeneration || previous.OwnershipEpoch != confirmed.Record.Pins.OwnershipEpoch {
			return ErrConflict
		}
		if _, _, err = namespaceOutcomeAdvance(previous.State, next); err != nil {
			return err
		}
	}
	exact, err := joinNamespaceAuthorizationInstant(at, nanos)
	if err != nil {
		return err
	}
	receipt := NamespaceKnownOutcome{NamespaceRevision: confirmed.Record.Revision, Reason: reason, AuthorizedAt: exact, AttemptID: attemptID, InputDigest: digest, Generation: confirmed.Record.CurrentGeneration, OwnershipEpoch: confirmed.Record.Pins.OwnershipEpoch, State: next}
	step.Receipts[attemptID] = receipt
	if len(step.Receipts) > 64 {
		return ErrInvalid
	}
	receiptJSON, err := json.Marshal(step.Receipts)
	if err != nil {
		return err
	}
	if len(receiptJSON) > 240*1024 {
		return ErrInvalid
	}
	if receiptWriteErr := namespaceExecOne(call, tx, `UPDATE `+table("loomplane_cluster_operation_steps")+` SET body=pg_catalog.jsonb_set(body,'{namespace_receipts}',$4::pg_catalog.jsonb,true) WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2 AND name OPERATOR(pg_catalog.=) $3 AND request_digest OPERATOR(pg_catalog.=) $5 AND body OPERATOR(pg_catalog.=) $6::pg_catalog.jsonb AND state OPERATOR(pg_catalog.=) $7`, observed.OrgID, observed.ID, before.Steps[1].Name, receiptJSON, before.Digest, stepBody, stepState); receiptWriteErr != nil {
		return receiptWriteErr
	}
	written = &receipt
	if _, err = tx.Exec(call, "COMMIT"); err != nil {
		return errors.Join(currentauthority.ErrCommitUnknown, err)
	}
	return nil
}

func namespaceOutcomeAdvance(before, observed kubernetes.EnrollmentState) (kubernetes.EnrollmentState, bool, error) {
	if observed.AccessInput != nil || observed.AccessReceipt != nil {
		return observed, false, ErrInvalid
	}
	if before.NamespaceUID != "" && observed.NamespaceUID != before.NamespaceUID || before.ServiceAccountUID != "" && observed.ServiceAccountUID != before.ServiceAccountUID {
		return observed, false, ErrConflict
	}
	changed := before.NamespaceUID != observed.NamespaceUID || before.ServiceAccountUID != observed.ServiceAccountUID
	if observed.NamespaceUID != "" && before.NamespaceUID == "" && !before.Attempted["Namespace"] {
		return observed, false, ErrConflict
	}
	for kind, uid := range before.Objects {
		if observed.Objects[kind] != uid {
			return observed, false, ErrConflict
		}
	}
	for kind, uid := range observed.Objects {
		if uid == "" {
			return observed, false, ErrInvalid
		}
		if before.Objects[kind] == "" {
			if !before.Attempted[kind] {
				return observed, false, ErrConflict
			}
			changed = true
		}
	}
	if observed.ServiceAccountUID != "" && observed.ServiceAccountUID != observed.Objects["ServiceAccount"] {
		return observed, false, ErrConflict
	}
	if before.Isolation != nil {
		if observed.Isolation == nil || before.Isolation.Digest != observed.Isolation.Digest {
			return observed, false, ErrConflict
		}
		for kind, uid := range before.Isolation.Objects {
			if observed.Isolation.Objects[kind] != uid {
				return observed, false, ErrConflict
			}
		}
	}
	if observed.Isolation != nil {
		for kind, uid := range observed.Isolation.Objects {
			if uid == "" || before.Isolation == nil || !before.Isolation.Attempted[kind] {
				return observed, false, ErrConflict
			}
			if before.Isolation.Objects[kind] != uid {
				changed = true
			}
		}
	}
	// Attempt intent belongs to a fresh checkpoint, never to receipt recovery.
	raw, err := json.Marshal(observed)
	if err != nil {
		return observed, false, err
	}
	var next kubernetes.EnrollmentState
	if stateDecodeErr := json.Unmarshal(raw, &next); stateDecodeErr != nil {
		return next, false, stateDecodeErr
	}
	next.Attempted = make(map[string]bool, len(before.Attempted))
	for key, value := range before.Attempted {
		next.Attempted[key] = value
	}
	if next.Isolation != nil {
		next.Isolation.Attempted = make(map[string]bool)
		if before.Isolation != nil {
			for key, value := range before.Isolation.Attempted {
				next.Isolation.Attempted[key] = value
			}
		}
	}
	return next, changed, nil
}

func (s *PgStore) readNamespaceOutcomes(ctx context.Context, org, operation string) (map[string]NamespaceKnownOutcome, error) {
	var schema string
	if err := s.db.QueryRow(ctx, `SELECT pg_catalog.current_schema()`).Scan(&schema); err != nil {
		return nil, err
	}
	return s.readNamespaceOutcomesInSchema(ctx, schema, org, operation)
}
func (s *PgStore) readNamespaceOutcomesInSchema(ctx context.Context, schema, org, operation string) (out map[string]NamespaceKnownOutcome, outErr error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT body OPERATOR(pg_catalog.->) 'namespace_receipts' FROM `+namespaceTable(schema, "loomplane_cluster_operation_steps")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2 AND name OPERATOR(pg_catalog.=) 'enroll-runtime'`, org, operation).Scan(&raw)
	if err != nil {
		return nil, storeError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if len(raw) > 256*1024 {
		return nil, ErrInvalid
	}
	if receiptsDecodeErr := json.Unmarshal(raw, &out); receiptsDecodeErr != nil {
		return nil, receiptsDecodeErr
	}
	if len(out) > 64 {
		return nil, ErrInvalid
	}
	for id, receipt := range out {
		if receipt.AttemptID != id || len(receipt.InputDigest) != 64 || receipt.AuthorizedAt.IsZero() {
			return nil, ErrConflict
		}
	}
	ids := make([]string, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	rows, err := s.db.Query(ctx, `SELECT id,input_digest,generation,ownership_epoch,result_namespace_revision,authorized_at,authorized_at_ns FROM `+namespaceTable(schema, "loomplane_fleet_namespace_attempts")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2 AND id OPERATOR(pg_catalog.=) ANY($3::pg_catalog.text[])`, org, operation, ids)
	if err != nil {
		return nil, err
	}
	defer func() { outErr = errors.Join(outErr, rows.Close()) }()
	count := 0
	for rows.Next() {
		var id, digest string
		var generation, epoch, revision int64
		var at time.Time
		var ns int16
		if outcomeScanErr := rows.Scan(&id, &digest, &generation, &epoch, &revision, &at, &ns); outcomeScanErr != nil {
			return nil, outcomeScanErr
		}
		exact, instantErr := joinNamespaceAuthorizationInstant(at, ns)
		if instantErr != nil {
			return nil, instantErr
		}
		item, ok := out[id]
		if !ok || item.InputDigest != digest || item.Generation != generation || item.OwnershipEpoch != epoch || item.NamespaceRevision != revision || !item.AuthorizedAt.Equal(exact) {
			return nil, ErrConflict
		}
		count++
	}
	if outcomesReadErr := rows.Err(); outcomesReadErr != nil {
		return nil, outcomesReadErr
	}
	if count != len(out) {
		return nil, ErrConflict
	}
	return out, nil
}
func (s *PgStore) getNamespaceAttemptOutcome(ctx context.Context, org, operation, attempt string) (*NamespaceKnownOutcome, error) {
	var schema string
	if err := s.db.QueryRow(ctx, `SELECT pg_catalog.current_schema()`).Scan(&schema); err != nil {
		return nil, err
	}
	return s.getNamespaceAttemptOutcomeInSchema(ctx, schema, org, operation, attempt)
}
func (s *PgStore) getNamespaceAttemptOutcomeInSchema(ctx context.Context, schema, org, operation, attempt string) (*NamespaceKnownOutcome, error) {
	outcomes, err := s.readNamespaceOutcomesInSchema(ctx, schema, org, operation)
	if err != nil {
		return nil, err
	}
	out, ok := outcomes[attempt]
	if !ok {
		return nil, ErrNotFound
	}
	return &out, nil
}

func (s *PgStore) GetInstanceNamespaceKnownOutcome(ctx context.Context, org, operation string, generation, epoch int64) (*NamespaceKnownOutcome, error) {
	outcomes, err := s.readNamespaceOutcomes(ctx, org, operation)
	if err != nil {
		return nil, err
	}
	var latest *NamespaceKnownOutcome
	for _, out := range outcomes {
		if out.Generation == generation && out.OwnershipEpoch == epoch && (latest == nil || out.NamespaceRevision > latest.NamespaceRevision) {
			candidate := out
			latest = &candidate
		}
	}
	return latest, nil
}
