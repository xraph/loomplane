package cloudprofiles

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

const (
	maxName = 64
	maxTags = 16
	maxTag  = 32
)

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Projects reads authoritative scope boundaries.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
	GetEnvironment(ctx context.Context, orgID, projectID, id string) (*projects.Environment, error)
}

// Connections lists verified accounts without revealing their credentials.
type Connections interface {
	All(ctx context.Context, orgID string) ([]*connections.Connection, error)
}

// Authorizer checks the caller's project permission.
type Authorizer interface {
	Require(context.Context, security.Principal, security.Permission, string) error
}

// Deps are the service's scoped stores, capability catalog and policy hooks.
type Deps struct {
	Store       Store
	Projects    Projects
	Connections Connections
	Providers   *provider.Registry
	Authz       Authorizer
	Audit       projects.Auditor
}

// Service manages Clouds and metadata. It cannot write assignments or redirect
// an existing target to another account, provider or region.
type Service struct {
	store       Store
	projects    Projects
	connections Connections
	providers   *provider.Registry
	authz       Authorizer
	audit       projects.Auditor
	now         func() time.Time
}

// NewService constructs the configuration service. Audit may be nil.
func NewService(d Deps) *Service {
	return &Service{store: d.Store, projects: d.Projects, connections: d.Connections, providers: d.Providers, authz: d.Authz, audit: d.Audit, now: time.Now}
}

// CreateInput names a new Cloud. DefaultPlacement is optional.
type CreateInput struct {
	Name             string            `json:"name"`
	DefaultPlacement *DefaultPlacement `json:"default_placement,omitempty"`
}

// SetDefaultPlacementInput replaces a Cloud's default at an expected revision.
// A null placement clears it.
type SetDefaultPlacementInput struct {
	DefaultPlacement *DefaultPlacement `json:"default_placement" nullable:"true" required:"true"`
	Revision         int64             `json:"revision"`
}

// UpdateInput renames a Cloud at an expected revision.
type UpdateInput struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}

