package cloudinfra

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
)

// InstanceNamespaceTransactions owns the complete Fleet namespace transaction.
// The service and adapter share one Fleet service for snapshot and status reads.
var errNamespaceHistorical = errors.New("namespace attempt already committed")

type InstanceNamespaceTransactions struct {
	receipts    namespaceReceiptLifecycle
	coordinator *currentauthority.FleetAuthority
	fleets      *fleets.Service
	store       *PgStore
	appID       string
}

func NewInstanceNamespaceTransactions(c *currentauthority.FleetAuthority, f *fleets.Service, s *PgStore, appID string) *InstanceNamespaceTransactions {
	return &InstanceNamespaceTransactions{coordinator: c, fleets: f, store: s, appID: appID}
}

type namespaceAction struct {
	Kind                      string                   `json:"kind"`
	AttemptID                 string                   `json:"attempt_id"`
	AppID                     string                   `json:"app_id"`
	ExpectedNamespaceRevision int64                    `json:"expected_namespace_revision"`
	ExpectedOperationRevision int64                    `json:"expected_operation_revision"`
	Record                    InstanceNamespaceRecord  `json:"record"`
	Prior                     *InstanceNamespaceRecord `json:"prior,omitempty"`
	Snapshot                  fleets.InstanceSnapshot  `json:"snapshot"`
	Operation                 ClusterOperation         `json:"operation"`
	Principal                 security.Principal       `json:"principal"`
	Attachment                *RuntimeAttachment       `json:"attachment,omitempty"`
	Connection                *connections.Connection  `json:"connection,omitempty"`
}

type namespaceResult struct {
	Record    InstanceNamespaceRecord `json:"record"`
	Operation ClusterOperation        `json:"operation"`
	Principal security.Principal      `json:"principal"`
	Snapshot  fleets.InstanceSnapshot `json:"snapshot"`
}

type namespaceEvidence[T any] struct {
	AuthorizedAt   string `json:"authorized_at"`
	AuthorizedAtNS int16  `json:"authorized_at_ns"`
	Value          T      `json:"value"`
}

// freezeNamespaceAction detaches every pointer and map before RunFleet starts.
// ClusterOperation.Principal has json:"-", so its full value is bound separately.
func freezeNamespaceAction(in namespaceAction) (namespaceAction, string, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return namespaceAction{}, "", err
	}
	if len(raw) > 1024*1024 {
		return namespaceAction{}, "", ErrInvalid
	}
	var out namespaceAction
	if decodeErr := json.Unmarshal(raw, &out); decodeErr != nil {
		return out, "", decodeErr
	}
	out.Operation.Principal = out.Record.CreationAuthority.Principal
	if validationErr := out.validate(); validationErr != nil {
		return out, "", validationErr
	}
	sum := sha256.Sum256(raw)
	return out, hex.EncodeToString(sum[:]), nil
}

