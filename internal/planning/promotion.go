package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// newerManifestCode is the warning a promotion gets when it doesn't use
// the latest manifest revision, or can't tell which one its source
// environment deployed with.
const newerManifestCode = "newer-manifest"

// pinFrom builds a promotion's lockset from what the from environment
// runs, read in one snapshot with the manifest revision it last released.
// Each source it moves is pinned to the commit and analysis from runs,
// with the root and branch of the plan that deployed it there. A source
// whose release from didn't record, but whose services' bindings show one
// commit, is pinned to that commit, with an analysis of it queued if none
// exists. The rest stay at what env runs. scope names the sources that
// move; empty means every one from runs. The manifest is nil when from's
// isn't recorded.
func (s *Service) pinFrom(ctx context.Context, p security.Principal, projectID string, env, from *projects.Environment, scope []string) ([]PlanSource, *DeployedManifest, error) {
	orgID := p.OrgID
	sources, err := s.analyses.ListSources(ctx, orgID, projectID, false)
	if err != nil {
		return nil, nil, err
	}
	if len(sources) == 0 {
		return nil, nil, ErrNoSources
	}
	manifest, fromRows, err := s.store.DeployedState(ctx, orgID, from.ID)
	if err != nil {
		return nil, nil, err
	}
	fromBindings, err := s.store.ListBindings(ctx, orgID, from.ID)
	if err != nil {
		return nil, nil, err
	}
	runs := map[string]*DeployedSource{}
	for _, d := range fromRows {
		runs[d.SourceID] = d
	}
	shown := bindingCommits(fromBindings)
	running := func(src *discovery.Source) bool { return runs[src.ID] != nil || len(shown[src.Alias]) > 0 }
	if !slices.ContainsFunc(sources, running) {
		return nil, nil, ErrNotRunning
	}
	moves := map[string]bool{}
	for _, a := range scope {
		a = strings.TrimSpace(a)
		i := slices.IndexFunc(sources, func(src *discovery.Source) bool { return src.Alias == a })
		if i < 0 {
			return nil, nil, fmt.Errorf("%w: %q isn't one of this project's repositories", ErrInvalid, a)
		}
		if !running(sources[i]) {
			return nil, nil, fmt.Errorf("%w: %s doesn't run %s, so it can't promote it", ErrInvalid, from.Name, a)
		}
		moves[a] = true
	}
	targetRows, err := s.store.DeployedSources(ctx, orgID, env.ID)
	if err != nil {
		return nil, nil, err
	}
	deployed := map[string]*DeployedSource{}
	for _, d := range targetRows {
		deployed[d.SourceID] = d
	}
	plans := map[string]*Plan{} // the from plans read so far, by ID
	out := make([]PlanSource, 0, len(sources))
	// The sources pinned from bindings, by their index in out.
	type unanalyzed struct {
		i   int
		src *discovery.Source
	}
	var queue []unanalyzed
	for _, src := range sources {
		ps := PlanSource{SourceID: src.ID, Alias: src.Alias, Repository: src.Repository, Root: src.Root, Ref: src.Ref}
		row := runs[src.ID]
		// A source from doesn't run is held, like one outside the scope.
		ps.InScope = running(src) && (len(moves) == 0 || moves[src.Alias])
		switch {
		case ps.InScope && row != nil:
			ps.AnalysisID, ps.CommitSHA, ps.FromPlanID = row.AnalysisID, row.CommitSHA, row.PlanID
			if cerr := s.copyFromPlan(ctx, orgID, plans, &ps); cerr != nil {
				return nil, nil, cerr
			}
		case ps.InScope:
			commit, cerr := oneCommit(from.Name, src.Alias, shown[src.Alias])
			if cerr != nil {
				return nil, nil, cerr
			}
			if s.commits == nil {
				return nil, nil, fmt.Errorf("%w: %s runs %s at %s, which nothing has analyzed", ErrNoAnalysis, from.Name, src.Alias, short(commit))
			}
			ps.CommitSHA = commit
			queue = append(queue, unanalyzed{len(out), src})
		default:
			d := deployed[src.ID]
			switch {
			case d == nil && running(src):
				return nil, nil, fmt.Errorf("%w: %s has never been deployed to %s and is outside the scope of this promotion; add it to the scope", ErrNeverDeployed, src.Alias, env.Name)
			case d == nil:
				return nil, nil, fmt.Errorf("%w: %s has never been deployed to %s, and %s doesn't run it either", ErrNeverDeployed, src.Alias, env.Name, from.Name)
			}
			ps.AnalysisID, ps.CommitSHA = d.AnalysisID, d.CommitSHA
		}
		out = append(out, ps)
	}
	// Analyses are queued last, once nothing else can refuse the plan.
	for _, q := range queue {
		ps := &out[q.i]
		a, aerr := s.commits.AnalyzeCommit(ctx, orgID, q.src, ps.CommitSHA, p.UserID)
		if aerr != nil {
			return nil, nil, fmt.Errorf("analyze %s at %s: %w", ps.Alias, short(ps.CommitSHA), aerr)
		}
		ps.AnalysisID = a.ID
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, manifest, nil
}

// bindingCommits is, by repository alias, the commits an environment's
// services run as their bindings show them, each with the services that
// run it.
func bindingCommits(bindings []*Binding) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for _, b := range bindings {
		if b.Subject != ServiceSubject {
			continue
		}
		var cfg ServiceConfig
		if json.Unmarshal(b.Config, &cfg) != nil || cfg.Repo == "" {
			continue
		}
		commit := b.CommitSHA
		if commit == "" {
			commit = cfg.CommitSHA
		}
		if commit == "" {
			continue
		}
		if out[cfg.Repo] == nil {
			out[cfg.Repo] = map[string][]string{}
		}
		out[cfg.Repo][commit] = append(out[cfg.Repo][commit], b.Name)
	}
	return out
}

