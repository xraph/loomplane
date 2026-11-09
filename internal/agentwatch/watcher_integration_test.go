//go:build integration

package agentwatch

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/realtime"
)

// pgRig is watchers over one fresh database: the real watcher, report and
// event stores, with the detectors' fake world and fake runs, budget and
// jobs shared between them.
type pgRig struct {
	t       *testing.T
	pg      *pgdriver.PgDB
	store   *countingStore
	events  *realtime.PgStore
	reports *agentreports.Service
	budget  *fakeBudget
	runs    *fakeRuns
	jobs    *recordingJobs
	deps    map[string]*deployments.Deployment
}

func newPgRig(t *testing.T) *pgRig {
	t.Helper()
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), agenttest.FreshDatabase(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return &pgRig{
		t: t, pg: pg, store: &countingStore{Store: NewPgStore(pg), refused: map[string]int{}},
		events:  realtime.NewPgStore(pg),
		reports: agentreports.NewService(agentreports.Deps{Store: agentreports.NewPgStore(pg)}),
		budget:  &fakeBudget{enabled: map[string]bool{org: true}},
		runs:    &fakeRuns{tasks: map[string]*agentruns.Task{}, finish: agentruns.StatusCompleted},
		jobs:    &recordingJobs{}, deps: map[string]*deployments.Deployment{},
	}
}

// watcher is one replica. Each has a world of its own over the same
// deployments, as each replica reads the same database.
func (r *pgRig) watcher(owner string) *Watcher {
	world := newWorld()
	maps.Copy(world.deployments, r.deps)
	return &Watcher{
		Store: r.store, Events: r.events, Reports: r.reports, Budget: r.budget, Runs: r.runs, Jobs: r.jobs,
		Detector: &Detector{World: world}, Safety: &agentsafety.Scanner{Engine: &quotingShield{}},
		Binder: &agentscope.Binder{Tenants: tenants{}}, Owner: owner,
		Warn: func(msg string, err error) { r.t.Logf("%s %s: %v", owner, msg, err) },
	}
}

// fail records a failed deployment and its event, as the deployment
// store's transaction does.
func (r *pgRig) fail(id string) {
	r.t.Helper()
	dep := failedDeployment(id, "env_prod")
	r.deps[id] = dep
	raw, err := json.Marshal(dep)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err = r.pg.Exec(context.Background(), `SELECT loomplane_append_event($1, $2, 'deployment.updated', $3, $4::jsonb)`,
		org, project, id, string(raw)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *pgRig) open() []*agentreports.Report {
	r.t.Helper()
	list, err := r.reports.Open(context.Background(), org, "", "")
	if err != nil {
		r.t.Fatal(err)
	}
	return list
}

// countingStore counts the claims another pass's lease refused, by
// replica.
type countingStore struct {
	Store
	mu      sync.Mutex
	refused map[string]int
}

func (s *countingStore) Claim(ctx context.Context, orgID, owner string, ttl time.Duration, hw func() (int64, error)) (*State, bool, error) {
	st, ok, err := s.Store.Claim(ctx, orgID, owner, ttl, hw)
	if err == nil && !ok {
		s.mu.Lock()
		s.refused[strings.SplitN(owner, ":", 2)[0]]++
		s.mu.Unlock()
	}
	return st, ok, err
}

func TestTwoReplicasWatchOneWorkspaceOnce(t *testing.T) {
	r := newPgRig(t)
	ctx := context.Background()
	// The workspace's first pass starts the watcher at the high water mark.
	if err := r.watcher("a").RunOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	r.fail("dep_1")
	a, b := r.watcher("a"), r.watcher("b")
	for range 20 {
		var wg sync.WaitGroup
		for _, w := range []*Watcher{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := w.RunOrg(ctx, org); err != nil {
					t.Errorf("%s: %v", w.Owner, err)
				}
			}()
		}
		wg.Wait()
	}
	open := r.open()
	if len(open) != 1 {
		t.Fatalf("%d reports, want 1", len(open))
	}
	if n := len(r.runs.starts()); n != 1 {
		t.Fatalf("%d investigations started, want 1", n)
	}
	if got := r.jobs.deliveries(); len(got) != 1 || got[0] != org+"/"+open[0].ID {
		t.Fatalf("deliveries %v, want the report once", got)
	}
	if rep := open[0]; rep.Investigation != agentreports.InvestigationDone || rep.Occurrences != 1 {
		t.Fatalf("report investigation %s, occurrences %d", rep.Investigation, rep.Occurrences)
	}
	r.store.mu.Lock()
	refused := r.store.refused["a"] + r.store.refused["b"]
	r.store.mu.Unlock()
	if refused == 0 {
		t.Fatal("no pass ever found the other holding the workspace")
	}
	// The cursor read past the reports' own agent_report events too.
	hw, err := r.events.HighWater(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	st, err := r.store.Get(ctx, org)
	if err != nil || cursor(org, st.EventCursor) != hw || st.TickedAt == nil {
		t.Fatalf("state %+v, %v; want the cursor at %s", st, err, hw)
	}
}

func TestLeaseExpiresForACrashedReplica(t *testing.T) {
	r := newPgRig(t)
	ctx := context.Background()
	// Replica a claims and crashes without letting go.
	if _, ok, err := r.store.Claim(ctx, org, "a:crashed", time.Second, func() (int64, error) { return 0, nil }); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	r.fail("dep_1")
	b := r.watcher("b")
	if err := b.RunOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	if n := len(r.open()); n != 0 {
		t.Fatalf("b ran under a's live lease: %d reports", n)
	}
	time.Sleep(1500 * time.Millisecond)
	b = r.watcher("b")
	if err := b.RunOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	if n := len(r.open()); n != 1 {
		t.Fatalf("%d reports once a's lease ran out, want 1", n)
	}
	// a's lease is gone: its late save is refused.
	if err := r.store.Save(ctx, "a:crashed", &State{OrgID: org, EventCursor: 0}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("the crashed replica's save: %v", err)
	}
}
