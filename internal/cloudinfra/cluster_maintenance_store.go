package cloudinfra

import (
	"context"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/pkg/provider"
)

func maintenancePinsTx(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	if !maintenanceKind(o.Kind) || o.Input.Maintenance == nil {
		return ErrInvalid
	}
	var valid bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND revision=$4 AND deleted_at IS NULL AND ownership='managed' AND body->>'observed_revision'=($5::bigint)::text)`, o.OrgID, o.CloudID, o.Input.ResourceID, o.Input.ResourceRevision, o.Input.Maintenance.ObservedRevision).Scan(&valid); checkErr != nil {
		return checkErr
	}
	if !valid {
		return ErrConflict
	}
	if o.Kind == "cluster-upgrade" || o.Kind == "node-pool-delete" || o.Input.Maintenance.DrainDigest != "" {
		var active bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_deployments d JOIN loomplane_runtime_attachments a ON a.org_id=d.org_id AND a.project_id=d.project_id WHERE d.org_id=$1 AND a.cluster_id=$2 AND a.deleted_at IS NULL AND loomplane_plan_uses_cluster(d.org_id,d.plan_id,a.cluster_id) AND d.status NOT IN ('succeeded','failed','cancelled'))`, o.OrgID, o.Input.ResourceID).Scan(&active); checkErr != nil {
			return checkErr
		}
		if active {
			return &ClusterMaintenanceAdmissionError{Reason: "deployment_in_flight"}
		}
	}
	return scalingPinsTx(ctx, tx, o)
}

type ClusterMaintenanceAdmissionError struct{ Reason string }

func (e *ClusterMaintenanceAdmissionError) Error() string {
	return "cluster maintenance admission blocked: " + e.Reason
}
func (e *ClusterMaintenanceAdmissionError) Unwrap() error { return ErrBusy }

func (s *PgStore) ValidateClusterMaintenanceEffect(ctx context.Context, o *ClusterOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	saved, err := clusterOperationTx(ctx, tx, o.OrgID, o.CloudID, o.ID)
	if err != nil {
		return err
	}
	if saved.Revision != o.Revision || saved.FencingEpoch != o.FencingEpoch || saved.LeaseOwner != o.LeaseOwner || saved.CancelRequested || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.After(time.Now()) || len(saved.Steps) == 0 || saved.Steps[0].State != "attempted" {
		return ErrConflict
	}
	if checkErr := clusterPinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if checkErr := maintenancePinsTx(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if o.CapacityTarget.Servers > 0 {
		if checkErr := s.reserveClusterCapacity(ctx, tx, o); checkErr != nil {
			return checkErr
		}
	}
	return tx.Commit()
}

func (s *PgStore) ValidateClusterCreationReceipt(ctx context.Context, r *ManagedResource) error {
	if r.CreationReceipt == nil || r.Observation == nil {
		return ErrConflict
	}
	receipt := r.CreationReceipt
	original, err := s.GetClusterOperation(ctx, r.OrgID, r.CloudID, receipt.OperationID)
	if err != nil {
		return err
	}
	if original.Kind != "create" || original.Input.Spec.OperationID != original.ID || original.ResourceID != r.ID || original.Input.AccountID != r.AccountID || original.Input.Provider != r.Provider || provider.ClusterRequestDigest(original.Input.Spec) != receipt.RequestDigest || receipt.Kind != "create" || receipt.Ref != r.Observation.Ref {
		return ErrConflict
	}
	for _, accepted := range original.Receipts {
		if accepted.Kind == "create" && accepted.OperationID == receipt.OperationID && accepted.Ref == receipt.Ref && accepted.RequestDigest == receipt.RequestDigest {
			return nil
		}
	}
	return ErrConflict
}
