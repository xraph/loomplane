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
	ErrInvalid       = errors.New("invalid analysis")
	ErrAlreadyActive = errors.New("an analysis of that repository is already running")
	ErrNoGraph       = errors.New("this analysis has no graph: it hasn't succeeded")
)

// Analysis is one run of repository discovery over one commit of a
// project's repository. Each successful one is a version of the project's
// application graph.
type Analysis struct {
	ID        string `json:"id" forge:"id"`
	OrgID     string `json:"org_id"`
	ProjectID string `json:"project_id"`
	// SourceID is the source the analysis read, and Source its alias.
	SourceID string `json:"source_id"`
	Source   string `json:"source"`
	Status   string `json:"status"`
	// Repository is "owner/name" on GitHub, as it was when the analysis
	// started.
	Repository string `json:"repository"`
	// Ref is the branch, tag or commit asked for; empty means the default
	// branch.
	Ref string `json:"ref"`
	// CommitSHA is the exact commit analyzed, once it's resolved.
	CommitSHA string `json:"commit_sha"`
	// Pinned marks an analysis of one exact commit that a promotion asked
	// for. It serves that promotion only: it is never a source's latest
	// analysis, so plans, the project graph, and the one-analysis-at-a-time
	// rule for Start and pushes all pass over it.
	Pinned bool `json:"pinned"`
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
	// ListSourceAnalyses returns a source's latest analyses, newest first.
	ListSourceAnalyses(ctx context.Context, orgID, sourceID string, limit int) ([]*Analysis, error)
	// ActiveAnalysis returns the source's analysis that's still running
	// and started after since, or ErrNotFound. Pinned analyses don't count.
	ActiveAnalysis(ctx context.Context, orgID, sourceID string, since time.Time) (*Analysis, error)

	// CreateSource attaches a source. An alias the project has used before,
	// even on a removed source, is ErrAliasTaken.
	CreateSource(ctx context.Context, s *Source) error
	// GetSource returns a project's source by alias, removed or not, or
	// ErrSourceNotFound.
	GetSource(ctx context.Context, orgID, projectID, alias string) (*Source, error)
	// ListSources returns a project's sources by alias: the live ones, or
	// every one ever attached.
	ListSources(ctx context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error)
	// UpdateSource writes a live source's root, ref and updated_at.
	UpdateSource(ctx context.Context, s *Source) error
	// RemoveSource marks a live source removed. Its row stays, so its
	// alias stays taken.
	RemoveSource(ctx context.Context, orgID, projectID, alias string, now time.Time) error

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
// successful analysis of each of its sources.
type ServiceCatalog struct {
	Store Store
}

// Services returns the services of the project's composed graph by ID,
// with their names. A project never analyzed has none.
func (c ServiceCatalog) Services(ctx context.Context, orgID, projectID string) (map[string]string, error) {
	g, _, err := ProjectGraph(ctx, c.Store, orgID, projectID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, s := range g.Services {
		out[s.ID] = s.Name
	}
	return out, nil
}
