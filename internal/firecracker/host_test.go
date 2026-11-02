package firecracker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type testDriver struct {
	starts int
	live   map[string]bool
	fail   bool
}

func (d *testDriver) Check(context.Context) (Capabilities, error) {
	return Capabilities{HostID: "host", Architecture: "amd64", RuntimeIdentity: strings.Repeat("a", 64), Isolation: true}, nil
}
func (d *testDriver) Start(_ context.Context, vm VM, _ Spec) error {
	d.starts++
	d.live[vm.ID] = true
	return nil
}
func (d *testDriver) Inspect(_ context.Context, vm VM) (bool, error) { return d.live[vm.ID], nil }
func (d *testDriver) Configure(_ context.Context, _ VM, _ Request) (GuestStatus, error) {
	if d.fail {
		return GuestStatus{}, errors.New("guest failed")
	}
	return GuestStatus{Ready: true, State: "running"}, nil
}
func (d *testDriver) Stop(_ context.Context, vm VM) error { delete(d.live, vm.ID); return nil }
func (d *testDriver) Remove(context.Context, VM) error    { return nil }
func (d *testDriver) Guest(context.Context, VM, string, io.Reader) (GuestStatus, error) {
	return GuestStatus{Ready: true, State: "running"}, nil
}
func testHost(t *testing.T) (*Host, *testDriver, Request) {
	t.Helper()
	s, err := OpenStore(t.TempDir() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d := &testDriver{live: map[string]bool{}}
	h := &Host{Store: s, Driver: d, Limits: Limits{VMs: 4, VCPUs: 8, MemoryMiB: 8192, DiskMiB: 32768}, Key: []byte(strings.Repeat("k", 32))}
	q := Request{RuntimeIdentity: strings.Repeat("a", 64), Scope: Scope{Org: "org", Connection: "conn", Project: "project", Environment: "env", Resource: "api"}, Authority: Authority{Generation: 1, Operation: "deploy-1", Expires: time.Now().Add(time.Minute)}, Spec: Spec{Service: "api", Image: "registry.example/app@sha256:" + strings.Repeat("a", 64), VCPUs: 1, MemoryMiB: 512, DiskMiB: 1024, Kind: "app"}, Values: Values{Env: map[string]string{"TOKEN": "secret"}}}
	return h, d, q
}
func TestHostReplayAndRestartDoNotDuplicateVM(t *testing.T) {
	h, d, q := testHost(t)
	r, err := h.Apply(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.Apply(t.Context(), q)
	if err != nil || again.Current.ID != r.Current.ID || d.starts != 1 {
		t.Fatalf("replay: %+v %v starts %d", again, err, d.starts)
	}
	h2 := *h
	r, err = h2.Apply(t.Context(), q)
	if err != nil || r.Current.ID != again.Current.ID || d.starts != 1 {
		t.Fatal("restart duplicated VM", err)
	}
}
func TestHostRejectsStaleChangedAndExpiredOperations(t *testing.T) {
	for _, change := range []string{"stale", "changed", "expired", "ownership"} {
		t.Run(change, func(t *testing.T) {
			h, d, q := testHost(t)
			q.Authority.Generation = 2
			if _, err := h.Apply(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "stale":
				q.Authority.Generation = 1
			case "changed":
				q.Values.Env["TOKEN"] = "different"
			case "expired":
				q.Authority.Expires = time.Now().Add(-time.Second)
			case "ownership":
				q.Scope.Project = "other"
			}
			if _, err := h.Apply(t.Context(), q); err == nil {
				t.Fatal("accepted invalid authority")
			}
			if d.starts != 1 {
				t.Fatal("launched unsafe VM")
			}
		})
	}
}
func TestHostFailedReplacementPreservesCurrent(t *testing.T) {
	h, d, q := testHost(t)
	old, err := h.Apply(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	q.Authority.Generation++
	q.Authority.Operation = "deploy-2"
	q.Spec.Image = "registry.example/app@sha256:" + strings.Repeat("b", 64)
	d.fail = true
	if _, err = h.Apply(t.Context(), q); err == nil {
		t.Fatal("failed guest became ready")
	}
	r, err := h.Inspect(t.Context(), q.Scope)
	if err != nil || r.Current.ID != old.Current.ID || !d.live[old.Current.ID] {
		t.Fatal("lost current release", err)
	}
}
func TestHostCapacityAndSecretPersistence(t *testing.T) {
	h, _, q := testHost(t)
	h.Limits.VMs = 1
	if _, err := h.Apply(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q.Scope.Resource = "other"
	if _, err := h.Apply(t.Context(), q); err == nil {
		t.Fatal("overcommitted host")
	}
	records, err := h.Store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(records[0].Digest, "secret") {
		t.Fatal("secret persisted")
	}
}

func TestStoppedApplicationKeepsItsDiskAllocation(t *testing.T) {
	h, d, q := testHost(t)
	before, err := h.Apply(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	q.Authority.Generation++
	q.Authority.Operation = "stop"
	stopped, err := h.Mutate(t.Context(), q, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Current == nil || d.live[stopped.Current.ID] {
		t.Fatal("stop lost allocation or retained process")
	}
	q.Authority.Generation++
	q.Authority.Operation = "start"
	after, err := h.Apply(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if after.Current.ID != before.Current.ID || after.Current.Slot != before.Current.Slot {
		t.Fatal("start replaced stopped allocation")
	}
}
func TestExpiredBuildIsReapedAfterHostRestart(t *testing.T) {
	h, d, q := testHost(t)
	q.Spec.Kind = "build"
	q.Spec.SourceDigest = strings.Repeat("a", 64)
	q.Spec.Dockerfile = ".loomplane-build/Dockerfile"
	q.Spec.BuildImage = "registry.example/test:" + strings.Repeat("a", 24)
	r, err := h.Apply(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	r.Current.Created = time.Now().Add(-time.Hour)
	if err = h.Store.Save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err = h.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := h.Inspect(t.Context(), q.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if after.Current != nil || len(d.live) != 0 || after.State != "removed" {
		t.Fatal("abandoned build remains live", after)
	}
}

func TestRecordedDestructionResumesAfterRestart(t *testing.T) {
	for _, action := range []string{"stop", "remove"} {
		t.Run(action, func(t *testing.T) {
			h, d, q := testHost(t)
			r, err := h.Apply(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			current := r.Current.ID
			r.Pending = &VM{ID: "pending", Slot: 2, Spec: q.Spec, Network: q.Scope.Network()}
			d.live["pending"] = true
			r.State = action + "-pending"
			if err = h.Store.Save(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			restarted := *h
			if err = restarted.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			after, err := h.Inspect(t.Context(), q.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if len(d.live) != 0 || after.Pending != nil || len(after.Garbage) != 0 {
				t.Fatalf("destruction did not converge: %+v live=%v", after, d.live)
			}
			if action == "stop" && (after.State != "stopped" || after.Current == nil || after.Current.ID != current) {
				t.Fatal("lost stopped allocation", after)
			}
			if action == "remove" && (after.State != "removed" || after.Current != nil) {
				t.Fatal("retained removed allocation", after)
			}
			if err = restarted.Reconcile(t.Context()); err != nil {
				t.Fatal("replay", err)
			}
		})
	}
}

type networkTestDriver struct {
	*testDriver
	networkErr error
	refreshes  int
}

func (d *networkTestDriver) RefreshNetwork(context.Context, VM) error {
	d.refreshes++
	return d.networkErr
}
func TestReadyReplayRepairsFailedNetworkPublication(t *testing.T) {
	for _, repair := range []string{"replay", "reconcile"} {
		t.Run(repair, func(t *testing.T) {
			h, d, q := testHost(t)
			network := &networkTestDriver{testDriver: d, networkErr: errors.New("nft unavailable")}
			h.Driver = network
			if _, err := h.Apply(t.Context(), q); err == nil {
				t.Fatal("ignored network failure")
			}
			network.networkErr = nil
			if repair == "replay" {
				if _, err := h.Apply(t.Context(), q); err != nil {
					t.Fatal(err)
				}
			} else if err := h.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			if network.refreshes != 2 || d.starts != 1 {
				t.Fatalf("refreshes=%d starts=%d", network.refreshes, d.starts)
			}
		})
	}
}
func TestStagingPeersAreRevokedBeforeDestruction(t *testing.T) {
	r := Record{Current: &VM{ID: "old"}, Pending: &VM{ID: "new"}}
	for _, state := range []string{"preparing", "starting", "ready"} {
		r.State = state
		if len(r.NetworkVMs()) != 2 {
			t.Fatalf("%s excluded staging identity", state)
		}
	}
	for _, state := range []string{"stop-pending", "remove-pending", "stopped", "removed"} {
		r.State = state
		if len(r.NetworkVMs()) != 0 || r.Serving() {
			t.Fatalf("%s retained peer authority", state)
		}
	}
}

func TestRemoveAllocationThatFailedBeforeReservation(t *testing.T) {
	h, _, q := testHost(t)
	h.Limits.VMs = 0
	if _, err := h.Apply(t.Context(), q); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	q.Spec = Spec{}
	q.Authority.Generation++
	q.Authority.Operation = "remove-empty"
	r, err := h.Mutate(t.Context(), q, "remove")
	if err != nil || r.State != "removed" {
		t.Fatal(r, err)
	}
}
