package fabriqloom

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// A rebuild's states.
const (
	RebuildQueued    = "queued"
	RebuildRunning   = "running"
	RebuildSucceeded = "succeeded"
	RebuildFailed    = "failed"
)

// rebuildBadArguments prefixes the output of a rebuild that exited 2:
// Fabriq's rebuild command exits 2 for arguments it doesn't take, which
// only Loomplane's recipe passes.
const rebuildBadArguments = "rebuild exited with code 2: Loomplane's Fabriq recipe passed bad arguments; retrying won't help."

const (
	// rebuildRunTimeout bounds one run, inside the worker's 70 minutes,
	// so it still records how the run ended. The job itself stops after
	// the recipe's hour.
	rebuildRunTimeout = 65 * time.Minute
	// rebuildStuckAfter is how long after its claim a running rebuild is
	// taken for one whose worker stopped.
	rebuildStuckAfter = 75 * time.Minute
	// rebuildStuck is what such a rebuild records.
	rebuildStuck = "The rebuild stopped before Loomplane heard how it ended. Rebuild again."
)

var (
	// ErrRequestConflict is a request id another rebuild already used.
	ErrRequestConflict = errors.New("that request_id belongs to another rebuild")
	// ErrRebuildClaimed is a rebuild that isn't in the state a change
	// expected: claimed already, or finished.
	ErrRebuildClaimed = errors.New("the rebuild was claimed or finished already")
)

var requestKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)

// projection is a Fabriq projection the panel rebuilds from Postgres: the
// job that does it, and the dependency it needs.
type projection struct{ job, title string }

var projections = map[string]projection{
	"graph":  {job: "rebuild-graph", title: "FalkorDB"},
	"search": {job: "rebuild-search", title: "Elasticsearch"},
}

