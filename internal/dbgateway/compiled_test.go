package dbgateway

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

// reads returns how many addresses were read and stored snapshots bumped
// since the last call.
func (h *serviceHarness) reads() (urls, bumps int) {
	h.urls.mu.Lock()
	urls, h.urls.reads = h.urls.reads, 0
	h.urls.mu.Unlock()
	h.store.mu.Lock()
	bumps, h.store.bumps = h.store.bumps, 0
	h.store.mu.Unlock()
	return urls, bumps
}

func (h *serviceHarness) setURL(resource, url string) {
	h.urls.mu.Lock()
	defer h.urls.mu.Unlock()
	if url == "" {
		delete(h.urls.urls, resource)
		return
	}
	h.urls.urls[resource] = url
}

// A poll that finds nothing new reads no address from Vault, seals nothing
// and writes nothing. Each kind of change still reaches the gateway.
func TestSnapshotPollWithNothingNewCompilesNothing(t *testing.T) {
	h := newServiceHarness(t)
	first, err := h.snapshot(t, 0)
	if err != nil || first == nil || first.Version != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	h.reads()
	for range 5 {
		if s, err := h.snapshot(t, 1); err != nil || s != nil {
			t.Fatalf("nothing newer: %+v %v", s, err)
		}
	}
	if urls, bumps := h.reads(); urls != 0 || bumps != 0 {
		t.Fatalf("idle polls read %d addresses and bumped %d times, want none", urls, bumps)
	}

	steps := []struct {
		name   string
		change func()
		check  func(t *testing.T, s *plane.Snapshot)
	}{
		{"password change", func() {
			h.setURL("orders", "postgres://app:rotated@db.do.example:25060/orders?sslmode=require")
		}, func(t *testing.T, s *plane.Snapshot) {
			if _, password := openCredential(t, h.keys.sealing, s.Routes[0]); password != "rotated" {
				t.Fatalf("password %q", password)
			}
		}},
		{"pause", func() { h.store.setState(h.gw.ID, "dbgr_2", "paused") }, func(t *testing.T, s *plane.Snapshot) {
			if s.Routes[1].State != plane.RouteStatePaused {
				t.Fatalf("billing state %q", s.Routes[1].State)
			}
		}},
		{"address gone", func() { h.setURL("billing", "") }, func(t *testing.T, s *plane.Snapshot) {
			if len(s.Routes) != 1 || s.Routes[0].ID != "dbgr_1" {
				t.Fatalf("routes %+v", s.Routes)
			}
		}},
		{"new route", func() {
			h.store.mu.Lock()
			h.store.routes[h.gw.ID] = append(h.store.routes[h.gw.ID], RouteRecord{ID: "dbgr_3", Resource: "billing", State: "active", Salt: testSalt})
			h.store.mu.Unlock()
			h.setURL("billing", "postgres://billing:pw2@db.do.example:25060/billing")
		}, func(t *testing.T, s *plane.Snapshot) {
			if len(s.Routes) != 3 {
				t.Fatalf("routes %+v", s.Routes)
			}
		}},
	}
	version := uint64(1)
	for _, step := range steps {
		step.change()
		s, err := h.snapshot(t, version)
		if err != nil || s == nil || s.Version != version+1 {
			t.Fatalf("after the %s: %+v %v", step.name, s, err)
		}
		step.check(t, s)
		version = s.Version
		h.reads()
		if s, err := h.snapshot(t, version); err != nil || s != nil {
			t.Fatalf("polling again after the %s: %+v %v", step.name, s, err)
		}
		if urls, bumps := h.reads(); urls != 0 || bumps != 0 {
			t.Fatalf("idle poll after the %s read %d addresses and bumped %d times", step.name, urls, bumps)
		}
	}
}

// A gateway that asks from an older version, as a new instance does, gets
// the snapshot compiled and sealed for that poll.
func TestSnapshotForAnOlderVersionIsCompiledAfresh(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.snapshot(t, 0); err != nil {
		t.Fatal(err)
	}
	h.reads()
	s, err := h.snapshot(t, 0)
	if err != nil || s == nil || s.Version != 1 {
		t.Fatalf("second instance: %+v %v", s, err)
	}
	if urls, _ := h.reads(); urls != 2 {
		t.Fatalf("read %d addresses for a snapshot it served, want both", urls)
	}
}

// A stored snapshot another control plane wrote is never taken for this
// one's last compile.
func TestSnapshotAnotherWriterForcesACompile(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.snapshot(t, 0); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	h.store.version, h.store.digest = 2, "another-writer"
	h.store.mu.Unlock()
	s, err := h.snapshot(t, 1)
	if err != nil || s == nil || s.Version != 3 {
		t.Fatalf("after another writer: %+v %v", s, err)
	}
}

// Without cheap versions, or when reading them fails, every poll compiles,
// as before.
func TestSnapshotWithoutVersionsCompilesEachPoll(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.snapshot(t, 0); err != nil {
		t.Fatal(err)
	}
	h.urls.mu.Lock()
	h.urls.versionErr = errors.New("vault metadata unavailable")
	h.urls.mu.Unlock()
	h.reads()
	if s, err := h.snapshot(t, 1); err != nil || s != nil {
		t.Fatalf("nothing newer: %+v %v", s, err)
	}
	if urls, bumps := h.reads(); urls != 2 || bumps != 1 {
		t.Fatalf("read %d addresses and bumped %d times, want a full compile", urls, bumps)
	}

	plain := newService(h.store, onlyURLs{h.urls}, h.svc.key, h.svc.trust, h.svc.now)
	if _, ok := plain.inputs(t.Context(), &h.gw); ok {
		t.Fatal("inputs without URLVersions")
	}
}

// onlyURLs hides URLVersion.
type onlyURLs struct{ u ResourceURLs }

func (o onlyURLs) URL(ctx context.Context, org, env, resource string) (string, error) {
	return o.u.URL(ctx, org, env, resource)
}

// A snapshot served after idle polls is the one a service that compiles
// every poll serves, sealed credentials aside, which differ by design.
func TestSnapshotContentUnchangedByTheCache(t *testing.T) {
	cached, plain := newServiceHarness(t), newServiceHarness(t)
	plain.svc = newService(plain.store, onlyURLs{plain.urls}, plain.svc.key, plain.svc.trust, plain.svc.now)
	for _, h := range []*serviceHarness{cached, plain} {
		if _, err := h.snapshot(t, 0); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := h.snapshot(t, 1); err != nil {
				t.Fatal(err)
			}
		}
		h.setURL("orders", "postgres://app:rotated@db.do.example:25060/orders?sslmode=require")
	}
	a, err := cached.snapshot(t, 1)
	if err != nil || a == nil {
		t.Fatalf("cached: %+v %v", a, err)
	}
	b, err := plain.snapshot(t, 1)
	if err != nil || b == nil {
		t.Fatalf("plain: %+v %v", b, err)
	}
	da, err := snapshotDigest(*a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := snapshotDigest(*b)
	if err != nil {
		t.Fatal(err)
	}
	if da != db || a.Version != b.Version {
		t.Fatalf("cached snapshot v%d %s, plain v%d %s", a.Version, da, b.Version, db)
	}
	for i := range a.Routes {
		ua, pa := openCredential(t, cached.keys.sealing, a.Routes[i])
		ub, pb := openCredential(t, plain.keys.sealing, b.Routes[i])
		if !reflect.DeepEqual([]string{ua, pa}, []string{ub, pb}) {
			t.Fatalf("route %d credential %s/%s, want %s/%s", i, ua, pa, ub, pb)
		}
	}
}
