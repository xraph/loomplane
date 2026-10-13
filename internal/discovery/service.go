package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
// repositories.
type StartAnalysisInput struct {
	// Sources are the aliases to analyze. Empty means every source.
	Sources []string `json:"sources,omitempty"`
	// Ref is a branch, tag or commit, for one source only. Empty means the
	// source's tracked branch.
	Ref string `json:"ref,omitempty"`
}

// Start queues one analysis per source asked for. If any of them is still
// being analyzed, nothing is queued: half a request is harder to reason
// about than none.
func (s *Service) Start(ctx context.Context, p security.Principal, projectID string, in StartAnalysisInput) ([]*Analysis, error) {
	if err := s.authz.Require(ctx, p, security.AnalysisRun, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	live, err := s.store.ListSources(ctx, p.OrgID, projectID, false)
	if err != nil {
		return nil, err
	}
	if len(live) == 0 {
		return nil, ErrNoSources
	}
	picked, err := pick(live, in.Sources)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(in.Ref)
	if ref != "" {
		if len(picked) != 1 {
			return nil, fmt.Errorf("%w: a ref is a branch of one repository, so name one source with it", ErrInvalid)
		}
		if !repositories.ValidRef(ref) {
			return nil, fmt.Errorf("%w: %q isn't a branch, tag or commit name", ErrInvalid, ref)
		}
	}
	now := s.now().UTC()
	for _, src := range picked {
		_, err := s.store.ActiveAnalysis(ctx, p.OrgID, src.ID, now.Add(-activeWindow))
		switch {
		case err == nil:
			return nil, fmt.Errorf("%w: %s", ErrAlreadyActive, src.Alias)
		case !errors.Is(err, ErrNotFound):
			return nil, err
		}
	}
	out := make([]*Analysis, 0, len(picked))
	for _, src := range picked {
		target := ref
		if target == "" {
			target = src.Ref
		}
		a, err := s.queue(ctx, p, src, target, now)
		if err != nil {
			// The earlier sources are already queued; take them back, or a
			// retry would find them "already running".
			for _, done := range out {
				if derr := s.store.DeleteAnalysis(context.WithoutCancel(ctx), done.OrgID, done.ID); derr != nil {
					err = errors.Join(err, fmt.Errorf("remove the %s analysis again: %w", done.Source, derr))
				}
			}
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// PushActor is who an analysis a push started is recorded as created by.
const PushActor = "github-push"

// StartFromPush queues an analysis of a source's tracked branch because
// GitHub said it moved. It skips the permission check, since nobody asked:
// the webhook's signature is the authority. A source with an analysis
// already under way is left alone, and that's ErrAlreadyActive.
func (s *Service) StartFromPush(ctx context.Context, src *Source) (*Analysis, error) {
	now := s.now().UTC()
	switch _, err := s.store.ActiveAnalysis(ctx, src.OrgID, src.ID, now.Add(-activeWindow)); {
	case err == nil:
		return nil, fmt.Errorf("%w: %s", ErrAlreadyActive, src.Alias)
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}
	return s.queue(ctx, security.Principal{OrgID: src.OrgID, UserID: PushActor}, src, src.Ref, now)
}

// pick finds the sources aliases name, in alias order. No aliases means all
// of them.
func pick(live []*Source, aliases []string) ([]*Source, error) {
	if len(aliases) == 0 {
		return live, nil
	}
	by := map[string]*Source{}
	for _, src := range live {
		by[src.Alias] = src
	}
	seen := map[string]bool{}
	var out []*Source
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		src := by[a]
		if src == nil {
			return nil, fmt.Errorf("%w: this project has no repository called %q", ErrSourceNotFound, a)
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, src)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

// queue creates one analysis and its job.
func (s *Service) queue(ctx context.Context, p security.Principal, src *Source, ref string, now time.Time) (*Analysis, error) {
	id, err := typeid.Generate("anl")
	if err != nil {
		return nil, fmt.Errorf("generate analysis id: %w", err)
	}
	a := &Analysis{
		ID: id.String(), OrgID: p.OrgID, ProjectID: src.ProjectID, SourceID: src.ID, Source: src.Alias,
		Status: StatusQueued, Repository: src.Repository, Ref: ref, CreatedBy: p.UserID, CreatedAt: now,
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
	s.record(ctx, p, src.ProjectID, "analysis.start", "info",
		map[string]any{"analysis_id": a.ID, "source": src.Alias, "repository": a.Repository, "ref": a.Ref})
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
	list, err := s.store.ListAnalyses(ctx, p.OrgID, projectID, listLimit)
	if err != nil {
		return nil, err
	}
	return s.analysisSummaries(ctx, p.OrgID, projectID, list)
}

// Get returns one analysis.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Analysis, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	a, err := s.store.GetAnalysis(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	list, err := s.analysisSummaries(ctx, p.OrgID, a.ProjectID, []*Analysis{a})
	if err != nil {
		return nil, err
	}
	return list[0], nil
}

// analysisSummaries projects successful runs through their current source
// roots. Stored analyses and their full repository graphs stay unchanged.
func (s *Service) analysisSummaries(ctx context.Context, orgID, projectID string, list []*Analysis) ([]*Analysis, error) {
	var sources map[string]*Source
	out := make([]*Analysis, 0, len(list))
	for _, a := range list {
		if a.Status != StatusSucceeded {
			out = append(out, analysisView(a, nil, nil))
			continue
		}
		if sources == nil {
			all, err := s.store.ListSources(ctx, orgID, projectID, true)
			if err != nil {
				return nil, err
			}
			sources = make(map[string]*Source, len(all))
			for _, src := range all {
				sources[src.ID] = src
			}
		}
		g, err := s.store.Graph(ctx, orgID, a.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, analysisView(a, sources[a.SourceID], g))
	}
	return out, nil
}

// Graph returns the application graph a succeeded analysis found.
func (s *Service) Graph(ctx context.Context, p security.Principal, id string) (*graph.Graph, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	return s.store.Graph(ctx, p.OrgID, id)
}
