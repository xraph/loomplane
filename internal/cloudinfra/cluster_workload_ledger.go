package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

// ClusterWorkloadInput pins a reviewed maximum, including every guaranteed HPA
// replica. Runtime release proof is bound after execution creates its receipt.
type ClusterWorkloadInput struct {
	Native          *ClusterNativeWorkload `json:"native,omitempty"`
	ID              string                 `json:"id"`
	OrgID           string                 `json:"org_id"`
	CloudID         string                 `json:"cloud_id"`
	ResourceID      string                 `json:"resource_id"`
	TargetID        string                 `json:"target_id"`
	AttachmentID    string                 `json:"attachment_id"`
	ProjectID       string                 `json:"project_id"`
	EnvironmentID   string                 `json:"environment_id"`
	ConnectionID    string                 `json:"connection_id"`
	PlanID          string                 `json:"plan_id"`
	ActionID        string                 `json:"action_id"`
	Service         string                 `json:"service"`
	RuntimeRef      string                 `json:"runtime_ref"`
	RuntimeRevision string                 `json:"runtime_revision"`
	ReleaseID       string                 `json:"release_id"`
	ReviewDigest    string                 `json:"review_digest"`
	TrustRevision   int64                  `json:"trust_revision"`
	Maximum         ClusterWorkloadVector  `json:"maximum"`
}

type ClusterWorkloadHold struct {
	Input     ClusterWorkloadInput          `json:"input"`
	CreatedAt time.Time                     `json:"created_at"`
	Attempt   *WorkloadAttempt              `json:"attempt,omitempty"`
	BindingID string                        `json:"binding_id,omitempty"`
	Receipt   *kubernetes.ControllerReceipt `json:"receipt,omitempty"`
}

// ClusterWorkloadGuard must lock and compare the immutable approved demand to
// Input, including its maximum, release identity and target. It must also verify
// scheduler constraints (taints, architecture, GPUs and storage reachability).
// Planning installs this guard inside its transaction; API input cannot supply it.
// Native envelope supply and first-effect orchestration belong to that caller.
type ClusterWorkloadGuard func(context.Context, driver.Tx, ClusterWorkloadInput) error