func (in namespaceAction) validate() error {
	r, o, a, snap := in.Record, in.Operation, in.Record.CurrentAuthority, in.Snapshot
	if err := supportedNamespaceSnapshot(snap); err != nil {
		return err
	}
	if in.AttemptID == "" || in.AppID == "" || o.Kind != InstanceNamespaceKind || o.Input.Kind != InstanceNamespaceKind || o.SchemaVersion != 1 || o.Input.Attachment == nil || o.Input.Attachment.Instance == nil || o.Input.Attachment.InstancePolicy == nil || o.Input.Attachment.RuntimeAccess != nil || o.Input.RuntimeAccess != nil || o.RuntimeAccess != nil {
		return ErrInvalid
	}
	if o.ID != r.OperationID || o.OrgID != r.OrgID || o.CloudID != r.CloudID || o.ResourceID != r.ClusterID || o.Input.Attachment.ProjectID != r.ProjectID || o.Input.Attachment.Namespace != r.Namespace || *o.Input.Attachment.Instance != r.Pins || o.Digest != clusterDigest(o.Input) || !namespaceEqual(in.Principal, a.Principal) || !namespaceEqual(o.Principal, r.CreationAuthority.Principal) || o.CancelRequested {
		return ErrConflict
	}
	if _, err := namespaceRecordArgs(r); err != nil {
		return err
	}
	if snap.InstanceID != r.Pins.InstanceID || snap.EnvironmentID != r.Pins.EnvironmentID || snap.FleetID != r.Pins.FleetID || snap.Generation != r.CurrentGeneration || snap.OwnershipEpoch != r.Pins.OwnershipEpoch || snap.Digest != r.CurrentSnapshotDigest || !namespaceEqual(snap, r.Snapshot) || r.QualificationExpiresAt.IsZero() {
		return ErrConflict
	}
	switch in.Kind {
	case "reserve":
		if in.ExpectedNamespaceRevision != 0 || in.ExpectedOperationRevision != 0 || o.Revision != 1 || o.Status != "accepted" || r.Revision != 0 || r.CreationAttemptID != "" || r.CurrentAttemptID != "" || in.Prior != nil || !namespaceEqual(r.CreationAuthority, a) {
			return ErrInvalid
		}
	case "checkpoint", "publish":
		if in.Kind == "checkpoint" && o.Status == "succeeded" {
			return ErrInvalid
		}
		if in.ExpectedNamespaceRevision < 1 || r.Revision != in.ExpectedNamespaceRevision || in.ExpectedOperationRevision < 1 || o.Revision != in.ExpectedOperationRevision+1 || in.Prior != nil {
			return ErrInvalid
		}
	case "renew":
		if in.Prior == nil || in.ExpectedNamespaceRevision < 1 || r.Revision != in.ExpectedNamespaceRevision || in.ExpectedOperationRevision < 1 || o.Revision != in.ExpectedOperationRevision || o.Status != "succeeded" {
			return ErrInvalid
		}
		identity := r
		identity.CurrentAuthority = in.Prior.CurrentAuthority
		identity.CurrentGeneration = in.Prior.CurrentGeneration
		identity.CurrentSnapshotDigest = in.Prior.CurrentSnapshotDigest
		identity.Snapshot = in.Prior.Snapshot
		if !namespaceEqual(identity, *in.Prior) || r.CurrentGeneration < in.Prior.CurrentGeneration {
			return ErrConflict
		}
	default:
		return ErrInvalid
	}
	if in.Kind == "publish" {
		if in.Attachment == nil || in.Connection == nil || o.Status != "succeeded" || o.Enrollment == nil {
			return ErrInvalid
		}
		if _, err := instanceNamespaceReceipt(&o); err != nil {
			return err
		}
	} else if in.Attachment != nil || in.Connection != nil {
		return ErrInvalid
	}
	return nil
}

func namespaceEqual(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}

