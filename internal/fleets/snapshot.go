package fleets

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/pkg/fleet"
)

// PinnedSecrets resolves versions only within a verified destination scope.
type PinnedSecrets interface {
	ResolvePinnedSecret(context.Context, environments.PinnedSecretScope, fleet.SecretRef) ([]byte, error)
}

// VerifyInstanceSnapshot rebuilds the plan input from authoritative records.
// No field from a serialized snapshot is trusted merely because it was saved.
func (s *Service) VerifyInstanceSnapshot(ctx context.Context, authority OperationAuthority, candidate InstanceSnapshot) (*InstanceSnapshot, error) {
	if err := s.RevalidateAuthority(ctx, authority); err != nil {
		return nil, err
	}
	instance, err := s.store.ManagedEnvironment(ctx, authority.Scope.OrgID, candidate.EnvironmentID)
	if err != nil {
		return nil, err
	}
	request := authority.Request
	if instance.FleetID != authority.Scope.FleetID || instance.ID != candidate.InstanceID || instance.ExternalRef != request.ExternalRef || instance.StageID != request.StageID || instance.DesiredReleaseID != request.ReleaseID || instance.ProfileID != request.ProfileID || instance.Status == "deleted" || instance.Status == "retired" {
		return nil, ErrConflict
	}
	releases, ok := s.store.(ReleaseStore)
	if !ok {
		return nil, ErrInvalid
	}
	release, err := releases.GetRelease(ctx, authority.Scope, request.ReleaseID)
	if err != nil {
		return nil, err
	}
	rendered, err := RenderInstance(*release, *instance, fleet.CreateInstanceInput{StageID: instance.StageID, ReleaseID: release.ID, ProfileID: instance.ProfileID, Parameters: candidate.Parameters, SecretRefs: candidate.SecretRefs})
	if err != nil {
		return nil, err
	}
	expected, err := json.Marshal(rendered)
	if err != nil {
		return nil, err
	}
	actual, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	if rendered.Digest != request.SnapshotDigest || !bytes.Equal(expected, actual) {
		return nil, ErrConflict
	}
	for service, refs := range rendered.Secrets {
		for _, ref := range refs {
			if s.secrets == nil {
				return nil, ErrInvalid
			}
			value, err := s.secrets.ResolvePinnedSecret(ctx, environments.PinnedSecretScope{OrgID: authority.Scope.OrgID, ProjectID: authority.Scope.ProjectID, EnvironmentID: instance.EnvironmentID, InstanceID: instance.ID, Service: service}, ref)
			clear(value)
			if err != nil {
				return nil, err
			}
		}
	}
	return rendered, nil
}