func (s *PgStore) ReserveClusterWorkload(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, guard ClusterWorkloadGuard) (*ClusterWorkloadHold, error) {
	if tx == nil || guard == nil || in.ID == "" || in.ReviewDigest == "" || in.Service == "" || in.ActionID == "" || in.Maximum.Pods <= 0 || in.TrustRevision <= 0 {
		return nil, ErrInvalid
	}
	if (in.RuntimeRef != "" || in.RuntimeRevision != "" || in.ReleaseID != "") && (in.ReleaseID == "" || in.ReleaseID != kubernetes.WorkloadReleaseID(in.RuntimeRef, in.RuntimeRevision)) {
		return nil, ErrInvalid
	}
	if _, err := clusterWorkloadUsage(in.Maximum, nil); err != nil {
		return nil, err
	}
	if err := lockCapacityCloud(ctx, tx, in.OrgID, in.CloudID); err != nil {
		return nil, err
	}
	snapshot, err := clusterWorkloadScope(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if checkErr := guard(ctx, tx, in); checkErr != nil {
		return nil, checkErr
	}
	var additional ClusterWorkloadVector
	if in.Native != nil {
		additional, err = s.nativeWorkloadSupply(ctx, tx, in, snapshot, additional)
		if err != nil {
			return nil, err
		}
	}
	holds, err := clusterWorkloadHolds(ctx, tx, in.OrgID, in.ResourceID)
	if err != nil {
		return nil, err
	}
	for _, hold := range holds {
		if hold.Input.ID == in.ID || hold.Input.PlanID == in.PlanID && hold.Input.Service == in.Service {
			if !reflect.DeepEqual(hold.Input, in) {
				return nil, ErrConflict
			}
			return &hold, nil
		}
	}
	usage, supply, err := clusterWorkloadAdmission(ctx, tx, snapshot, holds)
	if err != nil {
		return nil, err
	}
	if in.Native != nil {
		supply, err = clusterWorkloadUsage(supply, []clusterWorkloadCoverage{{Maximum: additional}})
		if err != nil {
			return nil, err
		}
	}
	usage, err = clusterWorkloadUsage(usage, []clusterWorkloadCoverage{{Maximum: in.Maximum}})
	if err != nil {
		return nil, err
	}
	requested, available := workloadDimensions(usage), workloadDimensions(supply)
	for i := range requested {
		if requested[i] > available[i] {
			return nil, &CapacityAdmissionError{Block: CapacityBlock{Reason: "workload_capacity"}}
		}
	}
	hold := &ClusterWorkloadHold{Input: in, CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(hold)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cluster_workload_holds(org_id,cloud_id,resource_id,id,plan_id,service,release_id,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, in.OrgID, in.CloudID, in.ResourceID, in.ID, in.PlanID, in.Service, in.ReleaseID, raw)
	return hold, storeError(err)
}

func clusterWorkloadScope(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput) (ClusterCapacitySnapshot, error) {
	var out ClusterCapacitySnapshot
	resource, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, in.OrgID, in.CloudID, in.ResourceID))
	if err != nil {
		return out, err
	}
	if clusterCapacityIdentityInvalid(resource) || resource.Kind != "cluster" {
		return out, ErrConflict
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT true FROM loomplane_runtime_attachments a
 JOIN loomplane_cloud_connections c ON c.org_id=a.org_id AND c.id=a.runtime_connection_id
 JOIN loomplane_cloud_targets t ON t.org_id=a.org_id AND t.cloud_id=a.cloud_id AND t.managed_resource_id=a.cluster_id AND t.connection_id=a.runtime_connection_id AND t.project_id=a.project_id
 JOIN loomplane_plans p ON p.org_id=a.org_id AND p.project_id=a.project_id
 WHERE a.org_id=$1 AND a.cloud_id=$2 AND a.cluster_id=$3 AND a.id=$4 AND a.project_id=$5 AND a.runtime_connection_id=$6 AND a.deleted_at IS NULL
 AND t.id=$7 AND p.id=$8 AND p.environment_id=$9 AND p.status='approved'
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') action WHERE action->>'id'=$11 AND action->>'name'=$12 AND action->>'subject'='service')
 AND c.cloud='kubernetes' AND c.status='ready' AND a.body->>'trust_revision'=$10
 AND c.details->>'namespace_uid'=a.body->>'namespace_uid' AND c.details->>'cluster_ca'=a.body->>'trust_fingerprint'
 AND c.details->>'managed_attachment_id'=a.id AND c.details->>'managed_resource_id'=a.cluster_id FOR SHARE OF a,c,t,p`, in.OrgID, in.CloudID, in.ResourceID, in.AttachmentID, in.ProjectID, in.ConnectionID, in.TargetID, in.PlanID, in.EnvironmentID, strconv.FormatInt(in.TrustRevision, 10), in.ActionID, in.Service).Scan(&valid)
	if err != nil {
		return out, err
	}
	if !valid {
		return out, ErrConflict
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cluster_capacity_inventory WHERE org_id=$1 AND resource_id=$2 AND saved_revision=issued_revision`, in.OrgID, in.ResourceID).Scan(&raw)
	if err != nil {
		return out, storeError(err)
	}
	if json.Unmarshal(raw, &out) != nil {
		return out, ErrInvalid
	}
	if !out.Fresh(time.Now()) || !out.WorkloadsComplete || out.ResourceFingerprint != retirementResourceFingerprint(resource) {
		if in.Native != nil {
			return out, nativeTemplateBlock()
		}
		return out, &CapacityAdmissionError{Block: CapacityBlock{Reason: "workload_inventory_incomplete", Resource: "workload", CloudID: in.CloudID, TargetID: in.TargetID, Retryable: true}}
	}
	return out, nil
}

