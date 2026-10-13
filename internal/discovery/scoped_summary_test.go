package discovery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/security"
	pd "github.com/xraph/loomplane/pkg/discovery"
)

type summaryStore struct {
	*memStore
	graphReads  int
	withRemoved bool
}

func (s *summaryStore) GetAnalysis(ctx context.Context, orgID, id string) (*Analysis, error) {
	a, err := s.memStore.GetAnalysis(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return s.analyses[a.ID], nil
}

func (s *summaryStore) ListAnalyses(ctx context.Context, orgID, projectID string, limit int) ([]*Analysis, error) {
	list, err := s.memStore.ListAnalyses(ctx, orgID, projectID, limit)
	for i, a := range list {
		list[i] = s.analyses[a.ID]
	}
	return list, err
}

func (s *summaryStore) ListSourceAnalyses(ctx context.Context, orgID, sourceID string, limit int) ([]*Analysis, error) {
	list, err := s.memStore.ListSourceAnalyses(ctx, orgID, sourceID, limit)
	for i, a := range list {
		list[i] = s.analyses[a.ID]
	}
	return list, err
}

func (s *summaryStore) ListSources(ctx context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error) {
	s.withRemoved = withRemoved
	return s.memStore.ListSources(ctx, orgID, projectID, withRemoved)
}

func (s *summaryStore) Graph(ctx context.Context, orgID, id string) (*graph.Graph, error) {
	s.graphReads++
	return s.memStore.Graph(ctx, orgID, id)
}

func summaryGraph() *graph.Graph {
	u := pd.Unit{ID: "go:.", Ecosystem: pd.Go, Path: ".", Entrypoints: []pd.Entrypoint{
		{Name: "api", Path: "cmd/api"},
		{Name: "demo", Path: "examples/demo/api"},
		{Name: "worker", Path: "examples/demo/worker"},
	}}
	return graph.Build(&pd.Report{Units: []pd.Unit{u}})
}

func TestAnalysisSummariesFollowCurrentSourceRootWithoutRewritingStoredAnalysis(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	viewer := f.as(t, "ausr_viewer", security.RoleViewer)
	src, err := f.store.GetSource(ctx, org, project, "mono")
	if err != nil {
		t.Fatal(err)
	}
	a := &Analysis{ID: "anl_scoped", OrgID: org, ProjectID: project, SourceID: src.ID, Source: src.Alias, Status: StatusQueued}
	if err = f.store.CreateAnalysis(ctx, a); err != nil {
		t.Fatal(err)
	}
	raw := summaryGraph()
	if err = f.store.Succeed(ctx, org, a.ID, raw, time.Now()); err != nil {
		t.Fatal(err)
	}
	storedSummary := f.store.analyses[a.ID].Summary
	before, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	store := &summaryStore{memStore: f.store}
	f.svc.store = store
	for _, tt := range []struct {
		root     string
		services int
	}{
		{"examples/demo", 2},
		{"cmd/api", 1},
		{"", 1},
	} {
		t.Run(tt.root, func(t *testing.T) {
			src.Root = tt.root
			if updateErr := f.store.UpdateSource(ctx, src); updateErr != nil {
				t.Fatal(updateErr)
			}
			got, getErr := f.svc.Get(ctx, viewer, a.ID)
			if getErr != nil || got.Summary.Services != tt.services {
				t.Fatalf("Get = %+v, %v, want %d services", got, getErr, tt.services)
			}
			list, listErr := f.svc.List(ctx, viewer, project)
			if listErr != nil || len(list) != 1 || list[0].Summary.Services != tt.services || !store.withRemoved {
				t.Fatalf("List = %+v, %v, removed sources %v", list, listErr, store.withRemoved)
			}
			reads := store.graphReads
			view, viewErr := f.svc.ProjectGraph(ctx, viewer, project)
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			state := view.Sources[0]
			if state.Latest.Summary.Services != tt.services || state.Shown.Summary.Services != tt.services || len(view.Graph.Services) != tt.services || store.graphReads != reads+1 {
				t.Fatalf("source summaries disagree with selected graph: %+v; graph reads %d", state, store.graphReads-reads)
			}
			if f.store.analyses[a.ID].Summary != storedSummary {
				t.Fatal("visible summary rewrote stored analysis")
			}
		})
	}
	after, err := json.Marshal(raw)
	if err != nil || string(before) != string(after) {
		t.Fatalf("visible summary changed raw graph: %v", err)
	}
	exposed, err := f.svc.Graph(ctx, viewer, a.ID)
	if err != nil || len(exposed.Services) != 3 {
		t.Fatalf("analysis graph endpoint must preserve raw services: %+v, %v", exposed, err)
	}
	src.Root = "examples/demo"
	if updateErr := f.store.UpdateSource(ctx, src); updateErr != nil {
		t.Fatal(updateErr)
	}
	failed := &Analysis{ID: "anl_failed_latest", OrgID: org, ProjectID: project, SourceID: src.ID, Source: src.Alias,
		Status: StatusFailed, CreatedAt: time.Now(), Summary: graph.Summary{Services: 19}}
	if createErr := f.store.CreateAnalysis(ctx, failed); createErr != nil {
		t.Fatal(createErr)
	}
	reads := store.graphReads
	view, err := f.svc.ProjectGraph(ctx, viewer, project)
	if err != nil || view.Sources[0].Latest.Summary != failed.Summary || view.Sources[0].Shown.Summary.Services != 2 || store.graphReads != reads+1 {
		t.Fatalf("failed latest summary changed or successful graph was read twice: %+v, %v", view, err)
	}
	if removeErr := f.store.RemoveSource(ctx, org, project, src.Alias, time.Now()); removeErr != nil {
		t.Fatal(removeErr)
	}
	got, err := f.svc.Get(ctx, viewer, a.ID)
	if err != nil || got.Summary.Services != 2 || !store.withRemoved {
		t.Fatalf("removed source lost its root: %+v, %v", got, err)
	}
	f.store.analyses[a.ID].SourceID = "src_legacy_missing"
	got, err = f.svc.Get(ctx, viewer, a.ID)
	if err != nil || got.Summary.Services != 1 {
		t.Fatalf("missing legacy source must use default scope: %+v, %v", got, err)
	}
}

func TestAnalysisSummariesLeaveActiveAndFailedRunsUntouched(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	viewer := f.as(t, "ausr_viewer", security.RoleViewer)
	store := &summaryStore{memStore: f.store}
	f.svc.store = store
	for _, status := range []string{StatusQueued, StatusAnalyzing, StatusFailed} {
		a := &Analysis{ID: "anl_" + status, OrgID: org, ProjectID: project, Status: status, Summary: graph.Summary{Services: 19}}
		if err := f.store.CreateAnalysis(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, err := f.svc.Get(ctx, viewer, a.ID)
		if err != nil || got.Summary != a.Summary {
			t.Fatalf("unfinished/failed summary changed: %+v, %v", got, err)
		}
	}
	list, err := f.svc.List(ctx, viewer, project)
	if err != nil || len(list) != 3 || store.graphReads != 0 {
		t.Fatalf("non-successful list read graphs: %+v, %v, reads %d", list, err, store.graphReads)
	}
}
