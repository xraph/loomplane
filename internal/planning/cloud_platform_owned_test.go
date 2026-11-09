package planning

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// platformSyncStore holds one environment assigned to the platform-owned
// Loomplane Cloud and one user Cloud it could move to. Methods a preview
// doesn't reach stay nil and panic.
type platformSyncStore struct {
	cloudprofiles.SyncStore
	saved *cloudprofiles.Preview
}

var (
	platformProfile = &cloudprofiles.Profile{ID: "cloud_platform", OrgID: "org", Name: "Loomplane Cloud", PlatformOwned: true, Revision: 1}
	userProfile     = &cloudprofiles.Profile{ID: "cloud_user", OrgID: "org", Name: "Mine", Revision: 1}
	platformTarget  = &cloudprofiles.Target{ID: "target_platform", OrgID: "org", CloudID: "cloud_platform", Name: "eu-west", DisplayName: "EU West", Runtime: "platform", Provider: "platform", Region: "eu-west", Revision: 1, Tags: []string{}}
)

func (st *platformSyncStore) GetProfile(_ context.Context, org, _, id string) (*cloudprofiles.Profile, error) {
	for _, p := range []*cloudprofiles.Profile{platformProfile, userProfile} {
		if p.OrgID == org && p.ID == id {
			c := *p
			return &c, nil
		}
	}
	return nil, cloudprofiles.ErrNotFound
}
func (st *platformSyncStore) GetTarget(_ context.Context, org, _, cloud, id string) (*cloudprofiles.Target, error) {
	if org == platformTarget.OrgID && cloud == platformTarget.CloudID && id == platformTarget.ID {
		c := *platformTarget
		return &c, nil
	}
	return nil, cloudprofiles.ErrNotFound
}
func (st *platformSyncStore) ListTargets(context.Context, string, string, string) ([]*cloudprofiles.Target, error) {
	return []*cloudprofiles.Target{}, nil
}
func (st *platformSyncStore) Resolve(_ context.Context, org, project, env string) (*cloudprofiles.Assignment, error) {
	return &cloudprofiles.Assignment{OrgID: org, ProjectID: project, EnvironmentID: env, CloudID: platformProfile.ID, CloudRevision: 1, Revision: 1, GrantRevision: 1, Targets: []cloudprofiles.Target{*platformTarget}}, nil
}
func (st *platformSyncStore) AssignmentGeneration(context.Context, string, string, string) (int64, error) {
	return 1, nil
}
func (st *platformSyncStore) SnapshotBindings(context.Context, string, string) (json.RawMessage, error) {
	return json.RawMessage(`[]`), nil
}
func (st *platformSyncStore) SavePreview(_ context.Context, p *cloudprofiles.Preview, _ *cloudprofiles.Snapshot) error {
	st.saved = p
	return nil
}

type platformProjects struct{}

func (platformProjects) GetProject(_ context.Context, org, id string) (*projects.Project, error) {
	return &projects.Project{ID: id, OrgID: org}, nil
}
func (platformProjects) ListEnvironments(_ context.Context, org, project string) ([]*projects.Environment, error) {
	return []*projects.Environment{{ID: "env", OrgID: org, ProjectID: project, Name: "production"}}, nil
}
func (platformProjects) GetEnvironment(_ context.Context, org, project, id string) (*projects.Environment, error) {
	return &projects.Environment{ID: id, OrgID: org, ProjectID: project, Name: "production"}, nil
}

type allowAuthz struct{}

func (allowAuthz) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// A shared change rewrites the Cloud itself, which the platform-owned
// Cloud never takes. Moving one environment off it is the workspace's call.
func TestPreviewCloudChangeOnPlatformOwnedCloud(t *testing.T) {
	st := &platformSyncStore{}
	s := NewService(Deps{CloudProfiles: st, Projects: platformProjects{}, Authz: allowAuthz{}})
	p := security.Principal{OrgID: "org", UserID: "user"}
	ctx := t.Context()

	_, err := s.PreviewCloudChange(ctx, p, "proj", cloudprofiles.ChangeInput{CloudID: "cloud_platform", TargetID: "target_platform", Revision: 1, Provider: "cloud-run", ConnectionID: "conn", Region: "us-central1"})
	if !errors.Is(err, cloudprofiles.ErrPlatformOwned) {
		t.Fatalf("target change: %v, want ErrPlatformOwned", err)
	}
	_, err = s.PreviewCloudChange(ctx, p, "proj", cloudprofiles.ChangeInput{CloudID: "cloud_platform", Default: true, Revision: 1, Provider: "cloud-run", ConnectionID: "conn"})
	if !errors.Is(err, cloudprofiles.ErrPlatformOwned) {
		t.Fatalf("default switch: %v, want ErrPlatformOwned", err)
	}
	if st.saved != nil {
		t.Fatal("a refused change saved a preview")
	}

	preview, err := s.PreviewCloudChange(ctx, p, "proj", cloudprofiles.ChangeInput{CloudID: "cloud_user", EnvironmentID: "env", Revision: 1})
	if err != nil {
		t.Fatalf("moving an environment off the platform Cloud: %v", err)
	}
	if st.saved == nil || preview.BeforeAssignment == nil || preview.BeforeAssignment.CloudID != "cloud_platform" || preview.AfterAssignment == nil || preview.AfterAssignment.CloudID != "cloud_user" {
		t.Fatalf("preview = %+v", preview)
	}
}