func (s *InstanceNamespaceTransactions) run(ctx context.Context, action namespaceAction) (*namespaceResult, error) {
	if s == nil || s.coordinator == nil || s.fleets == nil || s.store == nil || s.appID == "" {
		return nil, currentauthority.ErrUnavailable
	}
	owned, finish, err := s.beginNamespaceWork(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(owned, cancel)
	defer stop()
	if _, installationErr := s.coordinator.FleetSchemaForApp(s.store.db, s.appID); installationErr != nil {
		return nil, installationErr
	}
	action.AppID = s.appID
	if action.AttemptID == "" {
		action.AttemptID = namespaceAttemptIdentity(action)
	}
	in, digest, err := freezeNamespaceAction(action)
	if err != nil {
		return nil, err
	}
	binding := currentauthority.FleetBinding{Actor: in.Principal, AppID: in.AppID, Operation: "fleet.namespace." + in.Kind, RecordID: in.Operation.ID, AttemptID: in.AttemptID, InputDigest: digest}
	var result *namespaceResult
	var schema string
	err = s.coordinator.RunFleet(call, binding, func(call context.Context, attempt *currentauthority.Attempt) error {
		schema = attempt.Schema()
		tx := attempt.Tx()
		var recorded bool
		if historyReadErr := tx.QueryRow(call, `SELECT EXISTS(SELECT 1 FROM `+namespaceTable(schema, "loomplane_fleet_namespace_attempts")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND operation_id OPERATOR(pg_catalog.=) $2 AND id OPERATOR(pg_catalog.=) $3)`, in.Record.OrgID, in.Operation.ID, in.AttemptID).Scan(&recorded); historyReadErr != nil {
			return historyReadErr
		}
		if recorded {
			return errNamespaceHistorical
		}
		if cloudLockErr := lockNamespaceCloud(call, tx, schema, in); cloudLockErr != nil {
			return cloudLockErr
		}
		proof, workErr := fleets.LockInstanceAuthority(call, attempt, in.Record.CurrentAuthority, in.Snapshot)
		if workErr != nil {
			return workErr
		}
		locked, workErr := loadNamespaceAction(call, tx, schema, in)
		if workErr != nil {
			return workErr
		}
		decision, workErr := attempt.EvaluateFleet(call)
		if workErr != nil {
			return workErr
		}
		return attempt.StageFleet(call, decision, binding, func(write context.Context, tx driver.Tx, at time.Time) error {
			if proofErr := proof.ValidateAt(at); proofErr != nil {
				return proofErr
			}
			if lockedErr := locked.validateAt(in, at); lockedErr != nil {
				return lockedErr
			}
			var writeErr error
			result, writeErr = writeNamespaceAction(write, tx, schema, in, digest, at)
			return writeErr
		})
	})
	if errors.Is(err, currentauthority.ErrCommitUnknown) || errors.Is(err, errNamespaceHistorical) {
		// A primary read can confirm this attempt, but never stages another effect.
		return s.store.reconcileNamespaceAttempt(owned, schema, in, digest)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *InstanceNamespaceTransactions) Reserve(ctx context.Context, record InstanceNamespaceRecord, o *ClusterOperation) (*ClusterOperation, error) {
	if o == nil {
		return nil, ErrInvalid
	}
	result, err := s.run(ctx, namespaceAction{Kind: "reserve", Record: record, Snapshot: record.Snapshot, Operation: *o, Principal: record.CurrentAuthority.Principal})
	if err != nil {
		return nil, err
	}
	return &result.Operation, nil
}
func (s *InstanceNamespaceTransactions) Checkpoint(ctx context.Context, record InstanceNamespaceRecord, o *ClusterOperation, expected int64) error {
	if o == nil {
		return ErrInvalid
	}
	result, err := s.run(ctx, namespaceAction{Kind: "checkpoint", ExpectedNamespaceRevision: record.Revision, ExpectedOperationRevision: expected, Record: record, Snapshot: record.Snapshot, Operation: *o, Principal: record.CurrentAuthority.Principal})
	if err == nil {
		*o = result.Operation
	}
	return err
}
func (s *InstanceNamespaceTransactions) Renew(ctx context.Context, prior, next InstanceNamespaceRecord, o *ClusterOperation) error {
	if o == nil {
		return ErrInvalid
	}
	// Renewal uses the new Fleet key while preserving the operation's creation actor.
	operation := *o
	operation.Principal = next.CreationAuthority.Principal
	_, err := s.run(ctx, namespaceAction{Kind: "renew", ExpectedNamespaceRevision: prior.Revision, ExpectedOperationRevision: o.Revision, Record: next, Prior: &prior, Snapshot: next.Snapshot, Operation: operation, Principal: next.CurrentAuthority.Principal})
	return err
}
func (s *InstanceNamespaceTransactions) Publish(ctx context.Context, record InstanceNamespaceRecord, o *ClusterOperation, a *RuntimeAttachment, c *connections.Connection) error {
	if o == nil {
		return ErrInvalid
	}
	operation := *o
	operation.Revision++
	result, err := s.run(ctx, namespaceAction{Kind: "publish", ExpectedNamespaceRevision: record.Revision, ExpectedOperationRevision: o.Revision, Record: record, Snapshot: record.Snapshot, Operation: operation, Principal: record.CurrentAuthority.Principal, Attachment: a, Connection: c})
	if err == nil {
		*o = result.Operation
	}
	return err
}

// Intent identity survives a process restart and fresh presentation timestamps.
// The separate full digest still refuses a changed payload for that intent.
func namespaceAttemptIdentity(in namespaceAction) string {
	raw, err := json.Marshal([]any{in.AppID, in.Kind, in.Record.OrgID, in.Record.OperationID, in.Record.CurrentAuthority.ID, in.Record.CurrentGeneration, in.Record.Pins.OwnershipEpoch, in.ExpectedNamespaceRevision, in.ExpectedOperationRevision, in.Operation.FencingEpoch})
	if err != nil {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, raw).String()
}

func supportedNamespaceSnapshot(snapshot fleets.InstanceSnapshot) error {
	if len(snapshot.Template.Jobs) > 0 || len(snapshot.Template.RequiredCapabilities) > 0 {
		return currentauthority.ErrUnavailable
	}
	return nil
}