// CreateTargetInput selects the initial placement of a new immutable slug.
type CreateTargetInput struct {
	Runtime      string `json:"runtime"`
	PoolID       string `json:"pool_id,omitempty"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	Provider     string `json:"provider"`
	ConnectionID string `json:"connection_id"`
	Region       string `json:"region"`
	// Tags are lowercase slugs, such as frontend, eu or gpu.
	Tags []string `json:"tags,omitempty"`
}

// UpdateTargetInput edits display metadata at an expected revision. Omitted
// tags stay as they are; an empty list removes them all.
type UpdateTargetInput struct {
	DisplayName string    `json:"display_name"`
	Tags        *[]string `json:"tags,omitempty"`
	Revision    int64     `json:"revision"`
}

func (s *Service) scope(ctx context.Context, p security.Principal, projectID string, permission security.Permission) error {
	if permission == security.CloudAccountManage || projectID == "" {
		if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
			return err
		}
	}
	if projectID == "" {
		return nil
	}
	if err := s.authz.Require(ctx, p, permission, projectID); err != nil {
		return err
	}
	project, err := s.projects.GetProject(ctx, p.OrgID, projectID)
	if err != nil {
		return err
	}
	if project == nil || project.OrgID != p.OrgID || project.ID != projectID {
		return projects.ErrNotFound
	}
	return nil
}
func name(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxName {
		return "", fmt.Errorf("%w: use a name of 1 to %d characters", ErrInvalid, maxName)
	}
	return value, nil
}

// normalizeTags returns sorted, distinct tags or explains the first bad one.
func normalizeTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, tag := range in {
		tag = strings.TrimSpace(tag)
		if len(tag) > maxTag || !slugPattern.MatchString(tag) {
			return nil, fmt.Errorf("%w: tag %q must be a lowercase slug of at most %d characters", ErrInvalid, tag, maxTag)
		}
		out = append(out, tag)
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > maxTags {
		return nil, fmt.Errorf("%w: a target can carry at most %d tags", ErrInvalid, maxTags)
	}
	return out, nil
}

// defaultPlacement checks a Cloud default like a managed target's placement.
func (s *Service) defaultPlacement(ctx context.Context, orgID string, in *DefaultPlacement) (*DefaultPlacement, error) {
	if in == nil {
		return nil, nil
	}
	out := &DefaultPlacement{Provider: strings.TrimSpace(in.Provider), ConnectionID: strings.TrimSpace(in.ConnectionID), Region: strings.TrimSpace(in.Region)}
	if out.Provider == "" || out.ConnectionID == "" {
		return nil, fmt.Errorf("%w: a default placement needs one provider and one connection", ErrInvalid)
	}
	if err := s.placement(ctx, orgID, CreateTargetInput{Runtime: "managed", Provider: out.Provider, ConnectionID: out.ConnectionID, Region: out.Region}); err != nil {
		return nil, err
	}
	return out, nil
}

// placementAttributes is an untyped nil for an absent default, so audit
// readers see null rather than an empty object.
func placementAttributes(p *DefaultPlacement) any {
	if p == nil {
		return nil
	}
	return map[string]any{"provider": p.Provider, "connection_id": p.ConnectionID, "region": p.Region}
}

func (s *Service) record(ctx context.Context, p security.Principal, projectID, action string, attributes map[string]any) {
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: projectID, Principal: p, Attributes: attributes})
	}
}

// List returns the project's Clouds ordered by name and ID.
func (s *Service) List(ctx context.Context, p security.Principal, projectID string) ([]*Profile, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountRead); err != nil {
		return nil, err
	}
	return s.store.ListProfiles(ctx, p.OrgID, projectID)
}

// Create creates a Cloud without assigning it to an environment.
func (s *Service) Create(ctx context.Context, p security.Principal, projectID string, in CreateInput) (*Profile, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountManage); err != nil {
		return nil, err
	}
	value, err := name(in.Name)
	if err != nil {
		return nil, err
	}
	fallback, err := s.defaultPlacement(ctx, p.OrgID, in.DefaultPlacement)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("cloud")
	if err != nil {
		return nil, fmt.Errorf("generate cloud id: %w", err)
	}
	now := s.now().UTC()
	c := &Profile{ID: id.String(), OrgID: p.OrgID, ProjectID: projectID, Name: value, DefaultPlacement: fallback, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateProfile(ctx, c); err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "cloud.create", map[string]any{"cloud_id": c.ID, "revision": c.Revision, "default_placement": placementAttributes(fallback)})
	return c, nil
}

// Get reads one Cloud within the requested project.
func (s *Service) Get(ctx context.Context, p security.Principal, projectID, id string) (*Profile, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountRead); err != nil {
		return nil, err
	}
	return s.store.GetProfile(ctx, p.OrgID, projectID, id)
}

// Update renames a Cloud without changing its targets or assignment.
func (s *Service) Update(ctx context.Context, p security.Principal, projectID, id string, in UpdateInput) (*Profile, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountManage); err != nil {
		return nil, err
	}
	c, err := s.store.GetProfile(ctx, p.OrgID, projectID, id)
	if err != nil {
		return nil, err
	}
	if in.Revision != c.Revision {
		return nil, ErrConflict
	}
	value, err := name(in.Name)
	if err != nil {
		return nil, err
	}
	c.Name = value
	c.Revision++
	c.UpdatedAt = s.now().UTC()
	if err := s.store.UpdateProfile(ctx, c, in.Revision); err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "cloud.update", map[string]any{"cloud_id": c.ID, "revision": c.Revision})
	return c, nil
}

// SetDefaultPlacement sets, replaces or clears the Cloud's single default
// provider and account. Replacing or clearing one is refused while any
// environment uses the Cloud, because it would move serving services without
// a reviewed sync.
func (s *Service) SetDefaultPlacement(ctx context.Context, p security.Principal, projectID, id string, in SetDefaultPlacementInput) (*Profile, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountManage); err != nil {
		return nil, err
	}
	c, err := s.store.GetProfile(ctx, p.OrgID, projectID, id)
	if err != nil {
		return nil, err
	}
	if in.Revision != c.Revision {
		return nil, ErrConflict
	}
	fallback, err := s.defaultPlacement(ctx, p.OrgID, in.DefaultPlacement)
	if err != nil {
		return nil, err
	}
	before := c.DefaultPlacement
	c.DefaultPlacement = fallback
	c.Revision++
	c.UpdatedAt = s.now().UTC()
	if err := s.store.UpdateProfile(ctx, c, in.Revision); err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "cloud.default_placement", map[string]any{"cloud_id": c.ID, "revision": c.Revision, "before": placementAttributes(before), "after": placementAttributes(fallback)})
	return c, nil
}

// Delete removes an unassigned Cloud and its targets.
func (s *Service) Delete(ctx context.Context, p security.Principal, projectID, id string) error {
	if err := s.scope(ctx, p, projectID, security.CloudAccountManage); err != nil {
		return err
	}
	if err := s.store.DeleteProfile(ctx, p.OrgID, projectID, id); err != nil {
		return err
	}
	s.record(ctx, p, projectID, "cloud.delete", map[string]any{"cloud_id": id})
	return nil
}

// ListTargets returns the Cloud's targets ordered by immutable slug.
func (s *Service) ListTargets(ctx context.Context, p security.Principal, projectID, cloudID string) ([]*Target, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountRead); err != nil {
		return nil, err
	}
	if _, err := s.store.GetProfile(ctx, p.OrgID, projectID, cloudID); err != nil {
		return nil, err
	}
	return s.store.ListTargets(ctx, p.OrgID, projectID, cloudID)
}

// GetTarget reads one target within its full scope.
func (s *Service) GetTarget(ctx context.Context, p security.Principal, projectID, cloudID, id string) (*Target, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountRead); err != nil {
		return nil, err
	}
	return s.store.GetTarget(ctx, p.OrgID, projectID, cloudID, id)
}

// CreateTarget selects a ready account matching an available provider.
func (s *Service) CreateTarget(ctx context.Context, p security.Principal, projectID, cloudID string, in CreateTargetInput) (*Target, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountManage); err != nil {
		return nil, err
	}
	if _, err := s.store.GetProfile(ctx, p.OrgID, projectID, cloudID); err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) > 48 || !slugPattern.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: target name must be a lowercase slug of at most 48 characters", ErrInvalid)
	}
	display := strings.TrimSpace(in.DisplayName)
	if display == "" {
		display = in.Name
	}
	display, err := name(display)
	if err != nil {
		return nil, err
	}
	in.Provider, in.ConnectionID, in.Region = strings.TrimSpace(in.Provider), strings.TrimSpace(in.ConnectionID), strings.TrimSpace(in.Region)
	if in.Runtime == "" {
		in.Runtime = "managed"
	}
	if in.PoolID != "" && (in.Runtime != "server" || in.Provider != "server" || in.ConnectionID != "") {
		return nil, ErrInvalid
	}
	if in.PoolID == "" && in.Runtime != "managed" {
		return nil, ErrInvalid
	}
	if placementErr := s.placement(ctx, p.OrgID, in); placementErr != nil {
		return nil, placementErr
	}
	tags, err := normalizeTags(in.Tags)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("target")
	if err != nil {
		return nil, fmt.Errorf("generate target id: %w", err)
	}
	now := s.now().UTC()
	target := &Target{Runtime: in.Runtime, PoolID: in.PoolID, ID: id.String(), OrgID: p.OrgID, ProjectID: projectID, CloudID: cloudID, Name: in.Name, DisplayName: display, Provider: in.Provider, ConnectionID: in.ConnectionID, Region: in.Region, Tags: tags, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateTarget(ctx, target); err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "cloud_target.create", map[string]any{"cloud_id": cloudID, "target_id": target.ID, "revision": target.Revision, "tags": tags})
	return target, nil
}
func (s *Service) placement(ctx context.Context, orgID string, in CreateTargetInput) error {
	if in.PoolID != "" {
		return nil
	}
	if s.providers == nil || s.connections == nil {
		return fmt.Errorf("%w: provider and account checks are unavailable", ErrInvalid)
	}
	capability, err := s.providers.Get(in.Provider)
	if err != nil || !capability.Available {
		return fmt.Errorf("%w: provider %q is unavailable", ErrInvalid, in.Provider)
	}
	if len(capability.Runs) == 0 && len(capability.Provisions) == 0 {
		return fmt.Errorf("%w: provider has no deployment capability", ErrInvalid)
	}
	all, err := s.connections.All(ctx, orgID)
	if err != nil {
		return err
	}
	for _, c := range all {
		if c.ID == in.ConnectionID && c.OrgID == orgID && c.Cloud == capability.Cloud && c.Status == connections.StatusReady {
			return nil
		}
	}
	return fmt.Errorf("%w: choose a ready connection for %s", ErrInvalid, capability.Cloud)
}

// UpdateTarget changes the display name and tags. DefaultPlacement changes require
// review. A tag change reaches services on their next reviewed plan, and
// advances the revision so plans approved against the old tags go stale.
func (s *Service) UpdateTarget(ctx context.Context, p security.Principal, projectID, cloudID, id string, in UpdateTargetInput) (*Target, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountManage); err != nil {
		return nil, err
	}
	target, err := s.store.GetTarget(ctx, p.OrgID, projectID, cloudID, id)
	if err != nil {
		return nil, err
	}
	if in.Revision != target.Revision {
		return nil, ErrConflict
	}
	display, err := name(in.DisplayName)
	if err != nil {
		return nil, err
	}
	attributes := map[string]any{"cloud_id": cloudID, "target_id": id}
	if in.Tags != nil {
		tags, tagErr := normalizeTags(*in.Tags)
		if tagErr != nil {
			return nil, tagErr
		}
		attributes["tags_before"], attributes["tags"] = target.Tags, tags
		target.Tags = tags
	}
	target.DisplayName = display
	target.Revision++
	target.UpdatedAt = s.now().UTC()
	if err := s.store.UpdateTarget(ctx, target, in.Revision); err != nil {
		return nil, err
	}
	attributes["revision"] = target.Revision
	s.record(ctx, p, projectID, "cloud_target.update", attributes)
	return target, nil
}

// DeleteTarget removes a target only while its Cloud is unassigned.
func (s *Service) DeleteTarget(ctx context.Context, p security.Principal, projectID, cloudID, id string) error {
	if err := s.scope(ctx, p, projectID, security.CloudAccountManage); err != nil {
		return err
	}
	if err := s.store.DeleteTarget(ctx, p.OrgID, projectID, cloudID, id); err != nil {
		return err
	}
	s.record(ctx, p, projectID, "cloud_target.delete", map[string]any{"cloud_id": cloudID, "target_id": id})
	return nil
}

// EnvironmentCloud returns the assignment and generation, including tombstones.
func (s *Service) EnvironmentCloud(ctx context.Context, p security.Principal, projectID, environmentID string) (*Assignment, int64, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountRead); err != nil {
		return nil, 0, err
	}
	reader, ok := s.store.(interface {
		EnvironmentCloud(context.Context, string, string, string) (*Assignment, int64, error)
	})
	if !ok {
		return nil, 0, fmt.Errorf("%w: assignment storage unavailable", ErrInvalid)
	}
	return reader.EnvironmentCloud(ctx, p.OrgID, projectID, environmentID)
}
