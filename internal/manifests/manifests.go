// Package manifests keeps each project's loomplane.yaml, as a history of
// revisions. Saving never edits a revision: it adds one, so a plan can name
// exactly the manifest it was made from.
package manifests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// historyLimit is how many revisions the history shows.
const historyLimit = 50

// Errors the service and stores return.
var (
	ErrNotFound = errors.New("this project has no manifest yet")
	// ErrConflict is a save that raced another one.
	ErrConflict = errors.New("someone saved the manifest at the same time; load it again and retry")
	ErrInvalid  = errors.New("invalid manifest request")
)

// Revision is one saved version of a project's manifest.
type Revision struct {
	expectedRevision *int
	ID               string `json:"id" forge:"id"`
	OrgID            string `json:"org_id"`
	ProjectID        string `json:"project_id"`
	// Revision counts from 1 within the project.
	Revision int `json:"revision"`
	// YAML is the manifest as it was saved, comments and all.
	YAML string `json:"yaml"`
	// Manifest is YAML, parsed.
	Manifest *manifest.Manifest `json:"manifest"`
	// Digest identifies the parsed manifest: two revisions that mean the
	// same thing share it, whatever their comments or formatting.
	Digest    string    `json:"digest"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Store persists revisions, scoped to an organization.
type Store interface {
	// CreateRevision numbers r after the project's latest revision and
	// inserts it. A concurrent save of the same project is ErrConflict.
	CreateRevision(ctx context.Context, r *Revision) error
	// CurrentRevision returns the project's latest revision, or
	// ErrNotFound.
	CurrentRevision(ctx context.Context, orgID, projectID string) (*Revision, error)
	GetRevision(ctx context.Context, orgID, id string) (*Revision, error)
	// ListRevisions returns a project's latest revisions, newest first.
	ListRevisions(ctx context.Context, orgID, projectID string, limit int) ([]*Revision, error)
}

// Authorizer decides whether a principal may act.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Projects looks up the project a manifest belongs to, and its
// environments.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
	ListEnvironments(ctx context.Context, orgID, projectID string) ([]*projects.Environment, error)
}

// Service is the manifest API: every method checks the caller's role.
type Service struct {
	store    Store
	projects Projects
	authz    Authorizer
	audit    projects.Auditor
	now      func() time.Time
}

// NewService returns a Service. audit may be nil.
func NewService(store Store, prj Projects, authz Authorizer, audit projects.Auditor) *Service {
	return &Service{store: store, projects: prj, authz: authz, audit: audit, now: time.Now}
}

// SaveInput is a new manifest, as YAML. A form that builds a manifest
// sends Manifest instead, and the service writes the YAML.
type SaveInput struct {
	// BaseRevision makes a save conditional on the revision you read.
	// Zero expects no saved manifest; nil allows an unconditional save.
	BaseRevision *int               `json:"base_revision,omitempty"`
	YAML         string             `json:"yaml,omitempty"`
	Manifest     *manifest.Manifest `json:"manifest,omitempty"`
}

// EditInput is one change to the current manifest, made to its YAML so
// the comments in it survive: add a resource, update or remove one, ignore
// a resource kind, change an existing entry's placement, link a service to
// a need or unlink it, or set where one dependency of a catalog resource
// comes from.
type EditInput struct {
	AddResource    *NewResource         `json:"add_resource,omitempty"`
	UpdateResource *ResourceUpdateInput `json:"update_resource,omitempty"`
	RemoveResource string               `json:"remove_resource,omitempty"`
	Ignore         string               `json:"ignore,omitempty"`
	Placement      *manifest.Placement  `json:"placement,omitempty"`
	Link           *ServiceLink         `json:"link,omitempty"`
	Unlink         *ServiceLink         `json:"unlink,omitempty"`
	SetUse         *ResourceUse         `json:"set_use,omitempty"`
	// BaseRevision makes the edit conditional on the revision the caller
	// decided it from: when another save landed since, the edit is
	// ErrConflict and changes nothing. Nil edits whatever is current. It
	// isn't part of the API: a caller there edits the current revision.
	BaseRevision *int `json:"-"`
}

// ResourceUpdateInput changes an existing resource. A field left out
// keeps what is there; Provider or Target set to "" removes it. ClearGPU
// removes the GPU request, and can't be sent with GPU. Each setting is
// set, and a setting set to "" is deleted. Environment, one of the
// project's environments, makes the change in that environment's block
// instead of on the resource; Enabled turns the resource on or off there,
// and needs Environment.
type ResourceUpdateInput struct {
	Resource    string            `json:"resource"`
	Environment string            `json:"environment,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Provider    *string           `json:"provider,omitempty"`
	Target      *string           `json:"target,omitempty"`
	GPU         *manifest.GPU     `json:"gpu,omitempty"`
	ClearGPU    bool              `json:"clear_gpu,omitempty"`
	Settings    map[string]string `json:"settings,omitempty"`
}

