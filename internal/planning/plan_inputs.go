package planning

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/projects"
)

type planInputs struct {
	graph          *graph.Graph
	held, excluded map[string]bool
	// only are the services the plan names, or nil when it names none and
	// moves them all.
	only       map[string]bool
	exclusions []string
	revision   *manifests.Revision
}

func (s *Service) loadPlanInputs(ctx context.Context, plan *Plan) (*planInputs, error) {
	if plan.Kind == KindInstance || plan.FleetInput != nil {
		return s.instancePlanInputs(ctx, plan)
	}
	if plan.ManagedInstanceID != "" {
		return nil, projects.ErrFleetManaged
	}
	// An older plan the backfill couldn't pin would plan from an empty
	// graph, so it fails instead of proposing to tear down what's deployed.
	if len(plan.Sources) == 0 && !plan.Catalog {
		return nil, fmt.Errorf("%w: this plan pins no repositories; plan again", ErrInvalid)
	}
	parts := make([]graph.Part, 0, len(plan.Sources))
	held := map[string]bool{}
	for _, ps := range plan.Sources {
		g, err := s.analyses.Graph(ctx, plan.OrgID, ps.AnalysisID)
		if err != nil {
			return nil, fmt.Errorf("load the analysis of %s: %w", ps.Alias, err)
		}
		parts = append(parts, graph.Part{
			Source: graph.SourceRef{Alias: ps.Alias, Repository: ps.Repository, Root: ps.Root, CommitSHA: ps.CommitSHA, AnalysisID: ps.AnalysisID},
			Graph:  g,
		})
		if !ps.InScope {
			held[ps.Alias] = true
		}
	}
	g := graph.Compose(parts)
	excluded, exclusions, err := s.excluded(ctx, plan, g)
	if err != nil {
		return nil, err
	}
	rev, err := s.manifests.Get(ctx, plan.OrgID, plan.ManifestID)
	if err != nil {
		return nil, fmt.Errorf("load the manifest: %w", err)
	}
	if rev.Manifest == nil {
		return nil, fmt.Errorf("%w: manifest revision %d no longer parses", ErrInvalid, rev.Revision)
	}

	var only map[string]bool
	if len(plan.Services) > 0 {
		only = map[string]bool{}
		for _, name := range plan.Services {
			only[name] = true
		}
	}
	return &planInputs{graph: g, held: held, excluded: excluded, only: only, exclusions: exclusions, revision: rev}, nil
}