func clusterWorkloadHolds(ctx context.Context, tx driver.Tx, org, resource string) ([]ClusterWorkloadHold, error) {
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_cluster_workload_holds WHERE org_id=$1 AND resource_id=$2 AND NOT released ORDER BY id`, org, resource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClusterWorkloadHold
	for rows.Next() {
		var raw []byte
		var h ClusterWorkloadHold
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func clusterWorkloadAdmission(ctx context.Context, tx driver.Tx, snapshot ClusterCapacitySnapshot, holds []ClusterWorkloadHold) (used, supply ClusterWorkloadVector, err error) {
	var observed ClusterWorkloadVector
	for _, node := range snapshot.Nodes {
		if !node.Ready || node.Unschedulable {
			continue
		}
		m := node.Allocatable
		if m.CPUMillis == nil || m.MemoryBytes == nil || m.EphemeralStorageBytes == nil || m.Pods == nil {
			return observed, supply, ErrConflict
		}
		v := ClusterWorkloadVector{CPUMillis: *m.CPUMillis, MemoryBytes: *m.MemoryBytes, EphemeralStorageBytes: *m.EphemeralStorageBytes, Pods: *m.Pods}
		supply, err = clusterWorkloadUsage(supply, []clusterWorkloadCoverage{{Maximum: v}})
		if err != nil {
			return observed, supply, err
		}
	}
	seen := map[string]bool{}
	for _, w := range snapshot.Workloads {
		if w.PodUID == "" || seen[w.PodUID] {
			return observed, supply, ErrInvalid
		}
		seen[w.PodUID] = true
		observed, err = clusterWorkloadUsage(observed, []clusterWorkloadCoverage{{Maximum: w.Requests}})
		if err != nil {
			return observed, supply, err
		}
	}
	coverage := make([]clusterWorkloadCoverage, 0, len(holds))
	credited := map[string]bool{}
	for _, hold := range holds {
		c := clusterWorkloadCoverage{Maximum: hold.Input.Maximum}
		trusted, checkErr := clusterWorkloadReceiptCurrent(ctx, tx, hold)
		if checkErr != nil {
			return observed, supply, checkErr
		}
		if trusted {
			for _, w := range snapshot.Workloads {
				r := hold.Receipt
				if credited[w.PodUID] || w.NamespaceUID != r.NamespaceUID || w.DeploymentUID != r.DeploymentUID || w.DeploymentGeneration != r.DeploymentGeneration || w.ReplicaSetUID == "" || w.ReleaseID != r.ReleaseID {
					continue
				}
				credited[w.PodUID] = true
				c.Observed, err = clusterWorkloadUsage(c.Observed, []clusterWorkloadCoverage{{Maximum: w.Requests}})
				if err != nil {
					return observed, supply, err
				}
			}
		}
		coverage = append(coverage, c)
	}
	used, err = clusterWorkloadUsage(observed, coverage)
	return used, supply, err
}

func (s *PgStore) AttachClusterWorkloadAttempt(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, attempt WorkloadAttempt) error {
	if attempt.Org != in.OrgID || attempt.Project != in.ProjectID || attempt.Environment != in.EnvironmentID || attempt.Service != in.Service || attempt.Plan != in.PlanID {
		return ErrConflict
	}
	if err := lockCapacityCloud(ctx, tx, in.OrgID, in.CloudID); err != nil {
		return err
	}
	if _, err := clusterWorkloadScope(ctx, tx, in); err != nil {
		return err
	}
	if err := GuardWorkloadWrite(attempt.Context(ctx), tx); err != nil {
		return err
	}
	h, err := clusterWorkloadHold(ctx, tx, in)
	if err != nil {
		return err
	}
	if h.Attempt != nil && *h.Attempt != attempt {
		previous := *h.Attempt
		previous.Generation = attempt.Generation
		if previous != attempt || attempt.Generation <= h.Attempt.Generation {
			return ErrConflict
		}
		h.BindingID, h.Receipt = "", nil
	}
	h.Attempt = &attempt
	return saveClusterWorkloadHold(ctx, tx, h)
}

func clusterWorkloadHold(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput) (*ClusterWorkloadHold, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT body FROM loomplane_cluster_workload_holds WHERE org_id=$1 AND id=$2 AND NOT released FOR UPDATE`, in.OrgID, in.ID).Scan(&raw)
	if err != nil {
		return nil, storeError(err)
	}
	var h ClusterWorkloadHold
	if json.Unmarshal(raw, &h) != nil || !reflect.DeepEqual(in, h.Input) {
		return nil, ErrConflict
	}
	return &h, nil
}
func saveClusterWorkloadHold(ctx context.Context, tx driver.Tx, h *ClusterWorkloadHold) error {
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_cluster_workload_holds SET body=$3 WHERE org_id=$1 AND id=$2 AND NOT released`, h.Input.OrgID, h.Input.ID, raw)
	return err
}

func (s *PgStore) BindClusterWorkloadReceipt(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, bindingID string) error {
	if err := lockCapacityCloud(ctx, tx, in.OrgID, in.CloudID); err != nil {
		return err
	}
	if _, err := clusterWorkloadScope(ctx, tx, in); err != nil {
		return err
	}
	h, err := clusterWorkloadHold(ctx, tx, in)
	if err != nil {
		return err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT c.body FROM loomplane_bindings b JOIN cp_instances i ON i.tenant_id=b.org_id AND i.id=b.external_id JOIN loomplane_workload_controllers c ON c.org_id=b.org_id AND c.connection_id=b.connection_id AND c.instance_id=i.id AND c.runtime_ref=i.labels->>'`+owned.LabelRuntime+`' WHERE b.org_id=$1 AND b.id=$2 AND b.connection_id=$3 FOR SHARE OF b,i,c`, in.OrgID, bindingID, in.ConnectionID).Scan(&raw)
	if err != nil {
		return storeError(err)
	}
	var r kubernetes.ControllerReceipt
	if json.Unmarshal(raw, &r) != nil || !r.Valid() || r.ReleaseID == "" || (in.ReleaseID != "" && r.ReleaseID != in.ReleaseID) || (in.RuntimeRevision != "" && r.RuntimeRevision != in.RuntimeRevision) || (in.RuntimeRef != "" && r.RuntimeRef != in.RuntimeRef) || r.PlanID != in.PlanID || r.ServiceID != in.Service || r.ProjectID != in.ProjectID || r.EnvironmentID != in.EnvironmentID {
		return ErrConflict
	}
	if !workloadRuntimeScoped(in, r.RuntimeRef) {
		return ErrConflict
	}
	if h.Attempt == nil || !workloadRevisionMatches(r.RuntimeRevision, h.Attempt.Deployment, in.ActionID) {
		return ErrConflict
	}
	if h.Receipt != nil && (*h.Receipt != r || h.BindingID != bindingID) {
		return ErrConflict
	}
	h.BindingID, h.Receipt = bindingID, &r
	valid, err := clusterWorkloadReceiptCurrent(ctx, tx, *h)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return saveClusterWorkloadHold(ctx, tx, h)
}

