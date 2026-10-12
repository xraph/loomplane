package discovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/telemetry"
)

// Fetcher reads a repository at one commit. *repositories.GitHub is the
// real one.
type Fetcher interface {
	Resolve(ctx context.Context, repo repositories.Repository, ref, token string) (string, error)
	Fetch(ctx context.Context, repo repositories.Repository, sha, token, dest string) (repositories.Stats, error)
}

// TokenSource gives a short-lived token that reads one repository for one
// organization, or "" to read it anonymously. Loomplane's comes from the
// organization's GitHub App installation.
type TokenSource interface {
	Token(ctx context.Context, orgID string, repo repositories.Repository) (string, error)
}

// Runner runs analyses: the worker side of an analysis job. It never runs
// anything from the repository, only reads it.
type Runner struct {
	store   Store
	fetch   Fetcher
	tokens  TokenSource
	workDir string
	// MaxAttempts is how many times one analysis is tried before a
	// transient failure becomes its result.
	maxAttempts int
	now         func() time.Time
	// Telemetry counts analyses. It may be nil.
	Telemetry *telemetry.Recorder
}

// NewRunner returns a Runner. tokens may be nil, for public repositories
// only. workDir is where checkouts go; empty means the system's temporary
// directory. maxAttempts matches the job's retries plus one.
func NewRunner(store Store, fetch Fetcher, tokens TokenSource, workDir string, maxAttempts int) *Runner {
	return &Runner{store: store, fetch: fetch, tokens: tokens, workDir: workDir, maxAttempts: maxAttempts, now: time.Now}
}

// Run is the analysis job. An error means "try again"; anything that
// won't get better by retrying is recorded on the analysis instead.
func (r *Runner) Run(ctx context.Context, orgID, analysisID string) error {
	attempt, ok, err := r.store.BeginAttempt(ctx, orgID, analysisID, r.now().UTC())
	if errors.Is(err, ErrNotFound) {
		return nil // its project was deleted
	}
	if err != nil {
		return err
	}
	if !ok {
		return nil // a duplicate delivery of a finished analysis
	}
	a, err := r.store.GetAnalysis(ctx, orgID, analysisID)
	if err != nil {
		return err
	}
	started := r.now()
	err = r.analyze(ctx, a)
	if err == nil {
		r.Telemetry.AnalysisFinished(StatusSucceeded, r.now().Sub(started))
		return nil
	}
	if permanent(err) || attempt >= r.maxAttempts {
		if ferr := r.store.Fail(context.WithoutCancel(ctx), orgID, analysisID, reason(err), r.now().UTC()); ferr != nil {
			return errors.Join(err, ferr)
		}
		r.Telemetry.AnalysisFinished(StatusFailed, r.now().Sub(started))
		return nil
	}
	return err
}

func (r *Runner) analyze(ctx context.Context, a *Analysis) error {
	owner, name, ok := strings.Cut(a.Repository, "/")
	if !ok {
		return fmt.Errorf("%w: repository %q", repositories.ErrInvalidURL, a.Repository)
	}
	repo := repositories.Repository{Owner: owner, Name: name}
	token := ""
	if r.tokens != nil {
		var err error
		if token, err = r.tokens.Token(ctx, a.OrgID, repo); err != nil {
			return fmt.Errorf("repository access: %w", err)
		}
	}
	sha, err := r.fetch.Resolve(ctx, repo, a.Ref, token)
	if err != nil {
		return err
	}
	err = r.store.Progress(ctx, a.OrgID, a.ID, StatusFetching, sha)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(r.workDir, "loomplane-analysis-")
	if err != nil {
		return fmt.Errorf("checkout directory: %w", err)
	}
	defer os.RemoveAll(dir) // best effort; the OS cleans its temp directory too
	_, err = r.fetch.Fetch(ctx, repo, sha, token, dir)
	if err != nil {
		return err
	}
	err = r.store.Progress(ctx, a.OrgID, a.ID, StatusAnalyzing, sha)
	if err != nil {
		return err
	}
	report, err := Analyze(ctx, os.DirFS(dir))
	if err != nil {
		return err
	}
	return r.store.Succeed(ctx, a.OrgID, a.ID, graph.Build(report), r.now().UTC())
}

// permanent reports whether retrying can't help.
func permanent(err error) bool {
	return errors.Is(err, repositories.ErrNotFound) ||
		errors.Is(err, repositories.ErrTooLarge) ||
		errors.Is(err, repositories.ErrInvalidURL) ||
		errors.Is(err, repositories.ErrUnsupportedHost)
}

// reason is what a person sees for a failed analysis: the cause in
// Loomplane's words when it's one Loomplane knows, the error itself when
// it isn't.
func reason(err error) string {
	for _, known := range []error{
		repositories.ErrNotFound, repositories.ErrTooLarge, repositories.ErrInvalidURL,
		repositories.ErrUnsupportedHost, repositories.ErrRateLimited,
	} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "the analysis failed: " + err.Error()
}
