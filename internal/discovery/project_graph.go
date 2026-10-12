package discovery

import (
	"context"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/security"
)

// Graphs reads what a project's graph is composed from. Store is one.
type Graphs interface {
	ListSources(ctx context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error)
	ListSourceAnalyses(ctx context.Context, orgID, sourceID string, limit int) ([]*Analysis, error)
	Graph(ctx context.Context, orgID, id string) (*graph.Graph, error)
}

// SourceState is one source of a project's graph: its newest analysis, in
// any state, and the newest that succeeded, which is the one in the graph.
type SourceState struct {
	Source Source    `json:"source"`
	Latest *Analysis `json:"latest,omitempty"`
	Shown  *Analysis `json:"shown,omitempty"`
}

// ProjectGraph composes the newest successful analysis of each of a
// project's live sources into its application graph. Nothing stores the
// result, so it can't go stale.
func ProjectGraph(ctx context.Context, g Graphs, orgID, projectID string) (*graph.Graph, []SourceState, error) {
	sources, err := g.ListSources(ctx, orgID, projectID, false)
	if err != nil {
		return nil, nil, err
	}
	parts := make([]graph.Part, 0, len(sources))
	states := make([]SourceState, 0, len(sources))
	for _, src := range sources {
		list, err := g.ListSourceAnalyses(ctx, orgID, src.ID, listLimit)
		if err != nil {
			return nil, nil, err
		}
		st := SourceState{Source: *src}
		part := graph.Part{Source: graph.SourceRef{Alias: src.Alias, Repository: src.Repository, Root: src.Root}}
		if len(list) > 0 {
			st.Latest = list[0]
		}
		for _, a := range list {
			if a.Status != StatusSucceeded {
				continue
			}
			gr, err := g.Graph(ctx, orgID, a.ID)
			if err != nil {
				return nil, nil, err
			}
			st.Shown = a
			part.Source.CommitSHA, part.Source.AnalysisID, part.Graph = a.CommitSHA, a.ID, gr
			break
		}
		parts = append(parts, part)
		states = append(states, st)
	}
	return graph.Compose(parts), states, nil
}

// ProjectView is a project's application graph and what each of its
// sources contributed.
type ProjectView struct {
	ProjectID string        `json:"project_id" forge:"id"`
	Graph     *graph.Graph  `json:"graph"`
	Sources   []SourceState `json:"sources"`
}

// ProjectGraph returns the project's composed graph.
func (s *Service) ProjectGraph(ctx context.Context, p security.Principal, projectID string) (*ProjectView, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	g, states, err := ProjectGraph(ctx, s.store, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	return &ProjectView{ProjectID: projectID, Graph: g, Sources: states}, nil
}
