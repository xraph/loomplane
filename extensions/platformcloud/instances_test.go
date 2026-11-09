package platformcloud

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	cpinstance "github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/platformcloud"
)

var testIID = id.New(id.PrefixInstance)

type fakePlacements struct {
	deployments.PlatformHosts // not reached
	room                      bool
}

func (f *fakePlacements) PlacementOfInstance(context.Context, string) (*platformcloud.Placement, error) {
	host := "host_a"
	return &platformcloud.Placement{ID: "pl_1", OrgID: "org_1", RegionID: "reg_1", HostID: &host, Kind: "service", State: "running", InstanceID: testIID.String()}, nil
}
func (f *fakePlacements) RegionName(context.Context, string) (string, error) { return "eu-west", nil }
func (f *fakePlacements) ActiveBuilds(context.Context, string) ([]platformcloud.Placement, error) {
	return nil, nil
}
func (f *fakePlacements) HasRoomElsewhere(context.Context, platformcloud.Placement) (bool, error) {
	return f.room, nil
}

type noRegions struct{}

func (noRegions) PrepareRegion(string) error { return nil }

// fakeLifecycle is Ctrl Plane's instance lifecycle in memory. Starts fail
// while failStarts is above zero; each start records the host it was asked
// to keep.
type fakeLifecycle struct {
	mu         sync.Mutex
	state      cpprovider.InstanceState
	updated    time.Time
	stops      int
	starts     []string
	failStarts int
	// onStart runs at the start of each Start, before it fails or works.
	onStart func(ctx context.Context)
	// liveStarts records whether each start's context was still live.
	liveStarts []bool
	// resets records the cutoff of each reset the control asked for.
	resets []time.Time
}

