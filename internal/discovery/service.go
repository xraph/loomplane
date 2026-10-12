package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

const (
	// listLimit is how many past analyses a project's list shows.
	listLimit = 20
	// activeWindow is how long an unfinished analysis blocks another. One
	// still unfinished after this is presumed stuck (no worker running,
	// say), and a new one may start.
	activeWindow = time.Hour
)

// Authorizer decides whether a principal may act. *security.Authorizer is
// the real one.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Projects looks up the project an analysis belongs to.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
}

// Enqueuer starts an analysis in the background. Loomplane's is a Dispatch
// job on the analysis queue, which only workers take (internal/orchestration).
type Enqueuer interface {
	AnalyzeRepository(ctx context.Context, orgID, analysisID string) error
}

// Service is the analysis API: every method checks the caller's role first.
type Service struct {
	store    Store
	projects Projects
	authz    Authorizer
	enqueue  Enqueuer
	audit    projects.Auditor
	access   Access
	now      func() time.Time
}

// NewService returns a Service. audit may be nil.
func NewService(store Store, prj Projects, authz Authorizer, enqueue Enqueuer, audit projects.Auditor) *Service {
	return &Service{store: store, projects: prj, authz: authz, enqueue: enqueue, audit: audit, now: time.Now}
}

func (s *Service) record(ctx context.Context, p security.Principal, projectID, action, severity string, attrs map[string]any) {
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: action, Severity: severity, ProjectID: projectID, Principal: p, Attributes: attrs,
		})
	}
}

// StartAnalysisInput is what a caller sends to analyze a project's
// repository.
type StartAnalysisInput struct {
	// Ref is a branch, tag or commit. Empty means the default branch.
	Ref string `json:"ref,omitempty"`
}

// Start queues an analysis of the project's repository.
func (s *Service) Start(ctx context.Context, p security.Principal, projectID string, in StartAnalysisInput) (*Analysis, error) {
	if err := s.authz.Require(ctx, p, security.AnalysisRun, projectID); err != nil {
		return nil, err
	}
	proj, err := s.projects.GetProject(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(proj.RepositoryURL) == "" {
		return nil, ErrNoRepository
	}
	repo, err := repositories.ParseGitHubURL(proj.RepositoryURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	ref := strings.TrimSpace(in.Ref)
	if ref != "" && !repositories.ValidRef(ref) {
		return nil, fmt.Errorf("%w: %q isn't a branch, tag or commit name", ErrInvalid, ref)
	}
	now := s.now().UTC()
	_, err = s.store.ActiveAnalysis(ctx, p.OrgID, projectID, now.Add(-activeWindow))
	switch {
	case err == nil:
		return nil, ErrAlreadyActive
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}

	id, err := typeid.Generate("anl")
	if err != nil {
		return nil, fmt.Errorf("generate analysis id: %w", err)
	}
	a := &Analysis{
		ID: id.String(), OrgID: p.OrgID, ProjectID: projectID, Status: StatusQueued,
		Repository: repo.FullName(), Ref: ref, CreatedBy: p.UserID, CreatedAt: now,
	}
	err = s.store.CreateAnalysis(ctx, a)
	if err != nil {
		return nil, err
	}
	if err = s.enqueue.AnalyzeRepository(ctx, a.OrgID, a.ID); err != nil {
		// Without its job the analysis would sit queued, blocking the next.
		if derr := s.store.DeleteAnalysis(context.WithoutCancel(ctx), a.OrgID, a.ID); derr != nil {
			err = errors.Join(err, fmt.Errorf("remove the analysis again: %w", derr))
		}
		return nil, fmt.Errorf("start analysis: %w", err)
	}
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: "analysis.start", Severity: "info", ProjectID: projectID, Principal: p,
			Attributes: map[string]any{"analysis_id": a.ID, "repository": a.Repository, "ref": a.Ref},
		})
	}
	return a, nil
}

// List returns the project's latest analyses, newest first.
func (s *Service) List(ctx context.Context, p security.Principal, projectID string) ([]*Analysis, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	return s.store.ListAnalyses(ctx, p.OrgID, projectID, listLimit)
}

// Get returns one analysis.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Analysis, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	return s.store.GetAnalysis(ctx, p.OrgID, id)
}

// Graph returns the application graph a succeeded analysis found.
func (s *Service) Graph(ctx context.Context, p security.Principal, id string) (*graph.Graph, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	return s.store.Graph(ctx, p.OrgID, id)
}
