package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/clouds/gcp"
)

func pendingFailedCreate(identity *PendingCloudIdentity) bool {
	if identity == nil {
		return false
	}
	var state cloudSQLState
	return decodeDatabaseCheckpoint(identity.Proof, &state) == nil && state.FailedCreate != nil
}

func (s *cloudSQLState) resolveFailedCreate(ctx context.Context, client *gcp.Session, version string) (*PendingCloudIdentity, bool, error) {
	request := gcp.SQLCreateRequest{Instance: s.Scope.Instance, Region: s.Scope.Region, PhysicalID: s.ResourceID, Intent: s.Intent, DatabaseVersion: version, Caller: s.Caller}
	operation := s.Operation
	if s.FailedCreate != nil {
		request, operation = s.FailedCreate.SQLCreateRequest, s.FailedCreate.Operation
	} else if s.RequestedAt != nil {
		request.RequestedAt = *s.RequestedAt
	}
	proof, err := client.InspectSQLFailedCreate(ctx, request, operation, s.pscCheck)
	if err != nil {
		return nil, true, err
	}
	if proof == nil {
		if s.FailedCreate != nil {
			return nil, true, errors.New("captured failed create is no longer terminal")
		}
		return nil, false, nil
	}
	if kept := s.FailedCreate; kept != nil && (proof.Operation != kept.Operation || !proof.InsertedAt.Equal(kept.InsertedAt) || !proof.StartedAt.Equal(kept.StartedAt) || !proof.EndedAt.Equal(kept.EndedAt)) {
		return nil, true, errors.New("captured failed create operation changed")
	}
	if s.PSC != nil && (s.PSC.Provision != (gcp.SQLPSCProvisionState{}) || s.PSC.Endpoint != (gcp.SQLPSCProvisionedEndpoint{})) {
		return nil, true, errCloudSQLPSCOwnership
	}
	s.FailedCreate, s.ResourceID = proof, proof.PhysicalID
	s.Operation, s.Uncertain, s.RequestedAt, s.Caller = "", false, nil, ""
	if saveErr := s.save(); saveErr != nil {
		return nil, true, saveErr
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, true, err
	}
	owned, err := s.ownership(nil)
	if err != nil {
		return nil, true, err
	}
	identity := &PendingCloudIdentity{PhysicalID: s.ResourceID, Proof: raw}
	if !proof.Absent {
		identity.ExternalID = s.Scope.Project + ":" + s.Scope.Region + ":" + s.Scope.Instance
	}
	if owned != "" {
		if decodeErr := json.Unmarshal([]byte(owned), &identity.OwnedResources); decodeErr != nil {
			return nil, true, decodeErr
		}
	}
	return identity, true, nil
}
