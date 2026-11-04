//go:build integration

package agentreports

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agenttest"
)

func openPg(t *testing.T) *pgdriver.PgDB {
	t.Helper()
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), agenttest.FreshDatabase(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

// pgPeek reads and writes the delivery columns directly.
type pgPeek struct {
	pg    *pgdriver.PgDB
	store *PgStore
}

func (p pgPeek) hidden(t *testing.T, orgID, id string) record {
	t.Helper()
	r, err := scanRecord(p.pg.QueryRow(context.Background(), `SELECT `+reportColumns+` FROM loomplane_agent_reports WHERE org_id = $1 AND id = $2`, orgID, id))
	if err != nil {
		t.Fatal(err)
	}
	return *r
}

func (p pgPeek) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	res, err := p.pg.Exec(context.Background(), q, args...)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("%d rows, %v", n, err)
	}
}

func (p pgPeek) setIssueNumber(t *testing.T, orgID, id string, n int) {
	t.Helper()
	p.exec(t, `UPDATE loomplane_agent_reports SET issue_number = $3, issue_state = 'open' WHERE org_id = $1 AND id = $2`, orgID, id, n)
}

func (p pgPeek) noticeSent(t *testing.T, orgID, id string, at time.Time) {
	t.Helper()
	p.exec(t, `UPDATE loomplane_agent_reports SET pending_notice = '', last_notice_at = $3 WHERE org_id = $1 AND id = $2`, orgID, id, at.UTC())
}

func TestPgStoreRunsTheServiceRules(t *testing.T) {
	pg := openPg(t)
	store := NewPgStore(pg)
	for i, c := range serviceCases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, newHarness(t, store, pgPeek{pg: pg, store: store}, fmt.Sprintf("org_%02d", i)))
		})
	}
}

// Two writers queueing the same investigation: exactly one wins.
func TestSetInvestigationIsConditional(t *testing.T) {
	pg := openPg(t)
	store := NewPgStore(pg)
	h := newHarness(t, store, pgPeek{pg: pg, store: store}, "org_a")
	r := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	const writers = 8
	var wg sync.WaitGroup
	results := make(chan bool, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := h.svc.SetInvestigation(context.Background(), h.org, r.ID, []string{InvestigationNone},
				Investigation{Status: InvestigationQueued, TaskID: fmt.Sprintf("atask_%d", i)})
			if err != nil {
				t.Error(err)
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for ok := range results {
		if ok {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d writers moved it", won)
	}
}

// Eight detectors seeing the same new problem at once: one report, every
// occurrence counted.
func TestPgStoreObserveIsRaceFree(t *testing.T) {
	pg := openPg(t)
	store := NewPgStore(pg)
	h := newHarness(t, store, pgPeek{pg: pg, store: store}, "org_a")
	const writers = 8
	var wg sync.WaitGroup
	changes := make(chan Change, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			obs, err := h.svc.Observe(context.Background(), h.signal(SignalDeploymentFailed, "service:api", fmt.Sprintf("dep_%d#0", i)))
			if err != nil {
				t.Error(err)
				return
			}
			changes <- obs.Change
		}()
	}
	wg.Wait()
	close(changes)
	opened := 0
	for c := range changes {
		if c == ChangeOpened {
			opened++
		}
	}
	open, err := h.svc.Open(context.Background(), h.org, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if opened != 1 || len(open) != 1 || open[0].Occurrences != writers || open[0].Revision != writers {
		t.Fatalf("opened %d, open reports %d, occurrences %d", opened, len(open), open[0].Occurrences)
	}
	if keys := h.peek.hidden(t, h.org, open[0].ID).OccurrenceKeys; len(keys) != writers {
		t.Fatalf("keys %v", keys)
	}
}

// A reopen clears who took the report, and the keys stay bounded.
func TestPgStoreReopenClearsTheTakerAndKeepsTheLastKeys(t *testing.T) {
	pg := openPg(t)
	store := NewPgStore(pg)
	peek := pgPeek{pg: pg, store: store}
	h := newHarness(t, store, peek, "org_a")
	r := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_0#0")).Report
	for i := 1; i <= MaxOccurrenceKeys+5; i++ {
		h.observe(t, h.signal(SignalDeploymentFailed, "service:api", fmt.Sprintf("dep_%d#0", i)))
	}
	keys := peek.hidden(t, h.org, r.ID).OccurrenceKeys
	if len(keys) != MaxOccurrenceKeys || keys[0] != "dep_6#0" || keys[len(keys)-1] != fmt.Sprintf("dep_%d#0", MaxOccurrenceKeys+5) {
		t.Fatalf("%d keys, from %s to %s", len(keys), keys[0], keys[len(keys)-1])
	}
	peek.exec(t, `UPDATE loomplane_agent_reports SET taken_by = $3, taken_at = $4 WHERE org_id = $1 AND id = $2`, h.org, r.ID, developer, base)
	if got := h.lookup(t, r.ID); got.TakenBy != developer || got.TakenAt == nil {
		t.Fatalf("taken: %+v", got)
	}
	h.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}})
	back := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_99#0"))
	if back.Change != ChangeReopened || back.Report.TakenBy != "" || back.Report.TakenAt != nil {
		t.Fatalf("reopened: %s %+v", back.Change, back.Report)
	}
}
