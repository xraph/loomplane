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
	ID        string `json:"id" forge:"id"`
	OrgID     string `json:"org_id"`
	ProjectID string `json:"project_id"`
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

// Projects looks up the project a manifest belongs to.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
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
	YAML     string             `json:"yaml,omitempty"`
	Manifest *manifest.Manifest `json:"manifest,omitempty"`
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
		ID: id.String(), OrgID: p.OrgID, ProjectID: projectID, YAML: text, Manifest: m, Digest: digest,
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
