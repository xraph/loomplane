package fleets

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// Projects resolves project scope independently of request parameters.
type Projects interface {
	GetProject(context.Context, string, string) (*projects.Project, error)
}

// Deps supplies the existing authorization and audit boundaries.
type Deps struct {
	AdoptionInventory AdoptionInventory
	Resources         InstanceResourceAccess
	Secrets           PinnedSecrets
	Keys              security.PrincipalVerifier
	Store             Store
	Projects          Projects
	Authz             projects.Authorizer
	Audit             projects.Auditor
}

// Service manages fleet metadata. Provider effects belong to durable workflows.
type Service struct {
	adoptionInventory AdoptionInventory
	resources         InstanceResourceAccess
	secrets           PinnedSecrets
	keys              security.PrincipalVerifier
	store             Store
	projects          Projects
	authz             projects.Authorizer
	audit             projects.Auditor
}

// NewService binds scoped persistence and authorization.
func NewService(d Deps) *Service {
	return &Service{adoptionInventory: d.AdoptionInventory, resources: d.Resources, secrets: d.Secrets, keys: d.Keys, store: d.Store, projects: d.Projects, authz: d.Authz, audit: d.Audit}
}

// CreateFleet converts a project into a fleet without changing its environments.
func (s *Service) CreateFleet(ctx context.Context, p security.Principal, projectID string) (*fleet.Fleet, error) {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	f := &fleet.Fleet{ID: uuid.NewString(), OrgID: p.OrgID, ProjectID: projectID, Revision: 1, Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateFleet(ctx, f); err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "fleet.create", f.ID)
	return f, nil
}

// CreateStage establishes a bounded placement policy within a fleet.
func (s *Service) CreateStage(ctx context.Context, p security.Principal, fleetID string, in fleet.CreateStageInput) (*fleet.Stage, error) {
	f, err := s.store.GetFleet(ctx, p.OrgID, fleetID)
	if err != nil {
		return nil, err
	}
	if authErr := s.authz.Require(ctx, p, security.EnvironmentManage, f.ProjectID); authErr != nil {
		return nil, authErr
	}
	if !projects.ValidEnvironmentName(in.Name) || !slices.Contains(projects.Kinds(), in.EnvironmentKind) || in.MaxInstances < 0 || len(in.AllowedCloudIDs) == 0 || len(in.AllowedCloudIDs) > 100 {
		return nil, ErrInvalid
	}
	clouds := slices.Clone(in.AllowedCloudIDs)
	slices.Sort(clouds)
	for i, id := range clouds {
		if strings.TrimSpace(id) == "" || (i > 0 && clouds[i-1] == id) {
			return nil, ErrInvalid
		}
	}
	stage := &fleet.Stage{ID: uuid.NewString(), FleetID: f.ID, Name: in.Name, EnvironmentKind: in.EnvironmentKind, AllowedCloudIDs: clouds, MaxInstances: in.MaxInstances, PolicyRevision: 1, CreatedAt: time.Now().UTC()}
	if createErr := s.store.CreateStage(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, stage); createErr != nil {
		return nil, createErr
	}
	s.record(ctx, p, f.ProjectID, "fleet.stage.create", stage.ID)
	return stage, nil
}

func (s *Service) record(ctx context.Context, p security.Principal, project, action, id string) {
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: project, Principal: p, Attributes: map[string]any{"id": id}})
	}
}