// oneCommit is the one commit env's services run of alias, as
// bindingCommits found them, which has to be a full SHA to analyze.
func oneCommit(env, alias string, commits map[string][]string) (string, error) {
	if len(commits) > 1 {
		shas := slices.Sorted(maps.Keys(commits))
		named := make([]string, 0, len(shas))
		for _, sha := range shas {
			services := slices.Sorted(slices.Values(commits[sha]))
			named = append(named, fmt.Sprintf("%s (%s)", short(sha), strings.Join(services, ", ")))
		}
		return "", fmt.Errorf("%w: %s runs %s at more than one commit, %s; deploy one commit of it to %s first",
			ErrInvalid, env, alias, strings.Join(named, " and "), env)
	}
	for sha := range commits {
		if !repositories.IsCommitSHA(sha) {
			return "", fmt.Errorf("%w: %s runs %s at %q, which isn't a full commit, so it can't be analyzed", ErrInvalid, env, alias, sha)
		}
		return sha, nil
	}
	return "", fmt.Errorf("%w: %s doesn't run %s", ErrInvalid, env, alias)
}

// copyFromPlan gives ps the root and branch its commit was deployed from,
// as the plan that deployed it recorded them. A plan that's gone, or that
// doesn't list the source, is refused: the source's current root and
// branch may not be what that commit was built from.
func (s *Service) copyFromPlan(ctx context.Context, orgID string, plans map[string]*Plan, ps *PlanSource) error {
	plan, seen := plans[ps.FromPlanID]
	if !seen {
		var err error
		plan, err = s.store.GetPlan(ctx, orgID, ps.FromPlanID)
		if errors.Is(err, ErrNotFound) {
			plan, err = nil, nil
		}
		if err != nil {
			return fmt.Errorf("read the plan that deployed %s: %w", ps.Alias, err)
		}
		plans[ps.FromPlanID] = plan
	}
	if plan == nil {
		return fmt.Errorf("%w: plan %s, which deployed %s at %s, is gone, so its root and branch aren't known", ErrInvalid, ps.FromPlanID, ps.Alias, short(ps.CommitSHA))
	}
	i := slices.IndexFunc(plan.Sources, func(o PlanSource) bool { return o.SourceID == ps.SourceID })
	if i < 0 {
		return fmt.Errorf("%w: plan %s, which deployed %s at %s, doesn't list it, so its root and branch aren't known", ErrInvalid, ps.FromPlanID, ps.Alias, short(ps.CommitSHA))
	}
	ps.Root, ps.Ref = plan.Sources[i].Root, plan.Sources[i].Ref
	return nil
}