func clusterWorkloadReceiptCurrent(ctx context.Context, tx driver.Tx, h ClusterWorkloadHold) (bool, error) {
	if h.Attempt == nil || h.Receipt == nil || h.BindingID == "" || h.Receipt.ReleaseID == "" {
		return false, nil
	}
	a, r, in := h.Attempt, h.Receipt, h.Input
	raw, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT true FROM loomplane_bindings b
 JOIN cp_instances i ON i.tenant_id=b.org_id AND i.id=b.external_id`+routeRuntimeJoin+`
 JOIN loomplane_workload_controllers c ON c.org_id=b.org_id AND c.connection_id=b.connection_id AND c.instance_id=b.external_id
 JOIN loomplane_workload_attempts w ON w.org_id=b.org_id AND w.environment_id=b.environment_id AND w.service=b.name
 JOIN loomplane_cloud_connections k ON k.org_id=b.org_id AND k.id=b.connection_id
 JOIN loomplane_runtime_attachments a ON a.org_id=k.org_id AND a.runtime_connection_id=k.id
 WHERE b.org_id=$1 AND b.id=$2 AND b.project_id=$3 AND b.environment_id=$4 AND b.name=$5 AND b.plan_id=$6
 AND b.subject='service' AND b.provider='kubernetes' AND b.engine='ctrlplane' AND b.drift='' AND b.revision=$7
 AND i.provider_name='kubernetes.'||b.connection_id AND coalesce(i.kind,'') IN ('','deployment') AND i.state='running'
 AND jsonb_array_length(i.services)=1 AND i.services->0->>'name'=c.body->>'container' AND i.services->0->>'image'=$7
 AND i.labels->>'`+owned.LabelOrg+`'=$1 AND i.labels->>'`+owned.LabelProject+`'=$3 AND i.labels->>'`+owned.LabelEnvironment+`'=$4
 AND i.labels->>'`+owned.LabelService+`'=$5 AND i.labels->>'`+owned.LabelPlan+`'=$6 AND i.labels->>'`+owned.LabelConnection+`'=b.connection_id
 AND i.labels->>'`+owned.LabelRuntime+`'=$8 AND coalesce(`+routeRuntimeRevision+`,'')=c.body->>'runtime_revision'
 AND c.runtime_ref=$8 AND c.body=$9::jsonb AND k.status='ready' AND k.cloud='kubernetes'
 AND k.details->>'cluster_ca'=$10 AND k.details->>'namespace_uid'=$11
 AND k.details->>'namespace'=c.body->>'namespace' AND a.body->>'namespace_uid'=$11 AND a.body->>'trust_fingerprint'=$10
 AND a.project_id=b.project_id AND a.cloud_id=$17
 AND a.id=$12 AND a.cluster_id=$13 AND a.deleted_at IS NULL AND a.body->>'trust_revision'=$14
 AND (NOT EXISTS(SELECT 1 FROM loomplane_plans p WHERE p.org_id=b.org_id AND p.id=b.plan_id AND p.required_runtime_protocol<>'') OR EXISTS(SELECT 1 FROM loomplane_managed_workload_authority ma WHERE ma.org_id=b.org_id AND ma.release_id=c.body->>'release_id' AND ma.epoch=w.generation AND ma.revoked_at IS NULL))
 AND w.project_id=$3 AND w.plan_id=$6 AND w.deployment_id=$15 AND w.generation=$16 AND w.effects_started AND NOT w.legacy_unfenced FOR SHARE OF b,c,w,k,a,i`, in.OrgID, h.BindingID, in.ProjectID, in.EnvironmentID, in.Service, in.PlanID, r.Image, r.RuntimeRef, raw, r.ClusterCA, r.NamespaceUID, in.AttachmentID, in.ResourceID, strconv.FormatInt(in.TrustRevision, 10), a.Deployment, a.Generation, in.CloudID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return valid, err
}

// ClusterWorkloadUsage recomputes coverage under the admission transaction. It
// never persists released credit, so missing evidence restores the whole hold.
func (s *PgStore) ClusterWorkloadUsage(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput) (used, supply ClusterWorkloadVector, err error) {
	if checkErr := lockCapacityCloud(ctx, tx, in.OrgID, in.CloudID); checkErr != nil {
		return ClusterWorkloadVector{}, ClusterWorkloadVector{}, checkErr
	}
	snapshot, err := clusterWorkloadScope(ctx, tx, in)
	if err != nil {
		return ClusterWorkloadVector{}, ClusterWorkloadVector{}, err
	}
	holds, err := clusterWorkloadHolds(ctx, tx, in.OrgID, in.ResourceID)
	if err != nil {
		return ClusterWorkloadVector{}, ClusterWorkloadVector{}, err
	}
	return clusterWorkloadAdmission(ctx, tx, snapshot, holds)
}

// ReleaseUnstartedClusterWorkload requires the cancellation transaction to fence
// future effects through guard. Missing attempt evidence never releases a hold.
// Effects-started releases need controller revocation and cleanup proof, which
// this contract deliberately cannot bypass through a caller-supplied guard.
func (s *PgStore) ReleaseUnstartedClusterWorkload(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput, guard ClusterWorkloadGuard) error {
	if tx == nil || guard == nil {
		return ErrInvalid
	}
	if err := lockCapacityCloud(ctx, tx, in.OrgID, in.CloudID); err != nil {
		return err
	}
	var resourceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, in.OrgID, in.CloudID, in.ResourceID).Scan(&resourceID); err != nil {
		return storeError(err)
	}
	h, err := clusterWorkloadHold(ctx, tx, in)
	if err != nil {
		return err
	}
	if h.Attempt == nil || h.Receipt != nil {
		return ErrConflict
	}
	if checkErr := guard(ctx, tx, in); checkErr != nil {
		return checkErr
	}
	a := h.Attempt
	var settled bool
	err = tx.QueryRow(ctx, `SELECT settled FROM loomplane_workload_attempts WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4 AND plan_id=$5 AND deployment_id=$6 AND generation=$7 AND NOT active AND settled AND NOT effects_started AND NOT legacy_unfenced FOR UPDATE`, a.Org, a.Project, a.Environment, a.Service, a.Plan, a.Deployment, a.Generation).Scan(&settled)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_cluster_workload_holds SET released=true WHERE org_id=$1 AND id=$2`, in.OrgID, in.ID)
	return err
}

func workloadRevisionMatches(revision, deployment, action string) bool {
	base := deployment + "/" + action
	return deployment != "" && action != "" && (revision == base || strings.HasPrefix(revision, base+"/") && len(revision) > len(base)+1)
}

func workloadRuntimeScoped(in ClusterWorkloadInput, ref string) bool {
	prefix := in.OrgID + "/" + in.EnvironmentID + "/" + in.Service + "/runtime_"
	return strings.HasPrefix(ref, prefix) && len(ref) > len(prefix) && !strings.Contains(strings.TrimPrefix(ref, prefix), "/")
}
