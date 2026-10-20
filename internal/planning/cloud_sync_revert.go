package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// PreviewCloudRevert reviews compensating placement from the actual partial
// serving state. It refuses changes whose recorded after-values no longer match.
func (s *Service) PreviewCloudRevert(ctx context.Context, p security.Principal, project, id string) (*cloudprofiles.Preview, error) {
	if err := s.authorizeCloudSync(ctx, p, project); err != nil {
		return nil, err
	}
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	sync, snapshot, err := st.GetSync(ctx, p.OrgID, project, id)
	if err != nil {
		return nil, err
	}
	if sync.Status == cloudprofiles.SyncRunning || sync.Status == cloudprofiles.SyncPending || sync.Status == cloudprofiles.SyncReverted || sync.Status == cloudprofiles.SyncReverting {
		return nil, cloudprofiles.ErrBusy
	}
	for _, env := range snapshot.Environments {
		checked, ok := s.store.(cloudCompensationStore)
		if !ok {
			return nil, errors.New("the Cloud compensation comparisons are unavailable")
		}
		if compensationErr := checked.CheckCloudCompensation(ctx, p.OrgID, id, env.Bindings); compensationErr != nil {
			return nil, compensationErr
		}
	}
	previous, _, err := st.GetPreview(ctx, p.OrgID, project, sync.PreviewID)
	if err != nil {
		return nil, err
	}
	var in cloudprofiles.ChangeInput
	if sync.TargetID != "" {
		current, e := st.GetTarget(ctx, p.OrgID, project, sync.CloudID, sync.TargetID)
		if e != nil {
			return nil, e
		}
		if sync.After == nil || current.Provider != sync.After.Provider || current.ConnectionID != sync.After.ConnectionID || current.Region != sync.After.Region || current.Revision != sync.After.Revision {
			return nil, cloudprofiles.ErrConflict
		}
		in = cloudprofiles.ChangeInput{CloudID: sync.CloudID, TargetID: sync.TargetID, Revision: current.Revision, Provider: sync.Before.Provider, ConnectionID: sync.Before.ConnectionID, Region: sync.Before.Region}
	} else {
		env := previous.Input.EnvironmentID
		generation, e := st.AssignmentGeneration(ctx, p.OrgID, project, env)
		if e != nil {
			return nil, e
		}
		if generation != previous.Input.Revision+1 {
			return nil, cloudprofiles.ErrConflict
		}
		in.EnvironmentID = env
		in.Revision = generation
		if previous.BeforeAssignment != nil {
			in.CloudID = previous.BeforeAssignment.CloudID
		}
	}
	return s.previewCloudChange(ctx, p, project, in, id)
}

// ApplyCloudRevert uses the same revision-bound acceptance contract as a switch.
func (s *Service) ApplyCloudRevert(ctx context.Context, p security.Principal, project, previewID, revision, key string) (*cloudprofiles.Sync, error) {
	st, err := s.syncStore()
	if err != nil {
		return nil, err
	}
	preview, _, err := st.GetPreview(ctx, p.OrgID, project, previewID)
	if err != nil {
		return nil, err
	}
	if preview.Reverts == "" {
		return nil, cloudprofiles.ErrInvalid
	}
	return s.ApplyCloudChange(ctx, p, project, previewID, revision, key)
}

type cloudCompensationStore interface {
	CheckCloudCompensation(context.Context, string, string, json.RawMessage) error
}
