package agentops

import (
	"context"
	"errors"
	"strings"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// deployment_logs reads the last DefaultLogLines lines unless asked for
// more, and never more than MaxLogLines.
const (
	DefaultLogLines = 200
	MaxLogLines     = 1000
)

// logPage is how many lines one read of the log store returns, its most.
const logPage = 1000

// maxLogPages bounds how far deployment_logs reads to find the end of a
// log, so a log that never stops growing can't hold the call forever.
const maxLogPages = 1000

// errProductionRetry is retry_deployment on a production deployment. The
// tool authorizer (Task 14) refuses it first; this is the second line.
var errProductionRetry = errors.New("retry production deployments from the dashboard or ask a person")

// Outputs.
type (
	deploymentsOutput struct {
		Deployments []*deployments.Deployment `json:"deployments"`
	}
	logsOutput struct {
		DeploymentID string `json:"deployment_id"`
		// Lines is how many lines Log holds: the last ones the deployment
		// wrote.
		Lines int `json:"lines"`
		// Log is those lines, oldest first, as the safety scan quotes them.
		Log string `json:"log"`
	}
)

func registerDeployments(r *Registry, d *Deps) {
	Register(r, def("list_deployments"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.ProjectInput) (deploymentsOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return deploymentsOutput{}, err
		}
		list, _, err := d.Deployments.List(ctx, call.Principal, p.ID, page.Cursor{}, page.Default)
		if err != nil {
			return deploymentsOutput{}, err
		}
		out := deploymentsOutput{Deployments: make([]*deployments.Deployment, 0, len(list))}
		for _, dep := range list {
			if call.Scope.EnvironmentID != "" && dep.EnvironmentID != call.Scope.EnvironmentID {
				continue
			}
			out.Deployments = append(out.Deployments, d.quoteDeployment(ctx, dep))
		}
		return out, nil
	})

	Register(r, def("get_deployment"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.DeploymentInput) (*deployments.Deployment, error) {
		dep, err := d.deployment(ctx, call, in.DeploymentID)
		if err != nil {
			return nil, err
		}
		return d.quoteDeployment(ctx, dep), nil
	})

	Register(r, def("deployment_for_plan"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.PlanInput) (*deployments.Deployment, error) {
		if _, err := d.plan(ctx, call, in.PlanID); err != nil {
			return nil, err
		}
		dep, err := d.Deployments.ForPlan(ctx, call.Principal, in.PlanID)
		if err != nil {
			return nil, err
		}
		return d.quoteDeployment(ctx, dep), nil
	})

	Register(r, def("retry_deployment"), security.DeploymentCreate, func(ctx context.Context, call Call, in defs.DeploymentInput) (*deployments.Deployment, error) {
		dep, err := d.deployment(ctx, call, in.DeploymentID)
		if err != nil {
			return nil, err
		}
		kind, err := d.environmentKind(ctx, call, dep.ProjectID, dep.EnvironmentID)
		if err != nil {
			return nil, err
		}
		if kind == projects.KindProduction {
			return nil, errProductionRetry
		}
		dep, err = d.Deployments.Retry(ctx, call.Principal, in.DeploymentID)
		if err != nil {
			return nil, err
		}
		return d.quoteDeployment(ctx, dep), nil
	}, RiskFrom(func(ctx context.Context, call Call, in defs.DeploymentInput) (defs.Risk, error) {
		dep, err := d.deployment(ctx, call, in.DeploymentID)
		if err != nil {
			return "", err
		}
		kind, err := d.environmentKind(ctx, call, dep.ProjectID, dep.EnvironmentID)
		if err != nil {
			return "", err
		}
		if kind == projects.KindProduction {
			return defs.RiskPromoteProduction, nil
		}
		return defs.RiskDeployNonProduction, nil
	}))

	Register(r, def("deployment_logs"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.DeploymentLogsInput) (logsOutput, error) {
		// The log store reads without a principal: the deployments service
		// decides whether the agent and the person may read this one.
		dep, err := d.deployment(ctx, call, in.DeploymentID)
		if err != nil {
			return logsOutput{}, err
		}
		want := in.Lines
		if want <= 0 {
			want = DefaultLogLines
		}
		want = min(want, MaxLogLines)
		lines, err := d.lastLines(ctx, call.Principal.OrgID, dep.ID, want)
		if err != nil {
			return logsOutput{}, err
		}
		// One scan over the whole tail, which is how shield reads a log.
		return logsOutput{
			DeploymentID: dep.ID, Lines: len(lines),
			Log: d.quote(ctx, agentsafety.SourceLogLine, strings.Join(lines, "\n")),
		}, nil
	})
}

// deployment reads a deployment through the deployments service and checks
// it's in the run's scope.
func (d *Deps) deployment(ctx context.Context, call Call, id string) (*deployments.Deployment, error) {
	dep, err := d.Deployments.Get(ctx, call.Principal, id)
	if err != nil {
		return nil, err
	}
	if scopeErr := call.inScope(ctx, dep.ProjectID, dep.EnvironmentID); scopeErr != nil {
		return nil, scopeErr
	}
	return dep, nil
}

// lastLines pages through a deployment's log and keeps its last n lines,
// oldest first.
func (d *Deps) lastLines(ctx context.Context, orgID, deploymentID string, n int) ([]string, error) {
	ring := make([]string, n)
	seen := 0
	after := ""
	for range maxLogPages {
		pg, err := d.Logs.Logs(ctx, orgID, deploymentID, after, logPage)
		if err != nil {
			return nil, err
		}
		for _, l := range pg.Logs {
			ring[seen%n] = logLine(l)
			seen++
		}
		if pg.Next == "" {
			break
		}
		after = pg.Next
	}
	if seen <= n {
		return ring[:seen], nil
	}
	start := seen % n
	return append(ring[start:], ring[:start]...), nil
}

// logLine is one log line as the model reads it.
func logLine(l realtime.Log) string {
	var b strings.Builder
	b.WriteString(l.Time.UTC().Format("15:04:05"))
	if l.Stream != "" {
		b.WriteString(" [" + l.Stream + "]")
	}
	b.WriteString(" ")
	b.WriteString(l.Message)
	return b.String()
}
