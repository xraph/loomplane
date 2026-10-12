package discovery

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/graph"
)

// Analysis statuses, in the order an analysis moves through them.
const (
	StatusQueued    = "queued"
	StatusFetching  = "fetching"
	StatusAnalyzing = "analyzing"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Errors the service and stores return.
var (
	ErrNotFound      = errors.New("analysis not found")
	ErrNoRepository  = errors.New("this project has no repository URL; add one first")
	ErrInvalid       = errors.New("invalid analysis")
	ErrAlreadyActive = errors.New("an analysis of this project is already running")
	ErrNoGraph       = errors.New("this analysis has no graph: it hasn't succeeded")
)

// Analysis is one run of repository discovery over one commit of a
// project's repository. Each successful one is a version of the project's
// application graph.
type Analysis struct {
	ID        string `json:"id" forge:"id"`
	OrgID     string `json:"org_id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	// Repository is "owner/name" on GitHub, as it was when the analysis
	// started.
	Repository string `json:"repository"`
	// Ref is the branch, tag or commit asked for; empty means the default
	// branch.
	Ref string `json:"ref"`
	// CommitSHA is the exact commit analyzed, once it's resolved.
	CommitSHA string `json:"commit_sha"`
	// Error says why a failed analysis failed, for people.
	Error      string        `json:"error"`
	Summary    graph.Summary `json:"summary"`
	CreatedBy  string        `json:"created_by"`
	CreatedAt  time.Time     `json:"created_at"`
	StartedAt  *time.Time    `json:"started_at"`
	FinishedAt *time.Time    `json:"finished_at"`
}

// Finished reports whether the analysis has stopped, either way.
func (a *Analysis) Finished() bool {
	return a.Status == StatusSucceeded || a.Status == StatusFailed
}

func (a *Analysis) normalize() {
	a.CreatedAt = a.CreatedAt.UTC()
	if a.StartedAt != nil {
		t := a.StartedAt.UTC()
		a.StartedAt = &t
	}
	if a.FinishedAt != nil {
		t := a.FinishedAt.UTC()
		a.FinishedAt = &t
	}
}

// Store persists analyses. Every method is scoped to an organization: an
// analysis in another organization reads as not found.
type Store interface {
	CreateAnalysis(ctx context.Context, a *Analysis) error
	DeleteAnalysis(ctx context.Context, orgID, id string) error
	GetAnalysis(ctx context.Context, orgID, id string) (*Analysis, error)
	// ListAnalyses returns a project's latest analyses, newest first.
	ListAnalyses(ctx context.Context, orgID, projectID string, limit int) ([]*Analysis, error)
	// ActiveAnalysis returns the project's analysis that's still running
	// and started after since, or ErrNotFound.
	ActiveAnalysis(ctx context.Context, orgID, projectID string, since time.Time) (*Analysis, error)

	// BeginAttempt moves an unfinished analysis to fetching and counts the
	// attempt. It returns false, with no error, when the analysis has
	// already finished, and ErrNotFound when it's gone.
	BeginAttempt(ctx context.Context, orgID, id string, now time.Time) (attempt int, ok bool, err error)
	// Progress records the commit and the status of a running analysis.
	Progress(ctx context.Context, orgID, id, status, commitSHA string) error
	// Succeed stores the graph and finishes the analysis.
	Succeed(ctx context.Context, orgID, id string, g *graph.Graph, now time.Time) error
	// Fail finishes the analysis with a reason.
	Fail(ctx context.Context, orgID, id, reason string, now time.Time) error
	// Graph returns a succeeded analysis's graph, or ErrNoGraph.
	Graph(ctx context.Context, orgID, id string) (*graph.Graph, error)
}

// ServiceCatalog answers which services a project has, from its latest
// successful analysis.
type ServiceCatalog struct {
	Store Store
}

// Services returns the latest analysis's services by ID, with their
// names. A project never analyzed has none.
func (c ServiceCatalog) Services(ctx context.Context, orgID, projectID string) (map[string]string, error) {
	list, err := c.Store.ListAnalyses(ctx, orgID, projectID, listLimit)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range list {
		if a.Status != StatusSucceeded {
			continue
		}
		g, err := c.Store.Graph(ctx, orgID, a.ID)
		if err != nil {
			return nil, err
		}
		for _, s := range g.Services {
			out[s.ID] = s.Name
		}
		return out, nil
	}
	return out, nil
}
