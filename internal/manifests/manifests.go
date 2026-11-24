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
	"maps"
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
// the comments in it survive: add a service or a resource, update or
// remove a resource, ignore
// a resource kind, change an existing entry's placement, link a service to
// a need or unlink it, or set where one dependency of a catalog resource
// comes from.
type EditInput struct {
	AddService     *NewServiceInput     `json:"add_service,omitempty"`
	AddResource    *NewResource         `json:"add_resource,omitempty"`
	UpdateResource *ResourceUpdateInput `json:"update_resource,omitempty"`
	RemoveResource string               `json:"remove_resource,omitempty"`
	Ignore         string               `json:"ignore,omitempty"`
	Placement      *manifest.Placement  `json:"placement,omitempty"`
	Link           *ServiceLink         `json:"link,omitempty"`
	Unlink         *ServiceLink         `json:"unlink,omitempty"`
	SetUse         *ResourceUse         `json:"set_use,omitempty"`
	// Wire sets which variables a service reads one of its needs in.
	Wire *ServiceWiring `json:"wire,omitempty"`
	// DryRun makes the edit without saving it, and returns the revision
	// it would save, so a form can show the YAML before anyone commits to
	// it. The revision has no ID; its Revision is the number it would get.
	DryRun bool `json:"dry_run,omitempty"`
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

// ServiceWiring is which variables a service reads one of its needs in,
// each with what it holds: url, host, port, user, password or database.
// No variables go back to the one the need's kind names.
type ServiceWiring struct {
	Service string            `json:"service"`
	Need    string            `json:"need"`
	Env     map[string]string `json:"env,omitempty"`
}

// ResourceUse sets where one dependency of a catalog resource comes from.
// A Use with nothing set asks for a new dependency.
type ResourceUse struct {
	Resource   string       `json:"resource"`
	Dependency string       `json:"dependency"`
	Use        manifest.Use `json:"use"`
}

// DependencyAttachment references a provider resource for one catalog dependency.
type DependencyAttachment struct {
	ResourceID string `json:"resource_id"`
	Kind       string `json:"kind"`
}

// NewResource is a resource to add to the manifest.
type NewResource struct {
	Attachments map[string]DependencyAttachment `json:"attachments,omitempty"`
	// ConsumerOptions are applied to each service listed in NeededBy.
	ConsumerOptions  map[string]string `json:"consumer_options,omitempty"`
	ExistingResource string            `json:"existing_resource,omitempty"`
	Region           string            `json:"region,omitempty"`
	Name             string            `json:"name"`
	Kind             string            `json:"kind"`
	Provider         string            `json:"provider,omitempty"`
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
	// Only, with EnabledIn, turns the resource off in every other
	// environment the project has, so it runs in EnabledIn alone.
	Only bool `json:"only,omitempty"`
	// NeededBy names services that need the new resource. Each gets it in
	// its needs, and with it the resource's address in a variable, in the
	// same revision.
	NeededBy []string `json:"needed_by,omitempty"`
	// Wiring is, by service in NeededBy, the variables it reads the new
	// resource in, as ServiceWiring's Env. A service left out reads the one
	// the kind names.
	Wiring map[string]map[string]string `json:"wiring,omitempty"`
}

// NewServiceInput is a service to add to the manifest: where its code is and,
// optionally, where it runs and what it needs. Anything left out is
// inherited from defaults or comes from analysis, as for a service
// written by hand.
type NewServiceInput struct {
	Name string `json:"name"`
	// Source is where the service's code is, as analysis finds it: a
	// directory, a Dockerfile or a Procfile process.
	Source string `json:"source"`
	// Repo is the alias of the repository Source is in. A project with one
	// repository can leave it out.
	Repo string `json:"repo,omitempty"`
	// Kind corrects what analysis decides the service is: http-service,
	// web-app, static-site or worker.
	Kind string `json:"kind,omitempty"`
	// CloudTarget puts the service on a Cloud target, in the Cloud that
	// Cloud names or the project's default. Neither goes with Provider or
	// Target.
	Cloud       string `json:"cloud,omitempty"`
	CloudTarget string `json:"cloud_target,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Region      string `json:"region,omitempty"`
	// Target is the checked server or cluster connection it runs on.
	Target string `json:"target,omitempty"`
	Port   int    `json:"port,omitempty"`
	Public *bool  `json:"public,omitempty"`
	// Needs names the services and resources it talks to. Each must be in
	// the manifest already.
	Needs []string `json:"needs,omitempty"`
	// NeededBy names services that need the new one, and get its address.
	NeededBy []string `json:"needed_by,omitempty"`
	// EnabledIn and Only work as they do for a resource.
	EnabledIn string `json:"enabled_in,omitempty"`
	Only      bool   `json:"only,omitempty"`
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
	// Adding something to a project with no manifest yet starts one, so
	// the first service doesn't wait on a saved draft.
	if errors.Is(err, ErrNotFound) && (in.AddService != nil || in.AddResource != nil) {
		cur, err = &Revision{YAML: "version: 1\n"}, nil
	}
	if err != nil {
		return nil, false, err
	}
	if in.BaseRevision != nil && *in.BaseRevision != cur.Revision {
		return nil, false, ErrConflict
	}
	var out []byte
	edits := 0
	for _, present := range []bool{in.AddService != nil, in.AddResource != nil, in.UpdateResource != nil, in.RemoveResource != "", in.Ignore != "", in.Placement != nil, in.Link != nil, in.Unlink != nil, in.SetUse != nil, in.Wire != nil} {
		if present {
			edits++
		}
	}
	switch {
	case edits > 1:
		return nil, false, fmt.Errorf("%w: make one edit at a time", ErrInvalid)
	case in.AddService != nil:
		out, err = s.addService(ctx, p.OrgID, projectID, []byte(cur.YAML), in.AddService)
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
	case in.Wire != nil:
		w := in.Wire
		out, err = manifest.WithWiring([]byte(cur.YAML), strings.TrimSpace(w.Service), strings.TrimSpace(w.Need), w.Env)
	case in.SetUse != nil:
		u := in.SetUse
		out, err = manifest.WithUse([]byte(cur.YAML), strings.TrimSpace(u.Resource), strings.TrimSpace(u.Dependency), u.Use)
	default:
		return nil, false, fmt.Errorf("%w: say what to change: add_service, add_resource, update_resource, remove_resource, ignore, placement, link, unlink or set_use", ErrInvalid)
	}
	if err != nil {
		return nil, false, err
	}
	if in.DryRun {
		text, m, err := read(SaveInput{YAML: string(out)})
		if err != nil {
			return nil, false, err
		}
		digest, err := Digest(m)
		if err != nil {
			return nil, false, err
		}
		return &Revision{
			OrgID: p.OrgID, ProjectID: projectID, Revision: cur.Revision + 1, YAML: text, Manifest: m, Digest: digest,
			CreatedBy: p.UserID, CreatedAt: s.now().UTC(),
		}, false, nil
	}
	return s.Save(ctx, p, projectID, SaveInput{YAML: string(out), BaseRevision: &cur.Revision})
}

// addResource writes a new resource, turned on in the environment it
// names, if it names one, and needed by the services it names.
func (s *Service) addResource(ctx context.Context, orgID, projectID string, doc []byte, r *NewResource) ([]byte, error) {
	name := strings.TrimSpace(r.Name)
	if len(r.Attachments) > manifest.MaxResources {
		return nil, fmt.Errorf("%w: too many resource attachments", ErrInvalid)
	}
	for _, dep := range slices.Sorted(maps.Keys(r.Attachments)) {
		attachment := r.Attachments[dep]
		alias := r.Uses[dep].Resource
		if alias == "" || attachment.ResourceID == "" || attachment.Kind == "" {
			return nil, fmt.Errorf("%w: an attachment needs a resource name, inventory ID and kind for %s", ErrInvalid, dep)
		}
		var err error
		doc, err = manifest.WithResource(doc, alias, manifest.Resource{Kind: attachment.Kind, ExistingResource: attachment.ResourceID})
		if err != nil {
			return nil, err
		}
	}
	out, err := manifest.WithResource(doc, name, manifest.Resource{
		Kind: r.Kind, Provider: r.Provider, Target: r.Target, Version: r.Version, Uses: r.Uses,
		Settings: r.Settings, GPU: r.GPU, ExistingResource: r.ExistingResource, Region: r.Region,
	})
	if err != nil {
		return nil, err
	}
	for svc := range r.Wiring {
		if !slices.Contains(trimmed(r.NeededBy), svc) {
			return nil, fmt.Errorf("%w: wiring names %s, which isn't in needed_by", ErrInvalid, svc)
		}
	}
	out, err = s.placeNew(ctx, orgID, projectID, out, "resources", name, r.EnabledIn, r.Only, r.NeededBy, r.ConsumerOptions)
	if err != nil {
		return nil, err
	}
	for _, svc := range slices.Sorted(maps.Keys(r.Wiring)) {
		if out, err = manifest.WithWiring(out, svc, name, r.Wiring[svc]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addService writes a new service, with the same environment and
// needed-by choices a resource has.
func (s *Service) addService(ctx context.Context, orgID, projectID string, doc []byte, in *NewServiceInput) ([]byte, error) {
	name := strings.TrimSpace(in.Name)
	needs := trimmed(in.Needs)
	out, err := manifest.WithService(doc, name, manifest.NewService{
		Repo: in.Repo, Source: in.Source, Kind: in.Kind, Cloud: in.Cloud, CloudTarget: in.CloudTarget, Provider: in.Provider, Region: in.Region,
		Target: in.Target, Port: in.Port, Public: in.Public, Needs: needs,
	})
	if err != nil {
		return nil, err
	}
	return s.placeNew(ctx, orgID, projectID, out, "services", name, in.EnabledIn, in.Only, in.NeededBy, nil)
}

// placeNew finishes adding name to section: on in environment, off in
// every other one when only, and needed by the services in neededBy. It
// refuses a result where a service would need something that's off in an
// environment the service runs in, naming both, so the mistake shows here
// rather than as a problem on a later plan.
func (s *Service) placeNew(ctx context.Context, orgID, projectID string, doc []byte, section, name, environment string, only bool, neededBy []string, options map[string]string) ([]byte, error) {
	environment = strings.TrimSpace(environment)
	if only && environment == "" {
		return nil, fmt.Errorf("%w: only needs enabled_in, the environment %s runs in", ErrInvalid, name)
	}
	var envs []string
	if environment != "" || len(neededBy) > 0 {
		list, err := s.projects.ListEnvironments(ctx, orgID, projectID)
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			envs = append(envs, e.Name)
		}
	}
	if environment != "" {
		if !slices.Contains(envs, environment) {
			return nil, fmt.Errorf("%w: the project has no environment called %s", ErrInvalid, environment)
		}
		enabled := map[string]bool{environment: true}
		if only {
			for _, e := range envs {
				if e != environment {
					enabled[e] = false
				}
			}
		}
		var err error
		if doc, err = manifest.WithEnabledIn(doc, section, name, enabled); err != nil {
			return nil, err
		}
	}
	for _, svc := range trimmed(neededBy) {
		if svc == name {
			return nil, fmt.Errorf("%w: %s can't need itself", ErrInvalid, name)
		}
		var err error
		if doc, err = manifest.WithNeed(doc, svc, name, options); err != nil {
			return nil, err
		}
	}
	m, err := manifest.Parse(doc)
	if err != nil {
		return nil, err
	}
	if err := reachable(m, envs, name); err != nil {
		return nil, err
	}
	return doc, nil
}

// reachable refuses a manifest where a service needs something that's off
// in an environment the service runs in, for each need that involves the
// entry called added: the added service's own needs, and every need on it.
func reachable(m *manifest.Manifest, envs []string, added string) error {
	on := func(name, env string) (bool, bool) {
		if svc, ok := m.Services[name]; ok {
			e := svc.For(env, m.Defaults).Enabled
			return e == nil || *e, true
		}
		if res, ok := m.Resources[name]; ok {
			e := res.For(env, m.Defaults).Enabled
			return e == nil || *e, true
		}
		return false, false
	}
	for _, env := range envs {
		for _, svcName := range m.ServiceNames() {
			if running, _ := on(svcName, env); !running {
				continue
			}
			for _, need := range m.Services[svcName].Needs {
				if svcName != added && need != added {
					continue
				}
				if running, known := on(need, env); known && !running {
					return fmt.Errorf("%w: %s runs in %s and needs %s, which would be off there. Add %s to %s too, or turn %s off in %s first", ErrInvalid, svcName, env, need, need, env, svcName, env)
				}
			}
		}
	}
	return nil
}

// trimmed is list with each name trimmed and the empty ones dropped.
func trimmed(list []string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
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
