package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/projects"
)

// ClusterChange invalidates scoped reads without publishing enrollment receipts.
type ClusterChange struct {
	OperationID string `json:"operation_id"`
	CloudID     string `json:"cloud_id"`
	ResourceID  string `json:"resource_id"`
	Revision    int64  `json:"revision"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
}

func clusterChange(o *ClusterOperation) ClusterChange {
	return ClusterChange{OperationID: o.ID, CloudID: o.CloudID, ResourceID: o.ResourceID, Revision: o.Revision, Status: o.Status, Reason: ClusterPublicReason(o)}
}
func appendClusterEvent(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	raw, err := json.Marshal(clusterChange(o))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT loomplane_append_event($1,'','cloud_cluster.updated',$2,$3)`, o.OrgID, o.ID, raw)
	return err
}
func (s *Service) recordCluster(ctx context.Context, action string, o *ClusterOperation) {
	if s.Audit == nil || o == nil {
		return
	}
	event := clusterChange(o)
	s.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", Principal: o.Principal, Attributes: map[string]any{"operation_id": event.OperationID, "cloud_id": event.CloudID, "resource_id": event.ResourceID, "revision": event.Revision, "status": event.Status, "reason": event.Reason}})
}

type clusterEnrollmentError struct {
	reason string
	cause  error
}

func (e *clusterEnrollmentError) Error() string { return e.reason }
func (e *clusterEnrollmentError) Unwrap() error { return e.cause }
func clusterEnrollmentReason(err error) string {
	var failure *clusterEnrollmentError
	if errors.As(err, &failure) {
		return failure.reason
	}
	return "runtime_setup_incomplete"
}

// ClusterPublicReason never propagates provider or Kubernetes error strings.
func ClusterPublicReason(o *ClusterOperation) string {
	if o == nil {
		return ""
	}
	if o.Error == "" {
		if len(o.Steps) > 1 && o.Steps[1].State == "completed" {
			return "runtime_enrollment_verified"
		}
		if len(o.Steps) > 0 {
			switch o.Steps[0].State {
			case "attempted":
				return "provider_effect_attempted"
			case "observed", "completed":
				return "provider_resource_observed"
			}
		}
		return ""
	}
	switch o.Error {
	case "runtime_bootstrap_unavailable", "runtime_permissions_unavailable", "runtime_namespace_setup_failed", "runtime_token_unavailable", "runtime_setup_incomplete":
		return o.Error
	}
	switch o.Status {
	case "setup_incomplete":
		return "runtime_setup_incomplete"
	case "needs_attention":
		return "operator_recovery_required"
	case "waiting_provider":
		return "provider_observation_pending"
	case "cancelled":
		return "cancelled_resources_may_remain"
	default:
		return "review_operation_state"
	}
}

func (s *Service) recordClusterOutcome(ctx context.Context, action string, o *ClusterOperation, reason string) {
	if o == nil {
		return
	}
	event := clusterChange(o)
	event.Reason = reason
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", Principal: o.Principal, Attributes: map[string]any{"operation_id": event.OperationID, "cloud_id": event.CloudID, "resource_id": event.ResourceID, "revision": event.Revision, "status": event.Status, "reason": reason}})
	}
	if publisher, ok := s.Clusters.(interface {
		PublishClusterChange(context.Context, string, ClusterChange) error
	}); ok {
		if err := publisher.PublishClusterChange(ctx, o.OrgID, event); err != nil && s.Warn != nil {
			s.Warn("Publish cluster outcome", err)
		}
	}
}
func (s *PgStore) PublishClusterChange(ctx context.Context, org string, event ClusterChange) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `SELECT loomplane_append_event($1,'','cloud_cluster.updated',$2,$3)`, org, event.OperationID, raw)
	return err
}