// Rebuild is a request to rebuild one projection of a hosted Fabriq from
// Postgres. Retryable says a new request may help: it's false for a
// rebuild that exited 2, which a retry repeats.
type Rebuild struct {
	ID         string     `json:"id" forge:"id"`
	Projection string     `json:"projection"`
	Status     string     `json:"status"`
	Output     string     `json:"output"`
	CreatedBy  string     `json:"created_by"`
	Retryable  bool       `json:"retryable"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	OrgID         string `json:"-"`
	ProjectID     string `json:"-"`
	EnvironmentID string `json:"-"`
	Resource      string `json:"-"`
	RequestID     string `json:"-"`
	// Attempt names the claim that runs it: RebuildAttempt's, which says
	// when it was made.
	Attempt string `json:"-"`
}

// shown is r as the panel reads it.
func shown(r Rebuild) Rebuild {
	r.Retryable = r.Status == RebuildFailed && !strings.HasPrefix(r.Output, rebuildBadArguments)
	return r
}

// RebuildStore keeps rebuilds. Lists are newest first.
type RebuildStore interface {
	// Create stores r queued. When the organization already has a rebuild
	// with r's RequestID, or the resource has a queued or running rebuild
	// of r's projection, it stores nothing and returns that one. A request
	// id another resource or projection used is ErrRequestConflict.
	Create(ctx context.Context, r *Rebuild) (*Rebuild, error)
	List(ctx context.Context, orgID string, ref deployments.ResourceRef) ([]Rebuild, error)
	// Claim moves a queued rebuild to running as attempt. One that isn't
	// queued is ErrRebuildClaimed.
	Claim(ctx context.Context, orgID, id, attempt string) (*Rebuild, error)
	// Finish records how attempt ended. A rebuild that isn't running as
	// attempt is ErrRebuildClaimed.
	Finish(ctx context.Context, orgID, id, attempt, status, output string, at time.Time) error
	// Unfinished lists queued and running rebuilds across organizations,
	// oldest first.
	Unfinished(ctx context.Context) ([]Rebuild, error)
}

// attemptLayout starts an attempt: when its claim was made.
const attemptLayout = "20060102T150405.000000000Z"

// RebuildAttempt names a claim made at: the time, then randomness, so no
// two claims share a name and the reconciler can tell how old one is.
func RebuildAttempt(at time.Time) string {
	return at.UTC().Format(attemptLayout) + "-" + rand.Text()
}

// claimedAt is when attempt's claim was made.
func claimedAt(attempt string) (time.Time, bool) {
	stamp, _, ok := strings.Cut(attempt, "-")
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(attemptLayout, stamp)
	return at, err == nil
}

// rebuildJobName is the job a claim runs as: a DNS label of its own.
func rebuildJobName(id, attempt string) string {
	sum := sha256.Sum256([]byte("rebuild/" + id + "/" + attempt))
	return "lp-job-" + hex.EncodeToString(sum[:])[:12]
}

// hasJob says whether the composition has projection's job: one whose
// dependency is off isn't planned, so it has no binding.
func hasJob(parts []*planning.Binding, resource string, p projection) bool {
	name := planning.ComponentActionName(resource, p.job)
	for _, b := range parts {
		if b.Name == name {
			return true
		}
	}
	return false
}

// RequestRebuild queues a rebuild of projection ("graph" or "search")
// from Postgres. While one of the resource's rebuilds of it is queued or
// running, it returns that one, and requestID makes a retried request
// return what the first made.
func (s *Service) RequestRebuild(ctx context.Context, p security.Principal, ref deployments.ResourceRef, projectionName, requestID string) (*Rebuild, error) {
	_, parts, err := s.composition(ctx, p, security.DeploymentCreate, ref)
	if err != nil {
		return nil, err
	}
	proj, ok := projections[projectionName]
	if !ok {
		return nil, invalid("Rebuild the graph or the search projection.")
	}
	if !requestKey.MatchString(requestID) {
		return nil, invalid("request_id must be 8-128 letters, digits, underscores or hyphens.")
	}
	if !hasJob(parts, ref.Resource, proj) {
		return nil, invalid("%s is off for this Fabriq", proj.title)
	}
	id, err := typeid.Generate("rebuild")
	if err != nil {
		return nil, err
	}
	r, err := s.RebuildStore.Create(ctx, &Rebuild{ID: id.String(), OrgID: p.OrgID, ProjectID: ref.ProjectID, EnvironmentID: ref.EnvironmentID, Resource: ref.Resource,
		Projection: projectionName, RequestID: requestID, Status: RebuildQueued, CreatedBy: p.UserID, CreatedAt: s.now().UTC()})
	if err != nil {
		return nil, err
	}
	if r.Status == RebuildQueued && s.Enqueue != nil {
		// The reconciler enqueues it again within a minute.
		if eerr := s.Enqueue(ctx, r.OrgID, r.ID); eerr != nil {
			s.warn("enqueue a Fabriq rebuild", eerr)
		}
	}
	out := shown(*r)
	return &out, nil
}

// Rebuilds lists the resource's latest rebuilds.
func (s *Service) Rebuilds(ctx context.Context, p security.Principal, ref deployments.ResourceRef) ([]Rebuild, error) {
	if _, _, err := s.composition(ctx, p, security.DeploymentRead, ref); err != nil {
		return nil, err
	}
	list, err := s.RebuildStore.List(ctx, p.OrgID, ref)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = shown(list[i])
	}
	return list, nil
}

// RunRebuild is the worker's entry point. It claims the rebuild, checks
// again that whoever asked for it still may (as recovery.Service.Run does
// for a backup), runs the projection's job once, and records how it
// ended. A rebuild claimed already is left alone.
func (s *Service) RunRebuild(ctx context.Context, orgID, id string) error {
	attempt := RebuildAttempt(s.now())
	r, err := s.RebuildStore.Claim(ctx, orgID, id, attempt)
	if errors.Is(err, ErrRebuildClaimed) {
		return nil
	}
	if err != nil {
		return err
	}
	run, cancel := context.WithTimeout(ctx, rebuildRunTimeout)
	defer cancel()
	status, output := s.run(run, r)
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	return s.RebuildStore.Finish(cleanup, orgID, id, attempt, status, output, s.now().UTC())
}

// run runs a claimed rebuild's job and says how it ended.
func (s *Service) run(ctx context.Context, r *Rebuild) (status, output string) {
	ref := deployments.ResourceRef{ProjectID: r.ProjectID, EnvironmentID: r.EnvironmentID, Resource: r.Resource}
	requester := security.Principal{OrgID: r.OrgID, UserID: r.CreatedBy}
	// Components checks the requester's permission as it is now.
	_, parts, err := s.composition(ctx, requester, security.DeploymentCreate, ref)
	if err != nil {
		return RebuildFailed, scrub(err.Error())
	}
	proj, ok := projections[r.Projection]
	if !ok {
		return RebuildFailed, fmt.Sprintf("Loomplane doesn't rebuild a %q projection.", r.Projection)
	}
	if !hasJob(parts, ref.Resource, proj) {
		return RebuildFailed, proj.title + " is off for this Fabriq"
	}
	res, err := s.Access.RunJob(ctx, r.OrgID, ref, proj.job, rebuildJobName(r.ID, r.Attempt))
	if err != nil {
		return RebuildFailed, scrub(err.Error())
	}
	// The target hands back the end of the job's log with every secret
	// it gave the job hidden.
	switch res.ExitCode {
	case 0:
		return RebuildSucceeded, res.Output
	case 2:
		return RebuildFailed, strings.TrimSuffix(rebuildBadArguments+"\n"+res.Output, "\n")
	}
	if res.Output == "" {
		return RebuildFailed, fmt.Sprintf("rebuild exited with code %d.", res.ExitCode)
	}
	return RebuildFailed, res.Output
}

// ReconcileRebuilds enqueues queued rebuilds again, which repairs a
// request committed but never enqueued, and records a running one whose
// worker stopped as failed: a rebuild's job runs once, and a rerun after
// it is a new request.
func (s *Service) ReconcileRebuilds(ctx context.Context) error {
	list, err := s.RebuildStore.Unfinished(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range list {
		switch r.Status {
		case RebuildQueued:
			if s.Enqueue != nil {
				errs = append(errs, s.Enqueue(ctx, r.OrgID, r.ID))
			}
		case RebuildRunning:
			if at, ok := claimedAt(r.Attempt); ok && s.now().Sub(at) < rebuildStuckAfter {
				continue
			}
			ferr := s.RebuildStore.Finish(ctx, r.OrgID, r.ID, r.Attempt, RebuildFailed, rebuildStuck, s.now().UTC())
			if !errors.Is(ferr, ErrRebuildClaimed) {
				errs = append(errs, ferr)
			}
		}
	}
	return errors.Join(errs...)
}
