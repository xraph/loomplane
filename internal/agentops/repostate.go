package agentops

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// RepositoryReader reads where a repository is now from its Git host:
// repositories.Readers.
type RepositoryReader interface {
	HeadCommit(ctx context.Context, repo repositories.Repository, ref, token string) (repositories.Commit, error)
	LatestRelease(ctx context.Context, repo repositories.Repository, token string) (*repositories.Release, error)
	RepositoryURL(ctx context.Context, repo repositories.Repository) string
}

// stateReads is how many repositories get_repository_state reads from
// their hosts at once.
const stateReads = 4

// shortSHA is how much of a commit SHA people read.
const shortSHA = 7

// Outputs.
type (
	repositoryStateOutput struct {
		Project linkedProject `json:"project"`
		// Environments are the environments whose deployed commits were
		// compared. One missing from a repository's deployed list deploys
		// nothing from it yet.
		Environments []string          `json:"environments,omitempty"`
		Repositories []repositoryState `json:"repositories"`
		// DeploymentsHidden is true when the person the agent acts for may
		// not read deployments, so no repository lists what's deployed.
		DeploymentsHidden bool `json:"deployments_hidden,omitempty"`
	}
	linkedProject struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Path string `json:"path"`
	}
	repositoryState struct {
		Alias string `json:"alias"`
		// Provider is github, gitlab or bitbucket.
		Provider   string `json:"provider"`
		Repository string `json:"repository"`
		Root       string `json:"root,omitempty"`
		URL        string `json:"url"`
		// Branch is the branch the source tracks, else the repository's
		// default branch once the host has named it.
		Branch        string           `json:"branch"`
		HeadCommit    *stateCommit     `json:"head_commit"`
		LatestRelease *stateRelease    `json:"latest_release"`
		Deployed      []deployedCommit `json:"deployed,omitempty"`
		// Error says why the host couldn't be read. The rest of the call
		// still answers.
		Error string `json:"error,omitempty"`
	}
	stateCommit struct {
		SHA      string     `json:"sha"`
		ShortSHA string     `json:"short_sha"`
		Message  string     `json:"message"`
		Author   string     `json:"author,omitempty"`
		Date     *time.Time `json:"date,omitempty"`
		URL      string     `json:"url"`
	}
	stateRelease struct {
		Name        string     `json:"name"`
		Tag         string     `json:"tag"`
		URL         string     `json:"url"`
		PublishedAt *time.Time `json:"published_at,omitempty"`
		Kind        string     `json:"kind"`
	}
	deployedCommit struct {
		Environment string `json:"environment"`
		CommitSHA   string `json:"commit_sha"`
		ShortSHA    string `json:"short_sha"`
		// BehindHead is true when the environment runs another commit than
		// the branch's head, and absent when the head couldn't be read.
		BehindHead *bool     `json:"behind_head,omitempty"`
		DeployedAt time.Time `json:"deployed_at"`
		PlanID     string    `json:"plan_id"`
		Path       string    `json:"path"`
	}
)

func registerRepositoryState(r *Registry, d *Deps) {
	Register(r, def("get_repository_state"), security.ProjectRead, func(ctx context.Context, call Call, in defs.RepositoryStateInput) (repositoryStateOutput, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return repositoryStateOutput{}, err
		}
		var envs []*projects.Environment
		if strings.TrimSpace(in.Environment) != "" {
			env, eerr := d.environment(ctx, call, p.ID, in.Environment)
			if eerr != nil {
				return repositoryStateOutput{}, eerr
			}
			envs = []*projects.Environment{env}
		} else if envs, err = d.environments(ctx, call, p.ID); err != nil {
			return repositoryStateOutput{}, err
		}
		sources, err := d.Discovery.Sources(ctx, call.Principal, p.ID)
		if err != nil {
			return repositoryStateOutput{}, err
		}
		out := repositoryStateOutput{
			Project:      linkedProject{ID: p.ID, Name: p.Name, Path: projectPath(p.ID)},
			Repositories: d.readRepositories(ctx, call.Principal.OrgID, sources),
		}
		deployed, hidden, err := d.deployedBySource(ctx, call, p.ID, envs)
		if err != nil {
			return repositoryStateOutput{}, err
		}
		out.DeploymentsHidden = hidden
		if !hidden {
			out.Environments = make([]string, 0, len(envs))
			for _, e := range envs {
				out.Environments = append(out.Environments, e.Name)
			}
		}
		for i, src := range sources {
			st := &out.Repositories[i]
			for _, dep := range deployed[src.ID] {
				dc := deployedCommit{
					Environment: dep.environment, CommitSHA: dep.CommitSHA, ShortSHA: short(dep.CommitSHA),
					DeployedAt: dep.DeployedAt, PlanID: dep.PlanID, Path: deploymentPath(dep.PlanID),
				}
				if st.HeadCommit != nil {
					behind := !strings.EqualFold(dep.CommitSHA, st.HeadCommit.SHA)
					dc.BehindHead = &behind
				}
				st.Deployed = append(st.Deployed, dc)
			}
		}
		return out, nil
	})
}

// projectPath is a project's page in the dashboard, relative to the
// workspace.
func projectPath(id string) string { return "/projects/" + url.PathEscape(id) }

// deploymentPath is the dashboard's page of a deployment, which it finds by
// the plan that started it.
func deploymentPath(planID string) string {
	if planID == "" {
		return ""
	}
	return "/deployments/" + url.PathEscape(planID)
}

func short(sha string) string {
	if len(sha) > shortSHA {
		return sha[:shortSHA]
	}
	return sha
}

// deployedSource is a source's commit in one environment.
type deployedSource struct {
	planning.DeployedRepository
	environment string
}

