package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// commitLookback is how many of a source's latest analyses are searched
// for one of the commit asked for.
const commitLookback = 50

// CommitAnalyzer analyzes one exact commit of a source, for a promotion
// that needs the graph of a commit an environment runs but nothing has
// analyzed. Unlike Service it checks no role: the plan that asks for it
// already checked its caller.
type CommitAnalyzer struct {
	Store   Store
	Enqueue Enqueuer
	// Audit may be nil.
	Audit projects.Auditor
	// Now is nil for time.Now.
	Now func() time.Time
}

// Analyze returns a succeeded or in-flight analysis of exactly commitSHA,
// a full 40-character commit, or queues one pinned to it. It skips the
// one-active-analysis guard Start keeps: an analysis pinned to a commit
// can't change what a branch analysis finds, and the plan asking has to
// pin something now.
func (c *CommitAnalyzer) Analyze(ctx context.Context, orgID string, src *Source, commitSHA, createdBy string) (*Analysis, error) {
	if src == nil || src.OrgID != orgID {
		return nil, ErrSourceNotFound
	}
	if !repositories.IsCommitSHA(commitSHA) {
		return nil, fmt.Errorf("%w: %q isn't a full commit SHA", ErrInvalid, commitSHA)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	at := now().UTC()
	list, err := c.Store.ListSourceAnalyses(ctx, orgID, src.ID, commitLookback)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		switch {
		case a.Status == StatusSucceeded && a.CommitSHA == commitSHA:
			return a, nil
		// One unfinished for longer than activeWindow is presumed stuck,
		// as Start presumes.
		case !a.Finished() && (a.Ref == commitSHA || a.CommitSHA == commitSHA) && a.CreatedAt.After(at.Add(-activeWindow)):
			return a, nil
		}
	}
	return c.queue(ctx, src, commitSHA, createdBy, at)
}

// queue creates an analysis pinned to commitSHA and its job, as
// Service.queue does. It is marked Pinned, so it never becomes the
// source's latest analysis or blocks the next analysis of its branch.
func (c *CommitAnalyzer) queue(ctx context.Context, src *Source, commitSHA, createdBy string, now time.Time) (*Analysis, error) {
	id, err := typeid.Generate("anl")
	if err != nil {
		return nil, fmt.Errorf("generate analysis id: %w", err)
	}
	a := &Analysis{
		ID: id.String(), OrgID: src.OrgID, ProjectID: src.ProjectID, SourceID: src.ID, Source: src.Alias,
		Status: StatusQueued, Repository: src.Repository, Ref: commitSHA, Pinned: true, CreatedBy: createdBy, CreatedAt: now,
	}
	if err := c.Store.CreateAnalysis(ctx, a); err != nil {
		return nil, err
	}
	if err := c.Enqueue.AnalyzeRepository(ctx, a.OrgID, a.ID); err != nil {
		// Without its job the analysis would sit queued, and the next plan
		// would wait on it.
		if derr := c.Store.DeleteAnalysis(context.WithoutCancel(ctx), a.OrgID, a.ID); derr != nil {
			err = errors.Join(err, fmt.Errorf("remove the analysis again: %w", derr))
		}
		return nil, fmt.Errorf("start analysis: %w", err)
	}
	if c.Audit != nil {
		c.Audit.Record(ctx, projects.AuditEvent{
			Action: "analysis.start", Severity: "info", ProjectID: src.ProjectID,
			Principal: security.Principal{OrgID: src.OrgID, UserID: createdBy},
			Attributes: map[string]any{
				"analysis_id": a.ID, "source": src.Alias, "repository": a.Repository, "ref": a.Ref, "for": "promotion",
			},
		})
	}
	return a, nil
}

// Analysis returns one analysis.
func (c *CommitAnalyzer) Analysis(ctx context.Context, orgID, id string) (*Analysis, error) {
	return c.Store.GetAnalysis(ctx, orgID, id)
}

// AnalyzeCommit is Analyze under the name planning.CommitAnalyzer gives it.
func (c *CommitAnalyzer) AnalyzeCommit(ctx context.Context, orgID string, src *Source, commitSHA, createdBy string) (*Analysis, error) {
	return c.Analyze(ctx, orgID, src, commitSHA, createdBy)
}
