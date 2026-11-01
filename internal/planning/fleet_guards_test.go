package planning

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type fleetGuardProjects struct{ Projects }

func (fleetGuardProjects) GetEnvironment(_ context.Context, org, project, id string) (*projects.Environment, error) {
	if org != "org" || project != "project" || id != "environment" {
		return nil, projects.ErrEnvironmentNotFound
	}
	return &projects.Environment{ID: id, InstanceID: "instance"}, nil
}
func TestFleetStoredCloudSyncDenied(t *testing.T) {
	s := NewService(Deps{Projects: fleetGuardProjects{}})
	snapshot := &cloudprofiles.Snapshot{Environments: []cloudprofiles.EnvironmentSnapshot{{ProjectID: "project", EnvironmentID: "environment"}}}
	err := s.authorizeStoredCloudSync(t.Context(), security.Principal{OrgID: "org"}, &cloudprofiles.Sync{OrgID: "org", ProjectID: "project"}, snapshot)
	if !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("saved Cloud sync bypassed fleet: %v", err)
	}
}
