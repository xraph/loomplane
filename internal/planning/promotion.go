package planning

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/projects"
)

// newerManifestCode is the warning a promotion gets when it doesn't use
// the latest manifest revision, or can't tell which one its source
// environment deployed with.
const newerManifestCode = "newer-manifest"

// pinFrom builds a promotion's lockset from what the from environment
// runs, read in one snapshot with the manifest revision it last released.
// Each source it moves is pinned to the commit and analysis from runs,
// with the root and branch of the plan that deployed it there. The rest
// stay at what env runs. scope names the sources that move; empty means
// every one from runs. The manifest is nil when from's isn't recorded.
func (s *Service) pinFrom(ctx context.Context, orgID, projectID string, env, from *projects.Environment, scope []string) ([]PlanSource, *DeployedManifest, error) {
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
	runs := map[string]*DeployedSource{}
	for _, d := range fromRows {
		runs[d.SourceID] = d
	}
	if !slices.ContainsFunc(sources, func(src *discovery.Source) bool { return runs[src.ID] != nil }) {
		return nil, nil, ErrNotRunning
	}
	moves := map[string]bool{}
	for _, a := range scope {
		a = strings.TrimSpace(a)
		i := slices.IndexFunc(sources, func(src *discovery.Source) bool { return src.Alias == a })
		if i < 0 {
			return nil, nil, fmt.Errorf("%w: %q isn't one of this project's repositories", ErrInvalid, a)
		}
		if runs[sources[i].ID] == nil {
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
	for _, src := range sources {
		ps := PlanSource{SourceID: src.ID, Alias: src.Alias, Repository: src.Repository, Root: src.Root, Ref: src.Ref}
		row := runs[src.ID]
		// A source from doesn't run is held, like one outside the scope.
		ps.InScope = row != nil && (len(moves) == 0 || moves[src.Alias])
		if ps.InScope {
			ps.AnalysisID, ps.CommitSHA, ps.FromPlanID = row.AnalysisID, row.CommitSHA, row.PlanID
			if cerr := s.copyFromPlan(ctx, orgID, plans, &ps); cerr != nil {
				return nil, nil, cerr
			}
		} else {
			d := deployed[src.ID]
			if d == nil {
				return nil, nil, fmt.Errorf("%w: %s has never been deployed to %s, and %s doesn't run it either", ErrNeverDeployed, src.Alias, env.Name, from.Name)
			}
			ps.AnalysisID, ps.CommitSHA = d.AnalysisID, d.CommitSHA
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, manifest, nil
}

// copyFromPlan gives ps the root and branch its commit was deployed from,
// as the plan that deployed it recorded them. A plan that's gone, or that
// doesn't list the source, leaves the source's own.
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
		return nil
	}
	if i := slices.IndexFunc(plan.Sources, func(o PlanSource) bool { return o.SourceID == ps.SourceID }); i >= 0 {
		ps.Root, ps.Ref = plan.Sources[i].Root, plan.Sources[i].Ref
	}
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
	runs := map[string]*DeployedSource{}
	for _, d := range rows {
		runs[d.SourceID] = d
	}
	for _, ps := range plan.Sources {
		if !ps.InScope {
			continue
		}
		switch d := runs[ps.SourceID]; {
		case d == nil:
			diffs = append(diffs, fmt.Sprintf("%s: %s doesn't run it any more, the plan pins %s", ps.Alias, plan.From, ps.CommitSHA))
		case d.CommitSHA != ps.CommitSHA:
			diffs = append(diffs, fmt.Sprintf("%s: %s runs %s, the plan pins %s", ps.Alias, plan.From, d.CommitSHA, ps.CommitSHA))
		}
	}
	// A plan made while From's manifest wasn't recorded used the latest,
	// so there's nothing to compare until From records one.
	if manifest != nil && manifest.ManifestID != plan.ManifestID {
		diffs = append(diffs, fmt.Sprintf("manifest: %s runs revision %d, the plan pins %d", plan.From, manifest.ManifestRevision, plan.ManifestRevision))
	}
	return len(diffs) == 0, diffs, nil
}
