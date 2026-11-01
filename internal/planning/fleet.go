package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

var ErrInstanceRuntimeUnavailable = errors.New("instance runtime isolation is not qualified")

// InstanceVerifier rechecks saved authority and returns a freshly rendered snapshot.
type InstanceVerifier interface {
	VerifyInstanceSnapshot(context.Context, fleets.OperationAuthority, fleets.InstanceSnapshot) (*fleets.InstanceSnapshot, error)
}

// InstancePlanInput is an internal contract, never an HTTP request body.
type InstancePlanInput struct {
	Authority fleets.OperationAuthority `json:"authority"`
	Snapshot  fleets.InstanceSnapshot   `json:"snapshot"`
}

// CreateInstancePlan enters the normal planner with an authorized artifact release.
func (s *Service) CreateInstancePlan(ctx context.Context, p security.Principal, in InstancePlanInput) (*Plan, error) {
	if s.instances == nil || !p.IsFleet() || p.ReadOnly {
		return nil, security.ErrForbidden
	}
	caller, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	approved, err := json.Marshal(in.Authority.Principal)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(caller, approved) {
		return nil, security.ErrForbidden
	}
	snapshot, err := s.instances.VerifyInstanceSnapshot(ctx, in.Authority, in.Snapshot)
	if err != nil {
		return nil, err
	}
	envs, err := s.projects.ListEnvironments(ctx, p.OrgID, in.Authority.Scope.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		if env.ID != snapshot.EnvironmentID {
			continue
		}
		if env.InstanceID != snapshot.InstanceID {
			return nil, ErrInvalid
		}
		in.Snapshot = *snapshot
		return s.start(ctx, p, &Plan{Kind: KindInstance, OrgID: p.OrgID, ProjectID: in.Authority.Scope.ProjectID, EnvironmentID: env.ID, Environment: env.Name, ManagedInstanceID: env.InstanceID, FleetInput: &in})
	}
	return nil, ErrNotFound
}
func (s *Service) instancePlanInputs(ctx context.Context, plan *Plan) (*planInputs, error) {
	in := plan.FleetInput
	if s.instances == nil || in == nil || plan.Kind != KindInstance || plan.Catalog || len(plan.Sources) > 0 || plan.ManifestID != "" || plan.OrgID != in.Authority.Scope.OrgID || plan.ProjectID != in.Authority.Scope.ProjectID || plan.EnvironmentID != in.Snapshot.EnvironmentID || plan.ManagedInstanceID != in.Snapshot.InstanceID {
		return nil, ErrInvalid
	}
	snapshot, err := s.instances.VerifyInstanceSnapshot(ctx, in.Authority, in.Snapshot)
	if err != nil {
		return nil, err
	}
	artifact, err := ArtifactInput(ArtifactSnapshot{ReleaseID: snapshot.ReleaseID, Digest: snapshot.ReleaseDigest, Template: snapshot.Template})
	if err != nil {
		return nil, err
	}
	return &planInputs{graph: artifact.Graph, held: map[string]bool{}, excluded: map[string]bool{}, revision: &manifests.Revision{OrgID: plan.OrgID, ProjectID: plan.ProjectID, Manifest: &snapshot.Manifest, Digest: snapshot.Digest}}, nil
}

// InstanceRuntimeQualifier verifies namespace and resource receipts before a
// plan can reserve capacity or propose executable work.
type InstanceRuntimeQualifier interface {
	QualifyInstancePlan(context.Context, *Plan) error
}

func (s *Service) qualifyInstanceRuntime(ctx context.Context, plan *Plan) error {
	if plan.Kind != KindInstance {
		return nil
	}
	if s.instanceRuntime == nil {
		return ErrInstanceRuntimeUnavailable
	}
	return s.instanceRuntime.QualifyInstancePlan(ctx, plan)
}

func instancePlanningPermanent(err error) bool {
	return errors.Is(err, fleets.ErrAuthorityRevoked) || errors.Is(err, projects.ErrFleetManaged)
}