// ServiceLink names a service and something it needs. Link adds the need,
// with the options the service passes to it; Unlink removes the need and
// its options, and ignores Options.
type ServiceLink struct {
	Service string            `json:"service"`
	Need    string            `json:"need"`
	Options map[string]string `json:"options,omitempty"`
}

// ResourceUse sets where one dependency of a catalog resource comes from.
// A Use with nothing set asks for a new dependency.
type ResourceUse struct {
	Resource   string       `json:"resource"`
	Dependency string       `json:"dependency"`
	Use        manifest.Use `json:"use"`
}

// NewResource is a resource to add to the manifest.
type NewResource struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	// Target is the checked server connection a catalog resource runs on.
	Target  string                  `json:"target,omitempty"`
	Version string                  `json:"version,omitempty"`
	Uses    map[string]manifest.Use `json:"uses,omitempty"`
	// Settings are values the catalog kind takes, by setting name.
	Settings map[string]string `json:"settings,omitempty"`
	// GPU asks for GPUs, for a catalog kind that can use them.
	GPU *manifest.GPU `json:"gpu,omitempty"`
	// EnabledIn names one of the project's environments. The resource is
	// written with environments.<EnabledIn>.enabled: true, in the same
	// revision.
	EnabledIn string `json:"enabled_in,omitempty"`
}

// Edit applies one change to the project's current manifest and saves the
// result as a new revision. It keeps the YAML's comments and order.
func (s *Service) Edit(ctx context.Context, p security.Principal, projectID string, in EditInput) (*Revision, bool, error) {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, projectID); err != nil {
		return nil, false, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, false, err
	}
	cur, err := s.store.CurrentRevision(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, false, err
	}
	if in.BaseRevision != nil && *in.BaseRevision != cur.Revision {
		return nil, false, ErrConflict
	}
	var out []byte
	edits := 0
	for _, present := range []bool{in.AddResource != nil, in.UpdateResource != nil, in.RemoveResource != "", in.Ignore != "", in.Placement != nil, in.Link != nil, in.Unlink != nil, in.SetUse != nil} {
		if present {
			edits++
		}
	}
	switch {
	case edits > 1:
		return nil, false, fmt.Errorf("%w: make one edit at a time", ErrInvalid)
	case in.AddResource != nil:
		out, err = s.addResource(ctx, p.OrgID, projectID, []byte(cur.YAML), in.AddResource)
	case in.UpdateResource != nil:
		out, err = s.updateResource(ctx, p.OrgID, projectID, []byte(cur.YAML), in.UpdateResource)
	case in.RemoveResource != "":
		out, err = manifest.WithoutResource([]byte(cur.YAML), strings.TrimSpace(in.RemoveResource))
	case in.Ignore != "":
		out, err = manifest.WithIgnored([]byte(cur.YAML), strings.TrimSpace(in.Ignore))
	case in.Placement != nil:
		out, err = manifest.WithPlacement([]byte(cur.YAML), *in.Placement)
	case in.Link != nil:
		l := in.Link
		out, err = manifest.WithNeed([]byte(cur.YAML), strings.TrimSpace(l.Service), strings.TrimSpace(l.Need), l.Options)
	case in.Unlink != nil:
		l := in.Unlink
		out, err = manifest.WithoutNeed([]byte(cur.YAML), strings.TrimSpace(l.Service), strings.TrimSpace(l.Need))
	case in.SetUse != nil:
		u := in.SetUse
		out, err = manifest.WithUse([]byte(cur.YAML), strings.TrimSpace(u.Resource), strings.TrimSpace(u.Dependency), u.Use)
	default:
		return nil, false, fmt.Errorf("%w: say what to change: add_resource, update_resource, remove_resource, ignore, placement, link, unlink or set_use", ErrInvalid)
	}
	if err != nil {
		return nil, false, err
	}
	return s.Save(ctx, p, projectID, SaveInput{YAML: string(out), BaseRevision: &cur.Revision})
}

// addResource writes a new resource, turned on in the environment it
// names, if it names one.
func (s *Service) addResource(ctx context.Context, orgID, projectID string, doc []byte, r *NewResource) ([]byte, error) {
	name := strings.TrimSpace(r.Name)
	environment := strings.TrimSpace(r.EnabledIn)
	if environment != "" {
		if err := s.checkEnvironment(ctx, orgID, projectID, environment); err != nil {
			return nil, err
		}
	}
	out, err := manifest.WithResource(doc, name, manifest.Resource{
		Kind: r.Kind, Provider: r.Provider, Target: r.Target, Version: r.Version, Uses: r.Uses,
		Settings: r.Settings, GPU: r.GPU,
	})
	if err != nil || environment == "" {
		return out, err
	}
	return manifest.WithResourceEnabled(out, name, environment, true)
}

