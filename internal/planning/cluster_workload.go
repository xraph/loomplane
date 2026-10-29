package planning

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/xraph/grove/driver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/security"
)

// ClusterWorkloadDemand is part of the immutable approved action. It grants no
// runtime writes until the managed execution fence is qualified.
type ClusterWorkloadDemand struct {
	SchemaVersion        int                                  `json:"schema_version"`
	Mode                 string                               `json:"mode"`
	ConnectionGeneration int64                                `json:"connection_generation"`
	Placement            CloudPlacement                       `json:"placement"`
	Input                cloudinfra.ClusterWorkloadInput      `json:"input"`
	Profile              *cloudinfra.ClusterSchedulingProfile `json:"profile,omitempty"`
}

type clusterWorkloadAuthority interface {
	CheckClusterWorkloadCapacity(context.Context) error
	CurrentClusterScalingPolicy(context.Context, string, string, string, string) (*cloudinfra.ClusterScalingPolicy, error)
}

func managedClusterAction(a Action) bool {
	return a.Service != nil && a.Provider == "kubernetes" && a.Service.TargetSettings["managed_attachment_id"] != "" && a.Op != Keep && a.Op != Delete
}
func workloadBlock(reason string) error {
	return &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: reason}}
}

// PrepareClusterWorkloadDemands pins server-derived requests and observation
// identity before approval. Historical plans without this field stay immutable.
func (s *Service) PrepareClusterWorkloadDemands(ctx context.Context, plan *Plan, result *Result) error {
	st, ok := s.store.(*PgStore)
	if !ok {
		return nil
	}
	for i := range result.Actions {
		a := &result.Actions[i]
		if !managedClusterAction(*a) {
			continue
		}
		cfg := a.Service
		if cfg.CloudPlacement == nil || cfg.CloudPlacement.TargetID == "" {
			return workloadBlock("managed_target_required")
		}
		authority, ok := s.capacity.(clusterWorkloadAuthority)
		if !ok {
			return ErrUnavailable
		}
		if err := authority.CheckClusterWorkloadCapacity(ctx); err != nil {
			return err
		}
		mode := cfg.CapacityMode
		if mode == "" {
			mode = "guaranteed"
		}
		if mode != "guaranteed" && mode != "best_effort" {
			return ErrInvalid
		}
		id, err := typeid.Generate("workload")
		if err != nil {
			return err
		}
		pin := *cfg.CloudPlacement
		in := cloudinfra.ClusterWorkloadInput{ID: id.String(), OrgID: plan.OrgID, CloudID: pin.CloudID, TargetID: pin.TargetID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, PlanID: plan.ID, ActionID: a.ID, Service: a.Name}
		in, snapshot, err := cloudinfra.NewPgStore(st.db).ClusterWorkloadReview(ctx, in)
		if err != nil {
			return err
		}
		d := &ClusterWorkloadDemand{SchemaVersion: 1, Mode: mode, Placement: pin, Input: in}
		if checkErr := st.db.QueryRow(ctx, `SELECT generation FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND status='ready'`, plan.OrgID, in.ConnectionID).Scan(&d.ConnectionGeneration); checkErr != nil {
			return checkErr
		}
		if mode == "guaranteed" {
			if cfg.GPU != nil || cfg.Size != nil && (cfg.Size.GPU != nil || cfg.Size.Shm > 0) || cfg.Catalog != nil || cfg.Component != nil {
				return workloadBlock("workload_constraints_unsupported")
			}
			d.Input.Maximum, err = managedWorkloadMaximum(cfg)
			if err != nil {
				return err
			}
			architecture := cfg.TargetSettings["architecture"]
			if architecture == "" {
				return workloadBlock("native_template_unqualified")
			}
			for _, node := range snapshot.Nodes {
				if !node.WorkloadCompatible(architecture) {
					return workloadBlock("workload_constraints_unsupported")
				}
			}
			resource, readErr := cloudinfra.NewPgStore(st.db).GetManagedResource(ctx, plan.OrgID, in.ResourceID)
			if readErr != nil {
				return readErr
			}
			var selected *cloudinfra.ClusterSchedulingProfile
			var native *cloudinfra.ClusterScalingPolicy
			for i := range snapshot.SchedulingProfiles {
				candidate := &snapshot.SchedulingProfiles[i]
				if policy, exists := resource.ScalingPolicies[candidate.PoolID]; exists && policy.Mode == "native" && !policy.Revoked {
					current, e := authority.CurrentClusterScalingPolicy(ctx, plan.OrgID, pin.CloudID, in.ResourceID, candidate.PoolID)
					if e != nil {
						return e
					}
					if native != nil {
						return workloadBlock("native_template_unqualified")
					}
					native = current
					selected = candidate
				}
			}
			if selected == nil {
				if len(snapshot.SchedulingProfiles) != 1 {
					return workloadBlock("native_template_unqualified")
				}
				selected = &snapshot.SchedulingProfiles[0]
			}
			cpu, memory := OwnedWorkloadResources(cfg)
			if selected.Architecture != architecture || int64(cpu) > selected.Available.CPUMillis || int64(memory)*(1<<20) > selected.Available.MemoryBytes || selected.Available.Pods < 1 {
				return workloadBlock("native_template_unqualified")
			}
			d.Profile = selected
			if native != nil {
				d.Input.Native = &cloudinfra.ClusterNativeWorkload{PoolID: selected.PoolID, PolicyID: native.ID, PolicyRevision: native.Revision, ProfileDigest: selected.Digest, MaximumNodes: native.MaxNodes}
			}
		}
		digest, err := Digest(d)
		if err != nil {
			return err
		}
		d.Input.ReviewDigest = digest
		cfg.ClusterDemand = d
		result.RequiredRuntimeProtocol = orchestration.ManagedWorkloadProtocol
		a.ConnectionID = in.ConnectionID
	}
	return nil
}

