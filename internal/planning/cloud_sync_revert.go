package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/xraph/loomplane/internal/cloudinfra"
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
	if authErr := s.authorizeStoredCloudSync(ctx, p, sync, snapshot); authErr != nil {
		return nil, authErr
	}
	if sync.Status == cloudprofiles.SyncRunning || sync.Status == cloudprofiles.SyncPending || sync.Status == cloudprofiles.SyncReverted || sync.Status == cloudprofiles.SyncReverting {
		return nil, cloudprofiles.ErrBusy
	}
	for _, env := range snapshot.Environments {
		checked, ok := s.store.(cloudCompensationStore)
		if !ok {
			return nil, errors.New("the Cloud compensation comparisons are unavailable")
		}
		if compensationErr := checked.CheckCloudCompensation(ctx, p.OrgID, id, env.EnvironmentID, env.Bindings); compensationErr != nil {
			return nil, compensationErr
		}
	}
	previous, _, err := st.GetPreview(ctx, p.OrgID, project, sync.PreviewID)
	if err != nil {
		return nil, err
	}
	var in cloudprofiles.ChangeInput
	switch {
	case sync.Default:
		// Compensate only while the Cloud still holds exactly the default this sync set.
		current, e := st.GetProfile(ctx, p.OrgID, "", sync.CloudID)
		if e != nil {
			return nil, e
		}
		if !sameDefault(current.DefaultPlacement, sync.AfterDefault) {
			return nil, cloudprofiles.ErrConflict
		}
		in = cloudprofiles.ChangeInput{CloudID: sync.CloudID, Default: true, Revision: current.Revision}
		if before := sync.BeforeDefault; before != nil {
			in.Provider, in.ConnectionID, in.Region = before.Provider, before.ConnectionID, before.Region
		}
	case sync.TargetID != "":
		current, e := st.GetTarget(ctx, p.OrgID, "", sync.CloudID, sync.TargetID)
		if e != nil {
			return nil, e
		}
		if sync.After == nil || current.Provider != sync.After.Provider || current.ConnectionID != sync.After.ConnectionID || current.Region != sync.After.Region || current.Runtime != sync.After.Runtime || current.PoolID != sync.After.PoolID || current.Revision != sync.After.Revision {
			return nil, cloudprofiles.ErrConflict
		}
		in = cloudprofiles.ChangeInput{CloudID: sync.CloudID, TargetID: sync.TargetID, Revision: current.Revision, Provider: sync.Before.Provider, ConnectionID: sync.Before.ConnectionID, Region: sync.Before.Region, Runtime: sync.Before.Runtime, PoolID: sync.Before.PoolID}
	default:
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
	if err := s.authorizeCloudSync(ctx, p, project); err != nil {
		return nil, err
	}
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
	CheckCloudCompensation(context.Context, string, string, string, json.RawMessage) error
}

// CloudCompensation pins the source and candidate reviewed for one service.
// Retained restores the saved instance. Otherwise the source must be recreated.
type CloudCompensation struct {
	PlanID            string             `json:"plan_id"`
	Routes            []cloudinfra.Route `json:"routes,omitempty"`
	ActionID          string             `json:"action_id"`
	Source            Binding            `json:"source"`
	Candidate         Binding            `json:"candidate"`
	Retained          bool               `json:"retained"`
	ServerAttempt     bool               `json:"server_attempt"`
	OriginalCompleted bool               `json:"original_completed"`
}

func cloudCompensationBindings(current []*Binding, snapshot *cloudprofiles.Snapshot, syncID, environment string, unstarted bool) ([]*Binding, map[string]*CloudCompensation, error) {
	result := make([]*Binding, 0, len(current))
	reviewed := map[string]*CloudCompensation{}
	var saved []*Binding
	var original Plan
	for _, env := range snapshot.Environments {
		if env.EnvironmentID != environment {
			continue
		}
		var err error
		saved, err = decodeCloudBindings(env.Bindings)
		if err != nil {
			return nil, nil, err
		}
		if decodeErr := json.Unmarshal(env.Plan, &original); decodeErr != nil {
			return nil, nil, decodeErr
		}
	}
	for _, current := range current {
		planned := *current
		result = append(result, &planned)
		id := actionID(current.Subject, current.Name)
		var action *Action
		for i := range original.Actions {
			if original.Actions[i].ID == id {
				action = &original.Actions[i]
			}
		}
		if action == nil || action.Op == Keep {
			continue
		}
		var source *Binding
		for _, old := range saved {
			if old.ID == current.ID {
				source = old
			}
		}
		if source == nil {
			return nil, nil, cloudprofiles.ErrConflict
		}
		if unstarted {
			if !sameCompensationResource(*current, *source) || current.PlanID != source.PlanID {
				return nil, nil, cloudprofiles.ErrConflict
			}
			continue
		}
		if action.Provider != "server" && sameCompensationResource(*current, *source) {
			continue
		}
		c := &CloudCompensation{ServerAttempt: action.Provider == "server", PlanID: migrationPlanID(syncID, environment), ActionID: id, Routes: action.Routes, Source: *source, Candidate: *current}
		c.Source.Replaced, c.Candidate.Replaced = nil, nil
		for _, retained := range current.Replaced {
			if retained.ExternalID != source.ExternalID || retained.ConnectionID != source.ConnectionID {
				continue
			}
			if !sameCompensationResource(retained, *source) {
				return nil, nil, fmt.Errorf("%w: saved source identity changed", cloudprofiles.ErrConflict)
			}
			c.Retained = true
		}
		if sameCompensationResource(*current, *source) && action.Op == Replace {
			c.Retained = true
		}
		if source.Provider != "server" || !c.ServerAttempt {
			c.Retained = false
		}
		if c.Retained {
			planned = *source
		} else {
			planned.Config = source.Config
			planned.CommitSHA = source.CommitSHA
			var saved ServiceConfig
			if err := json.Unmarshal(source.Config, &saved); err != nil {
				return nil, nil, err
			}
			if saved.CloudPlacement != nil {
				saved.CloudPlacement.Allocation = ""
				raw, err := json.Marshal(saved)
				if err != nil {
					return nil, nil, err
				}
				planned.Config = raw
			}
		}
		reviewed[id] = c
	}
	return result, reviewed, nil
}

func sameCompensationResource(a, b Binding) bool {
	return a.ID == b.ID && a.OrgID == b.OrgID && a.ProjectID == b.ProjectID && a.EnvironmentID == b.EnvironmentID && a.Subject == b.Subject && a.Name == b.Name && a.Provider == b.Provider && a.Region == b.Region && a.Engine == b.Engine && a.URL == b.URL && a.ConnectionID == b.ConnectionID && a.ExternalID == b.ExternalID && a.PhysicalID == b.PhysicalID && a.Revision == b.Revision && a.CommitSHA == b.CommitSHA && reflect.DeepEqual(a.Config, b.Config) && a.RetainsOwnership(&b) && b.RetainsOwnership(&a)
}

// cloudCompensationDescription exposes reviewed modes without saved input refs.
func cloudCompensationDescription(actions []Action) (mode, message string) {
	var retained, recreated bool
	for _, a := range actions {
		if a.Compensation != nil {
			if a.Compensation.Retained {
				retained = true
			} else {
				recreated = true
			}
		}
	}
	switch {
	case retained && recreated:
		return "mixed", "Restore retained source instances and recreate released sources from saved inputs as new instances."
	case retained:
		return "restore_retained", "Restore the retained source instances without rebuilding."
	case recreated:
		return "recreate", "Recreate the saved source from its artifact and variables as new runtime instances."
	default:
		return "", ""
	}
}