// deployedBySource reads what each environment runs, by source ID. A
// person who may not read deployments is no error: hidden is true and
// nothing is listed.
func (d *Deps) deployedBySource(ctx context.Context, call Call, projectID string, envs []*projects.Environment) (bySource map[string][]deployedSource, hidden bool, err error) {
	bySource = map[string][]deployedSource{}
	for _, e := range envs {
		var list []*planning.DeployedRepository
		list, err = d.Plans.Deployed(ctx, call.Principal, projectID, e.Name)
		if errors.Is(err, security.ErrForbidden) {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, err
		}
		for _, r := range list {
			bySource[r.SourceID] = append(bySource[r.SourceID], deployedSource{DeployedRepository: *r, environment: e.Name})
		}
	}
	return bySource, false, nil
}

// readRepositories reads each source's repository from its host, a few at
// a time. Sources on one repository share its token and its latest release;
// sources on one branch share its head too. The result is in the order of
// sources.
func (d *Deps) readRepositories(ctx context.Context, orgID string, sources []*discovery.Source) []repositoryState {
	out := make([]repositoryState, len(sources))
	byRepo := map[string][]int{}
	var order []string
	for i, src := range sources {
		out[i] = repositoryState{Alias: src.Alias, Repository: src.Repository, Root: src.Root, Branch: src.Ref}
		k := strings.ToLower(src.Repository)
		if _, ok := byRepo[k]; !ok {
			order = append(order, k)
		}
		byRepo[k] = append(byRepo[k], i)
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, stateReads)
	for _, k := range order {
		idx := byRepo[k]
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			d.readRepository(ctx, orgID, sources, idx, out)
		}()
	}
	wg.Wait()
	return out
}

// readRepository fills out[i] for each source i in idx, which are all on
// one repository. Each goroutine writes only its own indexes.
func (d *Deps) readRepository(ctx context.Context, orgID string, sources []*discovery.Source, idx []int, out []repositoryState) {
	fail := func(msg string) {
		for _, i := range idx {
			out[i].Error = msg
		}
	}
	repo, err := repositories.ParseKey(sources[idx[0]].Repository)
	if err != nil {
		fail("Loomplane can't read this source's repository name")
		return
	}
	for _, i := range idx {
		out[i].Provider = repo.Provider()
		out[i].URL = repo.URL()
	}
	if d.Repositories == nil {
		fail("this server can't read Git hosts")
		return
	}
	webURL := d.Repositories.RepositoryURL(ctx, repo)
	for _, i := range idx {
		out[i].URL = webURL
	}
	token := ""
	if d.RepositoryTokens != nil {
		if token, err = d.RepositoryTokens.Token(ctx, orgID, repo); err != nil {
			fail(d.hostError(ctx, err))
			return
		}
	}
	var release *stateRelease
	rel, relErr := d.Repositories.LatestRelease(ctx, repo, token)
	if relErr == nil && rel != nil {
		release = d.quoteRelease(ctx, rel)
	}
	heads := map[string]*stateCommit{}
	headErrs := map[string]error{}
	branches := map[string]string{}
	for _, i := range idx {
		ref := sources[i].Ref
		if _, done := heads[ref]; !done && headErrs[ref] == nil {
			c, herr := d.Repositories.HeadCommit(ctx, repo, ref, token)
			if herr != nil {
				headErrs[ref] = herr
			} else {
				heads[ref] = d.quoteCommit(ctx, c)
				branches[ref] = c.Ref
			}
		}
		st := &out[i]
		st.HeadCommit = heads[ref]
		st.LatestRelease = release
		if st.Branch == "" {
			st.Branch = branches[ref]
		}
		switch {
		case headErrs[ref] != nil:
			st.Error = d.hostError(ctx, headErrs[ref])
		case relErr != nil:
			st.Error = "the latest release couldn't be read: " + d.hostError(ctx, relErr)
		}
	}
}

// hostError is why a Git host couldn't be read, in words the model may
// repeat. Loomplane's own errors are said as they are; anything else can
// carry the host's words, so it's quoted.
func (d *Deps) hostError(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, repositories.ErrNotFound):
		return "the repository or branch doesn't exist, or it's private and no connected account can read it"
	case errors.Is(err, repositories.ErrRateLimited):
		return "the Git host's rate limit was reached; try again later"
	case errors.Is(err, repositories.ErrUnsupportedHost):
		return "Loomplane can't read commits and releases from this repository's host"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "the Git host didn't answer in time"
	}
	return d.quote(ctx, agentsafety.SourceErrorOutput, err.Error())
}

// quoteCommit is a head commit with its message and author quoted: whoever
// commits writes both.
func (d *Deps) quoteCommit(ctx context.Context, c repositories.Commit) *stateCommit {
	out := &stateCommit{
		SHA: c.SHA, ShortSHA: short(c.SHA), URL: c.URL,
		Message: d.quote(ctx, agentsafety.SourceCommitMessage, c.Message),
		Author:  d.quote(ctx, agentsafety.SourceCommitMessage, c.Author),
	}
	if !c.Date.IsZero() {
		date := c.Date
		out.Date = &date
	}
	return out
}

// quoteRelease is a release with its name and tag quoted: the repository's
// people name both.
func (d *Deps) quoteRelease(ctx context.Context, r *repositories.Release) *stateRelease {
	out := &stateRelease{
		Name: d.quote(ctx, agentsafety.SourceToolResult, r.Name),
		Tag:  d.quote(ctx, agentsafety.SourceToolResult, r.Tag),
		URL:  r.URL, Kind: r.Kind,
	}
	if !r.PublishedAt.IsZero() {
		at := r.PublishedAt
		out.PublishedAt = &at
	}
	return out
}
