package releaseprep

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// The narrow interfaces below are what release preparation reads, each
// satisfied by one of Loomplane's services, so tests use fakes.

// Plans is *planning.Service.
type Plans interface {
	Create(ctx context.Context, p security.Principal, projectID string, in planning.CreatePlanInput) (*planning.Plan, error)
	Get(ctx context.Context, p security.Principal, id string) (*planning.Plan, error)
	List(ctx context.Context, p security.Principal, projectID, environment string, before page.Cursor, limit int) ([]*planning.Plan, page.Cursor, error)
	Deployed(ctx context.Context, p security.Principal, projectID, environment string) ([]*planning.DeployedRepository, error)
	PinsMatchSource(ctx context.Context, orgID string, plan *planning.Plan) (bool, []string, error)
}

// Watches is *releasewatch.Service.
type Watches interface {
	SoakForPlan(ctx context.Context, p security.Principal, planID string) (*releasewatch.Soak, error)
	Armable(ctx context.Context, p security.Principal, projectID, environment string) (*releasewatch.Coverage, error)
	Evidence(ctx context.Context, p security.Principal, projectID, environment string) (*releasewatch.ReleaseEvidence, error)
}

// Canaries is *deployments.Service. LastCanary is the newest canary run
// of an environment that ramped actionID, however many canaries of other
// services ran since; nil when none did.
type Canaries interface {
	LastCanary(ctx context.Context, p security.Principal, projectID, environmentID, actionID string) (*deployments.CanaryRun, error)
}

// Projects is *projects.Service.
type Projects interface {
	Get(ctx context.Context, p security.Principal, id string) (*projects.Project, error)
	Environments(ctx context.Context, p security.Principal, projectID string) ([]*projects.Environment, error)
}

// Budgets is *agentsettings.Service.
type Budgets interface {
	Budget(ctx context.Context, orgID string) (agentsettings.BudgetState, error)
	Enabled(ctx context.Context, orgID string) (bool, error)
}

// Permissions is *security.Authorizer.
type Permissions interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// OpenReport is an agent report still open on the environment a release
// promotes from.
type OpenReport struct {
	ID string `json:"id"`
	// Title is untrusted: 3c trims and cuts it but doesn't scan it. It
	// reaches the model only quoted as untrusted, people only as plain
	// text, and never a gate's detail or a notice.
	Title    string    `json:"title"`
	Severity string    `json:"severity,omitempty"`
	Path     string    `json:"path"` // the report's dashboard path
	OpenedAt time.Time `json:"opened_at"`
}

// OpenReports reads the agent reports open on one environment.
type OpenReports interface {
	Open(ctx context.Context, orgID, projectID, environmentID string) ([]OpenReport, error)
}

// WatcherReports reads 3c's open agent reports.
type WatcherReports struct {
	Reports interface {
		Open(ctx context.Context, orgID, projectID, environmentID string) ([]*agentreports.Report, error)
	} // *agentreports.Service
}

var _ OpenReports = WatcherReports{}

// Open returns the reports open on one environment of a project. It
// refuses an empty project or environment, which would widen the read to
// every one of them, and returns 3c's error as it is, so gate 6 fails as
// unreadable rather than passing.
func (w WatcherReports) Open(ctx context.Context, orgID, projectID, environmentID string) ([]OpenReport, error) {
	if projectID == "" || environmentID == "" {
		return nil, errors.New("open reports are read for one project's environment")
	}
	if w.Reports == nil {
		return nil, errors.New("the agent's reports aren't available on this server")
	}
	list, err := w.Reports.Open(ctx, orgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	out := make([]OpenReport, 0, len(list))
	for _, r := range list {
		out = append(out, OpenReport{ID: r.ID, Title: r.Title, Severity: r.Severity, Path: "/agent/reports/" + r.ID, OpenedAt: r.FirstSeenAt})
	}
	return out, nil
}

// Diffs compares two commits of a source's repository. repository is
// PlanSource.Repository, a stored key.
type Diffs interface {
	Compare(ctx context.Context, orgID, repository, base, head string) (*repositories.Comparison, error)
}

// GitHubDiffs compares through GitHub with the organization's token.
type GitHubDiffs struct {
	GitHub *repositories.GitHub
	Tokens interface {
		Token(ctx context.Context, orgID string, repo repositories.Repository) (string, error)
	} // *repositories.Connections
}

var _ Diffs = GitHubDiffs{}

// Compare reads repository's key and compares base to head on GitHub. A
// repository on any other host is refused before anything is asked, so
// its diff is unreadable evidence and the release asks a person.
func (d GitHubDiffs) Compare(ctx context.Context, orgID, repository, base, head string) (*repositories.Comparison, error) {
	repo, err := repositories.ParseKey(repository)
	if err != nil {
		return nil, err
	}
	if repo.Host != repositories.HostGitHub {
		return nil, fmt.Errorf("comparing commits on %s isn't supported yet: %w", repo.Host, repositories.ErrUnsupportedHost)
	}
	if d.GitHub == nil || d.Tokens == nil {
		return nil, errors.New("comparing commits isn't available on this server")
	}
	token, err := d.Tokens.Token(ctx, orgID, repo)
	if err != nil {
		return nil, err
	}
	return d.GitHub.Compare(ctx, repo, base, head, token)
}

// Sources are everything release preparation reads.
type Sources struct {
	Plans    Plans
	Watches  Watches
	Canaries Canaries
	Projects Projects
	Budgets  Budgets
	Authz    Permissions
	Reports  OpenReports
	Diffs    Diffs
}