func (f *fakeLifecycle) Get(context.Context, id.ID) (*cpinstance.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &cpinstance.Instance{Entity: ctrlplane.Entity{UpdatedAt: f.updated}, State: f.state}, nil
}
func (f *fakeLifecycle) Stop(context.Context, id.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.state = cpprovider.StateStopped
	return nil
}
func (f *fakeLifecycle) Start(ctx context.Context, _ id.ID) error {
	if f.onStart != nil {
		f.onStart(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.liveStarts = append(f.liveStarts, ctx.Err() == nil)
	f.starts = append(f.starts, platformcloud.KeptHost(ctx))
	if f.failStarts > 0 {
		f.failStarts--
		f.state = cpprovider.StateFailed
		return errors.New("apply failed")
	}
	f.state = cpprovider.StateRunning
	return nil
}

func newTestInstances(room bool) (*instances, *fakeLifecycle) {
	lc := &fakeLifecycle{state: cpprovider.StateRunning, updated: time.Now()}
	ic := &instances{svc: &fakePlacements{room: room}, regions: noRegions{}, ctrl: lc}
	ic.resetStuck = func(_ context.Context, _ id.ID, state cpprovider.InstanceState, since time.Time) error {
		lc.mu.Lock()
		defer lc.mu.Unlock()
		lc.resets = append(lc.resets, since)
		if lc.state == state && lc.updated.Before(since) {
			lc.state, lc.updated = cpprovider.StateFailed, time.Now()
		}
		return nil
	}
	return ic, lc
}

// With no room on another host a move stops nothing.
func TestMoveWithNoRoomStopsNothing(t *testing.T) {
	ic, lc := newTestInstances(false)
	if err := ic.Move(t.Context(), testIID.String()); !errors.Is(err, platformcloud.ErrNoCapacity) {
		t.Fatalf("redeploy = %v, want no capacity", err)
	}
	if lc.stops != 0 || len(lc.starts) != 0 || lc.state != cpprovider.StateRunning {
		t.Fatalf("stops %d, starts %v, state %s", lc.stops, lc.starts, lc.state)
	}
}

// A move waits for its start, and one whose new host fails is started
// again on the host it came from.
func TestMovePutsAFailedMoveBack(t *testing.T) {
	ic, lc := newTestInstances(true)
	if err := ic.Move(t.Context(), testIID.String()); err != nil {
		t.Fatal(err)
	}
	if lc.stops != 1 || len(lc.starts) != 1 || lc.starts[0] != "" || lc.state != cpprovider.StateRunning {
		t.Fatalf("a move that worked: stops %d, starts %v, state %s", lc.stops, lc.starts, lc.state)
	}

	ic, lc = newTestInstances(true)
	lc.failStarts = 1
	err := ic.Move(t.Context(), testIID.String())
	if !errors.Is(err, errMoveFailed) {
		t.Fatalf("redeploy = %v, want the move reported failed", err)
	}
	if len(lc.starts) != 2 || lc.starts[0] != "" || lc.starts[1] != "host_a" || lc.state != cpprovider.StateRunning {
		t.Fatalf("starts %v (kept hosts), state %s, want a second start back on host_a", lc.starts, lc.state)
	}
}

// Start waits for the instance to run and reports a start that fails.
func TestStartWaitsAndReportsFailure(t *testing.T) {
	ic, lc := newTestInstances(true)
	lc.state = cpprovider.StateStopped
	lc.failStarts = 1
	if err := ic.Start(t.Context(), testIID.String()); err == nil {
		t.Fatal("a failed start was reported as started")
	}
	if err := ic.Start(t.Context(), testIID.String()); err != nil || lc.state != cpprovider.StateRunning {
		t.Fatalf("start = %v, state %s", err, lc.state)
	}
}

// A repair redeploys whatever the room elsewhere: the reservation may keep
// the host it is on, the only one in its region.
func TestRedeployRepairsWithNoRoomElsewhere(t *testing.T) {
	ic, lc := newTestInstances(false)
	if err := ic.Redeploy(t.Context(), testIID.String()); err != nil {
		t.Fatal(err)
	}
	if lc.stops != 1 || len(lc.starts) != 1 || lc.state != cpprovider.StateRunning {
		t.Fatalf("stops %d, starts %v, state %s", lc.stops, lc.starts, lc.state)
	}
}

// A request that goes away mid-move doesn't strand the service: the start
// and the start back where it was run on contexts of their own.
func TestMoveSurvivesACancelledRequest(t *testing.T) {
	ic, lc := newTestInstances(true)
	ctx, cancel := context.WithCancel(t.Context())
	lc.failStarts = 1
	lc.onStart = func(context.Context) { cancel() }
	if err := ic.Move(ctx, testIID.String()); !errors.Is(err, errMoveFailed) {
		t.Fatalf("move = %v", err)
	}
	if len(lc.starts) != 2 || lc.starts[1] != "host_a" || lc.state != cpprovider.StateRunning {
		t.Fatalf("starts %v, state %s, want it started back on host_a", lc.starts, lc.state)
	}
	for i, live := range lc.liveStarts {
		if !live {
			t.Fatalf("start %d ran on a cancelled context", i)
		}
	}
}

// An instance stuck starting past the start limit, as a start whose own
// failure write died leaves it, is started again by a repair.
func TestRepairRestartsAnInstanceStuckStarting(t *testing.T) {
	ic, lc := newTestInstances(false)
	lc.state, lc.updated = cpprovider.StateStarting, time.Now().Add(-2*(startTimeout+stuckGrace))
	if err := ic.Redeploy(t.Context(), testIID.String()); err != nil {
		t.Fatal(err)
	}
	if len(lc.starts) != 1 || lc.state != cpprovider.StateRunning {
		t.Fatalf("starts %v, state %s", lc.starts, lc.state)
	}
	// One that has only just begun starting is left to finish.
	ic, lc = newTestInstances(false)
	lc.state = cpprovider.StateStarting
	if err := ic.Redeploy(t.Context(), testIID.String()); err == nil || len(lc.starts) != 0 {
		t.Fatalf("a fresh start was restarted: %v, %v", err, lc.starts)
	}
}

// The reset waits a grace beyond the timeout: an instance past the timeout
// but inside the grace is left alone, one past both is reset.
func TestStuckResetWaitsForTheGrace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   cpprovider.InstanceState
		timeout time.Duration
	}{
		{"starting", cpprovider.StateStarting, startTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ic, lc := newTestInstances(false)
			lc.state, lc.updated = tc.state, time.Now().Add(-tc.timeout-stuckGrace/2)
			inst, err := ic.current(t.Context(), testIID)
			if err != nil || inst.State != tc.state || len(lc.resets) != 0 {
				t.Fatalf("inside the grace: %v, state %s, resets %v", err, inst.State, lc.resets)
			}
			lc.updated = time.Now().Add(-tc.timeout - stuckGrace - time.Second)
			inst, err = ic.current(t.Context(), testIID)
			if err != nil || inst.State != cpprovider.StateFailed || len(lc.resets) != 1 {
				t.Fatalf("past the grace: %v, state %s, resets %v", err, inst.State, lc.resets)
			}
			if age := time.Since(lc.resets[0]); age < tc.timeout+stuckGrace-time.Second || age > tc.timeout+stuckGrace+time.Minute {
				t.Fatalf("cutoff is %s old", age)
			}
		})
	}
}