// promotionWarnings says when a promotion doesn't use the latest manifest
// revision, or can't tell which one its source environment deployed with.
// revision is the one the plan uses.
func (s *Service) promotionWarnings(ctx context.Context, plan *Plan, revision int) ([]Finding, error) {
	if plan.Kind != KindPromotion || plan.FromEnvironmentID == "" {
		return nil, nil
	}
	manifest, _, err := s.store.DeployedState(ctx, plan.OrgID, plan.FromEnvironmentID)
	if err != nil {
		return nil, err
	}
	latest, err := s.manifests.Latest(ctx, plan.OrgID, plan.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("load the latest manifest: %w", err)
	}
	switch {
	case manifest == nil && revision == latest.Revision:
		return []Finding{{Code: newerManifestCode, Message: fmt.Sprintf(
			"%s's deployed manifest revision isn't recorded; this promotion uses the latest, revision %d", plan.From, revision)}}, nil
	case manifest == nil:
		return []Finding{{Code: newerManifestCode, Message: fmt.Sprintf(
			"%s's deployed manifest revision isn't recorded; this promotion uses revision %d, and revision %d is newer", plan.From, revision, latest.Revision)}}, nil
	case revision < latest.Revision:
		return []Finding{{Code: newerManifestCode, Message: fmt.Sprintf(
			"This promotion uses manifest revision %d, which %s deployed with. Revision %d is newer; deploy it to %s first to promote it.",
			revision, plan.From, latest.Revision, plan.From)}}, nil
	}
	return nil, nil
}

// PinsMatchSource reports whether the From environment still runs every
// in-scope pin of a promotion and the manifest revision it uses, and what
// differs. A plan without From matches.
func (s *Service) PinsMatchSource(ctx context.Context, orgID string, plan *Plan) (matches bool, diffs []string, err error) {
	if plan.FromEnvironmentID == "" {
		return true, nil, nil
	}
	manifest, rows, err := s.store.DeployedState(ctx, orgID, plan.FromEnvironmentID)
	if err != nil {
		return false, nil, err
	}
	bindings, err := s.store.ListBindings(ctx, orgID, plan.FromEnvironmentID)
	if err != nil {
		return false, nil, err
	}
	runs := map[string]*DeployedSource{}
	for _, d := range rows {
		runs[d.SourceID] = d
	}
	shown := bindingCommits(bindings)
	for _, ps := range plan.Sources {
		if !ps.InScope {
			continue
		}
		// A source with no recorded release runs what its bindings show,
		// as pinFrom read it.
		commits := slices.Sorted(maps.Keys(shown[ps.Alias]))
		if d := runs[ps.SourceID]; d != nil {
			commits = []string{d.CommitSHA}
		}
		switch {
		case len(commits) == 0:
			diffs = append(diffs, fmt.Sprintf("%s: %s doesn't run it any more, the plan pins %s", ps.Alias, plan.From, ps.CommitSHA))
		case len(commits) > 1 || commits[0] != ps.CommitSHA:
			diffs = append(diffs, fmt.Sprintf("%s: %s runs %s, the plan pins %s", ps.Alias, plan.From, strings.Join(commits, " and "), ps.CommitSHA))
		}
	}
	// A plan made while From's manifest wasn't recorded used the latest,
	// so there's nothing to compare until From records one.
	if manifest != nil && manifest.ManifestID != plan.ManifestID {
		diffs = append(diffs, fmt.Sprintf("manifest: %s runs revision %d, the plan pins %d", plan.From, manifest.ManifestRevision, plan.ManifestRevision))
	}
	return len(diffs) == 0, diffs, nil
}