func (s *Service) resolveClusterWorkload(ctx context.Context, st *PgStore, plan *Plan, a Action) (err error) {
	d := a.Service.ClusterDemand
	authority, ok := s.capacity.(clusterWorkloadAuthority)
	if !ok {
		return ErrUnavailable
	}
	if checkErr := authority.CheckClusterWorkloadCapacity(ctx); checkErr != nil {
		return checkErr
	}
	var raw, actor []byte
	err = st.db.QueryRow(ctx, `SELECT i.body,i.actor FROM loomplane_cluster_workload_intents i JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id AND p.revision=i.plan_revision WHERE i.org_id=$1 AND i.plan_id=$2 AND i.service=$3 AND p.status='approved'`, plan.OrgID, plan.ID, a.Name).Scan(&raw, &actor)
	if err != nil {
		return err
	}
	var saved ClusterWorkloadDemand
	var principal security.Principal
	if checkErr := json.Unmarshal(raw, &saved); checkErr != nil {
		return checkErr
	}
	if checkErr := json.Unmarshal(actor, &principal); checkErr != nil {
		return checkErr
	}
	if !reflect.DeepEqual(saved, *d) || principal.OrgID != plan.OrgID || principal.UserID == "" || principal.ReadOnly || s.authz == nil {
		return ErrConflict
	}
	if principal.KeyID != "" {
		if s.capacityPrincipal == nil {
			return ErrUnavailable
		}
		if checkErr := s.capacityPrincipal.ValidatePrincipal(ctx, principal); checkErr != nil {
			return checkErr
		}
	}
	if checkErr := s.authz.Require(ctx, principal, security.DeploymentApprove, plan.ProjectID); checkErr != nil {
		return checkErr
	}
	if n := d.Input.Native; n != nil {
		p, e := authority.CurrentClusterScalingPolicy(ctx, plan.OrgID, d.Input.CloudID, d.Input.ResourceID, n.PoolID)
		if e != nil {
			return e
		}
		if p.ID != n.PolicyID || p.Revision != n.PolicyRevision || p.MaxNodes != n.MaximumNodes {
			return ErrConflict
		}
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, plan.OrgID); checkErr != nil {
		return checkErr
	}
	guard := func(ctx context.Context, tx driver.Tx, in cloudinfra.ClusterWorkloadInput) error {
		var current bool
		if e := tx.QueryRow(ctx, `SELECT p.status='approved' AND p.revision=$3 AND i.body=$5::jsonb AND loomplane_cluster_workload_scope_current(p,i.body) AND NOT EXISTS(SELECT 1 FROM loomplane_deployments x WHERE x.org_id=p.org_id AND x.plan_id=p.id AND x.status='cancelled') FROM loomplane_plans p JOIN loomplane_cluster_workload_intents i ON i.org_id=p.org_id AND i.plan_id=p.id WHERE p.org_id=$1 AND p.id=$2 AND i.service=$4 FOR UPDATE OF p,i`, plan.OrgID, plan.ID, plan.Revision, a.Name, raw).Scan(&current); e != nil {
			return e
		}
		if !current || !reflect.DeepEqual(in, d.Input) {
			return ErrConflict
		}
		check := *d
		check.Input.ReviewDigest = ""
		hash, e := Digest(check)
		if e != nil || hash != d.Input.ReviewDigest {
			return ErrConflict
		}
		if a.Service.CloudPlacement == nil || !reflect.DeepEqual(*a.Service.CloudPlacement, d.Placement) || a.ConnectionID != in.ConnectionID || a.Service.TargetSettings["managed_attachment_id"] != in.AttachmentID || a.Service.TargetSettings["managed_resource_id"] != in.ResourceID {
			return ErrConflict
		}
		mode := a.Service.CapacityMode
		if mode == "" {
			mode = "guaranteed"
		}
		if d.Mode != mode {
			return ErrConflict
		}
		if d.Mode == "best_effort" {
			if d.Input.Native != nil || d.Profile != nil || d.Input.Maximum != (cloudinfra.ClusterWorkloadVector{}) {
				return ErrConflict
			}
			return nil
		}
		if a.Service.GPU != nil || a.Service.Size != nil && (a.Service.Size.GPU != nil || a.Service.Size.Shm > 0) || a.Service.Catalog != nil || a.Service.Component != nil {
			return workloadBlock("workload_constraints_unsupported")
		}
		maximum, e := managedWorkloadMaximum(a.Service)
		if e != nil {
			return e
		}
		if maximum != in.Maximum || d.SchemaVersion != 1 || d.Mode != "guaranteed" || d.Profile == nil || in.Native != nil && d.Profile.Digest != in.Native.ProfileDigest {
			return ErrConflict
		}
		return cloudinfra.NewPgStore(st.db).ValidateClusterWorkloadProfile(ctx, tx, in, *d.Profile)
	}
	switch d.Mode {
	case "guaranteed":
		if _, err = cloudinfra.NewPgStore(st.db).ReserveClusterWorkload(ctx, tx, d.Input, guard); err != nil {
			return err
		}
	case "best_effort":
		if checkErr := guard(ctx, tx, d.Input); checkErr != nil {
			return checkErr
		}
	default:
		return ErrInvalid
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return checkErr
	}
	// Managed runtime issuance must be qualified before this reservation can execute.
	return workloadBlock("managed_workload_execution_unqualified")
}
