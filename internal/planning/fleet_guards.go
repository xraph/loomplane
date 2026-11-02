package planning

import (
	"context"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/projects"
)

// requireUnmanagedEnvironmentID reloads ownership, including for shared Cloud
// changes whose participant list may have been read before ownership changed.
func (s *Service) requireUnmanagedEnvironmentID(ctx context.Context, org, project, id string) error {
	if reader, ok := s.projects.(interface {
		GetEnvironment(context.Context, string, string, string) (*projects.Environment, error)
	}); ok {
		env, err := reader.GetEnvironment(ctx, org, project, id)
		if err != nil {
			return err
		}
		if env.InstanceID != "" {
			return projects.ErrFleetManaged
		}
		return nil
	}
	environments, err := s.projects.ListEnvironments(ctx, org, project)
	if err != nil {
		return err
	}
	for _, env := range environments {
		if env.ID == id {
			if env.InstanceID != "" {
				return projects.ErrFleetManaged
			}
			return nil
		}
	}
	return projects.ErrEnvironmentNotFound
}

func (s *Service) requireUnmanagedSnapshot(ctx context.Context, org, project string, snapshot *cloudprofiles.Snapshot) error {
	if snapshot == nil {
		return ErrInvalid
	}
	for _, env := range snapshot.Environments {
		if err := s.requireUnmanagedEnvironmentID(ctx, org, cloudprofiles.ParticipantProject(env.ProjectID, project), env.EnvironmentID); err != nil {
			return err
		}
	}
	return nil
}
