package agentops

import (
	"context"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// How analyze waits: it reads each analysis every analysisPoll until it
// finishes, for at most analysisWait.
var (
	analysisPoll = 2 * time.Second
	analysisWait = 10 * time.Minute
)

// Outputs.
type (
	// githubStatusOutput is the GitHub connection without the link that
	// installs the App: connecting an account is a person's job
	// (request_github_connection).
	githubStatusOutput struct {
		Configured    bool                              `json:"configured"`
		Installations []repositories.GitHubInstallation `json:"installations"`
	}
	repositoriesOutput struct {
		Repositories []repositories.ConnectedRepository `json:"repositories"`
	}
	analysesOutput struct {
		Analyses []discovery.Analysis `json:"analyses"`
	}
)

func registerRepositories(r *Registry, d *Deps) {
	Register(r, def("github_status"), security.ProjectRead, func(ctx context.Context, call Call, _ defs.NoInput) (githubStatusOutput, error) {
		st, err := d.GitHub.Status(ctx, call.Principal)
		if err != nil {
			return githubStatusOutput{}, err
		}
		out := githubStatusOutput{Configured: st.Configured, Installations: st.Installations}
		if out.Installations == nil {
			out.Installations = []repositories.GitHubInstallation{}
		}
		return out, nil
	})

	Register(r, def("list_repositories"), security.ProjectRead, func(ctx context.Context, call Call, _ defs.NoInput) (repositoriesOutput, error) {
		list, err := d.GitHub.Repositories(ctx, call.Principal)
		if err != nil {
			return repositoriesOutput{}, err
		}
		if list == nil {
			list = []repositories.ConnectedRepository{}
		}
		return repositoriesOutput{Repositories: list}, nil
	})

	Register(r, def("add_source"), security.ProjectUpdate, func(ctx context.Context, call Call, in defs.AddSourceInput) (*discovery.Source, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return nil, err
		}
		return d.Discovery.AddSource(ctx, call.Principal, p.ID, discovery.AddSourceInput{Repository: in.Repository, Alias: in.Alias, Ref: in.Branch})
	})

	Register(r, def("analyze"), security.AnalysisRun, func(ctx context.Context, call Call, in defs.AnalyzeInput) (analysesOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return analysesOutput{}, err
		}
		queued, err := d.Discovery.Start(ctx, call.Principal, p.ID, discovery.StartAnalysisInput{Sources: in.Sources, Ref: in.Ref})
		if err != nil {
			return analysesOutput{}, err
		}
		out := analysesOutput{Analyses: make([]discovery.Analysis, 0, len(queued))}
		for _, a := range queued {
			done, werr := d.waitAnalysis(ctx, call, a.ID)
			if werr != nil {
				return analysesOutput{}, werr
			}
			out.Analyses = append(out.Analyses, d.quoteAnalysis(ctx, done))
		}
		return out, nil
	})
}

// waitAnalysis reads the analysis until it finishes, at most analysisWait.
func (d *Deps) waitAnalysis(ctx context.Context, call Call, id string) (*discovery.Analysis, error) {
	ctx, cancel := context.WithTimeout(ctx, analysisWait)
	defer cancel()
	tick := time.NewTicker(analysisPoll)
	defer tick.Stop()
	for {
		a, err := d.Discovery.Get(ctx, call.Principal, id)
		if err != nil {
			return nil, err
		}
		if a.Finished() {
			return a, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the analysis of %s hadn't finished after %s; read it again later: %w", a.Source, analysisWait, ctx.Err())
		case <-tick.C:
		}
	}
}
