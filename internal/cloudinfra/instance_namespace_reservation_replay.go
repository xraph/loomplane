package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/security"
)

// replayReservation accepts only the freshly validated public request. Generated
// presentation times come from the immutable input, whose full digest is checked.
func (s *InstanceNamespaceTransactions) replayReservation(ctx context.Context, record InstanceNamespaceRecord, input ClusterInput, actor security.Principal) (*ClusterOperation, bool, error) {
	action, found, err := s.readOriginalReservation(ctx, record.OrgID, record.OperationID)
	if err != nil || !found {
		return nil, found, err
	}
	if !namespaceEqual(action.Record, record) || !namespaceEqual(action.Operation.Input, input) || !namespaceEqual(action.Principal, actor) || !action.Operation.ExpiresAt.Equal(record.CreationAuthority.ExpiresAt) {
		return nil, true, ErrConflict
	}
	// The normal historical path verifies every typed journal binding again.
	result, err := s.run(ctx, action)
	if err != nil {
		return nil, true, err
	}
	return &result.Operation, true, nil
}

// readOriginalReservation verifies installation identity before loading immutable
// evidence. Reading it never grants fresh effect permission.
func (s *InstanceNamespaceTransactions) readOriginalReservation(ctx context.Context, org, operation string) (namespaceAction, bool, error) {
	if s == nil || s.coordinator == nil || s.store == nil || s.appID == "" {
		return namespaceAction{}, false, currentauthority.ErrUnavailable
	}
	owned, finish, err := s.beginNamespaceWork(ctx)
	if err != nil {
		return namespaceAction{}, false, err
	}
	defer finish()
	schema, err := s.coordinator.FleetSchemaForApp(s.store.db, s.appID)
	if err != nil {
		return namespaceAction{}, false, err
	}
	call, cancel := context.WithTimeout(owned, currentauthority.AttemptLimit)
	defer cancel()
	var rawInput, rawResult []byte
	var digest string
	var at time.Time
	var nanos int16
	var primary bool
	err = s.store.db.QueryRow(call, `SELECT j.input_digest,j.input,j.result,j.authorized_at,j.authorized_at_ns,NOT pg_catalog.pg_is_in_recovery() FROM `+namespaceTable(schema, "loomplane_fleet_namespaces")+` n JOIN `+namespaceTable(schema, "loomplane_fleet_namespace_attempts")+` j ON j.org_id OPERATOR(pg_catalog.=) n.org_id AND j.operation_id OPERATOR(pg_catalog.=) n.operation_id AND j.id OPERATOR(pg_catalog.=) n.creation_attempt_id WHERE n.org_id OPERATOR(pg_catalog.=) $1 AND n.operation_id OPERATOR(pg_catalog.=) $2 AND j.kind OPERATOR(pg_catalog.=) 'reserve'`, org, operation).Scan(&digest, &rawInput, &rawResult, &at, &nanos, &primary)
	if errors.Is(storeError(err), ErrNotFound) {
		return namespaceAction{}, false, nil
	}
	if err != nil || !primary {
		return namespaceAction{}, false, errors.Join(currentauthority.ErrUnavailable, err)
	}
	var original namespaceEvidence[namespaceAction]
	if err := json.Unmarshal(rawInput, &original); err != nil {
		return namespaceAction{}, true, err
	}
	action := original.Value
	if action.Kind != "reserve" || action.AppID != s.appID || action.Record.OrgID != org || action.Operation.ID != operation {
		return namespaceAction{}, true, ErrConflict
	}
	if _, err := decodeNamespaceAttempt(action, digest, rawInput, rawResult, at, nanos); err != nil {
		return namespaceAction{}, true, err
	}
	return action, true, nil
}