// checkEnvironment refuses an environment the project doesn't have.
func (s *Service) checkEnvironment(ctx context.Context, orgID, projectID, name string) error {
	envs, err := s.projects.ListEnvironments(ctx, orgID, projectID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(envs, func(e *projects.Environment) bool { return e.Name == name }) {
		return fmt.Errorf("%w: the project has no environment called %s", ErrInvalid, name)
	}
	return nil
}

// updateResource changes an existing resource's YAML, on the resource or
// in one of the project's environments.
func (s *Service) updateResource(ctx context.Context, orgID, projectID string, doc []byte, in *ResourceUpdateInput) ([]byte, error) {
	if in.ClearGPU && in.GPU != nil {
		return nil, fmt.Errorf("%w: send gpu or clear_gpu, not both", ErrInvalid)
	}
	environment := strings.TrimSpace(in.Environment)
	if environment != "" {
		if err := s.checkEnvironment(ctx, orgID, projectID, environment); err != nil {
			return nil, err
		}
	}
	u := manifest.ResourceUpdate{
		Environment: environment, Enabled: in.Enabled,
		Provider: in.Provider, Target: in.Target, Settings: in.Settings,
	}
	switch {
	case in.ClearGPU:
		var none *manifest.GPU
		u.GPU = &none
	case in.GPU != nil:
		u.GPU = &in.GPU
	}
	return manifest.WithResourceUpdate(doc, strings.TrimSpace(in.Resource), u)
}

// Current returns the project's manifest.
func (s *Service) Current(ctx context.Context, p security.Principal, projectID string) (*Revision, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	return s.store.CurrentRevision(ctx, p.OrgID, projectID)
}

// History returns the project's latest revisions, newest first.
func (s *Service) History(ctx context.Context, p security.Principal, projectID string) ([]*Revision, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	return s.store.ListRevisions(ctx, p.OrgID, projectID, historyLimit)
}

// Save validates a manifest and adds it as the project's next revision.
// Saving what's already current changes nothing and returns the current
// revision, so saving twice doesn't make history noise.
func (s *Service) Save(ctx context.Context, p security.Principal, projectID string, in SaveInput) (*Revision, bool, error) {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, projectID); err != nil {
		return nil, false, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, false, err
	}
	text, m, err := read(in)
	if err != nil {
		return nil, false, err
	}
	digest, err := Digest(m)
	if err != nil {
		return nil, false, err
	}
	cur, err := s.store.CurrentRevision(ctx, p.OrgID, projectID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	base := 0
	if cur != nil {
		base = cur.Revision
	}
	if in.BaseRevision != nil && base != *in.BaseRevision {
		return nil, false, ErrConflict
	}
	switch {
	case err == nil && cur.Digest == digest && cur.YAML == text:
		return cur, false, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return nil, false, err
	}
	id, err := typeid.Generate("mfst")
	if err != nil {
		return nil, false, fmt.Errorf("generate manifest id: %w", err)
	}
	r := &Revision{
		expectedRevision: in.BaseRevision,
		ID:               id.String(), OrgID: p.OrgID, ProjectID: projectID, YAML: text, Manifest: m, Digest: digest,
		CreatedBy: p.UserID, CreatedAt: s.now().UTC(),
	}
	if err := s.store.CreateRevision(ctx, r); err != nil {
		return nil, false, err
	}
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: "manifest.save", Severity: "info", ProjectID: projectID, Principal: p,
			Attributes: map[string]any{
				"manifest_id": r.ID, "revision": r.Revision,
				"services": len(m.Services), "resources": len(m.Resources),
			},
		})
	}
	return r, true, nil
}

// read turns a save into YAML and the manifest it means.
func read(in SaveInput) (string, *manifest.Manifest, error) {
	switch {
	case in.Manifest != nil && strings.TrimSpace(in.YAML) != "":
		return "", nil, fmt.Errorf("%w: send yaml or manifest, not both", ErrInvalid)
	case in.Manifest != nil:
		if err := in.Manifest.Validate(); err != nil {
			return "", nil, err
		}
		data, err := manifest.Marshal(in.Manifest)
		if err != nil {
			return "", nil, err
		}
		return string(data), in.Manifest, nil
	case strings.TrimSpace(in.YAML) == "":
		return "", nil, fmt.Errorf("%w: send the manifest as yaml", ErrInvalid)
	}
	m, err := manifest.Parse([]byte(in.YAML))
	if err != nil {
		return "", nil, err
	}
	return in.YAML, m, nil
}

// Digest identifies a parsed manifest.
func Digest(m *manifest.Manifest) (string, error) {
	data, err := manifest.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Get returns one revision, for planning. It doesn't check a role: only
// Loomplane's own workflow calls it, for a plan that was authorized.
func (s *Service) Get(ctx context.Context, orgID, id string) (*Revision, error) {
	return s.store.GetRevision(ctx, orgID, id)
}

// Latest returns a project's current revision, for planning.
func (s *Service) Latest(ctx context.Context, orgID, projectID string) (*Revision, error) {
	return s.store.CurrentRevision(ctx, orgID, projectID)
}
