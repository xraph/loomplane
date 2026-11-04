package platformcloud

import (
	"context"
	"errors"
	"sync"
	"testing"

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
	stops      int
	starts     []string
	failStarts int
}

func (f *fakeLifecycle) Get(context.Context, id.ID) (*cpinstance.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &cpinstance.Instance{State: f.state}, nil
}
func (f *fakeLifecycle) Stop(context.Context, id.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.state = cpprovider.StateStopped
	return nil
}
func (f *fakeLifecycle) Start(ctx context.Context, _ id.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	lc := &fakeLifecycle{state: cpprovider.StateRunning}
	return &instances{svc: &fakePlacements{room: room}, regions: noRegions{}, ctrl: lc}, lc
}

// With no room on another host a move stops nothing.
func TestRedeployWithNoRoomStopsNothing(t *testing.T) {
	ic, lc := newTestInstances(false)
	if err := ic.Redeploy(t.Context(), testIID.String()); !errors.Is(err, platformcloud.ErrNoCapacity) {
		t.Fatalf("redeploy = %v, want no capacity", err)
	}
	if lc.stops != 0 || len(lc.starts) != 0 || lc.state != cpprovider.StateRunning {
		t.Fatalf("stops %d, starts %v, state %s", lc.stops, lc.starts, lc.state)
	}
}

// A move waits for its start, and one whose new host fails is started
// again on the host it came from.
func TestRedeployPutsAFailedMoveBack(t *testing.T) {
	ic, lc := newTestInstances(true)
	if err := ic.Redeploy(t.Context(), testIID.String()); err != nil {
		t.Fatal(err)
	}
	if lc.stops != 1 || len(lc.starts) != 1 || lc.starts[0] != "" || lc.state != cpprovider.StateRunning {
		t.Fatalf("a move that worked: stops %d, starts %v, state %s", lc.stops, lc.starts, lc.state)
	}

	ic, lc = newTestInstances(true)
	lc.failStarts = 1
	err := ic.Redeploy(t.Context(), testIID.String())
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
