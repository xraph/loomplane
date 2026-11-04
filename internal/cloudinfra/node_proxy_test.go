package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/server"
)

const testNonce = "0123456789abcdef0123456789abcdef"

// proxyRoute is a route on srv_1, with a handoff's challenge address so
// port 80 already has a server.
func proxyRoute(id, host string) server.IngressRoute {
	return server.IngressRoute{ID: id, Hostname: host, ChallengeAddress: "198.51.100.20", Backend: server.IngressBackend{OrgID: "org", ProjectID: "shop", EnvironmentID: "prod", Service: "web",
		InstanceID: "web", ReleaseID: "web/release", ContainerID: "web-container", Port: 8080, Address: "10.0.0.2"}}
}

func proxyConfig(t *testing.T, check string, routes ...server.IngressRoute) []byte {
	t.Helper()
	raw, err := server.IngressConfig{OrgID: "org", ServerID: "srv_1", Version: "v1", Routes: routes, AddressCheck: check}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestConfirmProxyStateAgainstStored(t *testing.T) {
	initial, err := server.IngressConfig{OrgID: "org", ServerID: "srv_1", Version: "initial", Routes: []server.IngressRoute{}}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	initialCheck, err := server.IngressConfig{OrgID: "org", ServerID: "srv_1", Version: "initial", Routes: []server.IngressRoute{}, AddressCheck: testNonce}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	a, b := proxyRoute("r1", "a.example.com"), proxyRoute("r2", "b.example.com")
	stored := proxyConfig(t, "", a)
	for _, c := range []struct {
		name         string
		stored, live []byte
		want         proxyState
	}{
		{"the stored config", stored, proxyConfig(t, "", a), proxyAsStored},
		{"the stored config and a check", stored, proxyConfig(t, testNonce, a), proxyWithAddressCheck},
		{"the initial config and a check", initial, initialCheck, proxyWithAddressCheck},
		{"another route", stored, proxyConfig(t, "", a, b), proxyChanged},
		{"another route and a check", stored, proxyConfig(t, testNonce, a, b), proxyChanged},
		{"a route taken away", stored, initial, proxyChanged},
		{"a config that isn't JSON", stored, []byte("{"), proxyChanged},
	} {
		if got := proxyAgainst(c.stored, c.live); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestConfirmIngressExpected(t *testing.T) {
	a := proxyRoute("r1", "a.example.com")
	previous := &server.IngressSnapshot{ContainerID: "proxy", ETag: "1", Config: proxyConfig(t, "", a)}
	for _, c := range []struct {
		name   string
		actual *server.IngressSnapshot
		taken  bool
	}{
		{"a check left behind", &server.IngressSnapshot{ContainerID: "proxy", ETag: "2", Config: proxyConfig(t, testNonce, a)}, true},
		{"the same config under a new etag", &server.IngressSnapshot{ContainerID: "proxy", ETag: "3", Config: proxyConfig(t, "", a)}, true},
		{"another config", &server.IngressSnapshot{ContainerID: "proxy", ETag: "4", Config: proxyConfig(t, testNonce, a, proxyRoute("r2", "b.example.com"))}, false},
		{"another proxy", &server.IngressSnapshot{ContainerID: "other", ETag: "5", Config: proxyConfig(t, testNonce, a)}, false},
		{"nothing read", nil, false},
	} {
		got := ingressExpected(previous, c.actual)
		if (got == c.actual) != c.taken || (!c.taken && got != previous) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if ingressExpected(nil, previous) != nil {
		t.Error("no previous snapshot took the live one")
	}
}

// fakeAdmin is Caddy's admin as the address check drives it: a config, an
// etag that moves on every load, and compare-and-swap. failApply fails an
// apply before it loads; lostApply loads it and reports an error, as a
// stream that dropped before the answer would.
type fakeAdmin struct {
	mu        sync.Mutex
	config    []byte
	etag      int
	applies   int
	failApply map[int]error
	lostApply map[int]bool
	ensureErr error
	ensured   int
}

func newFakeAdmin(config []byte) *fakeAdmin {
	return &fakeAdmin{config: config, etag: 1, failApply: map[int]error{}, lostApply: map[int]bool{}}
}

func (f *fakeAdmin) snapshot() *server.IngressSnapshot {
	return &server.IngressSnapshot{ContainerID: "proxy", ETag: fmt.Sprint(f.etag), Config: append([]byte(nil), f.config...)}
}

func (f *fakeAdmin) Ensure(context.Context) (*server.IngressSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured++
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	return f.snapshot(), nil
}

func (f *fakeAdmin) Read(context.Context) (*server.IngressSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot(), nil
}

func (f *fakeAdmin) Apply(_ context.Context, expected *server.IngressSnapshot, v server.IngressConfig) (*server.IngressSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies++
	if err := f.failApply[f.applies]; err != nil {
		return nil, err
	}
	if expected.ContainerID != "proxy" || expected.ETag != fmt.Sprint(f.etag) {
		return nil, errors.New("HTTP 412: the config changed")
	}
	raw, err := v.JSON()
	if err != nil {
		return nil, err
	}
	f.config, f.etag = raw, f.etag+1
	if f.lostApply[f.applies] {
		return nil, errors.New("the stream dropped before the answer")
	}
	return f.snapshot(), nil
}

func (f *fakeAdmin) serving() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Contains(string(f.config), server.NodeAddressPath+testNonce)
}

func (f *fakeAdmin) runs(t *testing.T, want []byte) bool {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return server.SameIngressConfig(want, f.config)
}

// proxyCheck is one address check through a fake admin.
type proxyCheck struct {
	svc     *Service
	state   *IngressState
	saved   []*server.IngressSnapshot
	warned  []string
	checked int
}

func newProxyCheck(t *testing.T, admin *fakeAdmin, routes ...server.IngressRoute) *proxyCheck {
	t.Helper()
	pc := &proxyCheck{svc: NewService(Deps{})}
	pc.svc.Warn = func(msg string, _ error) { pc.warned = append(pc.warned, msg) }
	if routes != nil {
		pc.state = &IngressState{Config: server.IngressConfig{OrgID: "org", ServerID: "srv_1", Version: "v1", Routes: routes}, Current: admin.snapshot()}
	} else {
		pc.state = &IngressState{}
	}
	return pc
}

func (pc *proxyCheck) run(t *testing.T, admin *fakeAdmin) error {
	t.Helper()
	target := nodeNonceTarget{OrgID: "org", ServerID: "srv_1"}
	save := func(_ context.Context, st *IngressState) error {
		pc.saved = append(pc.saved, st.Current)
		return nil
	}
	return pc.svc.checkThroughProxy(t.Context(), admin, target, pc.state, testNonce, func(context.Context) error {
		pc.checked++
		if !admin.serving() {
			t.Error("the check ran without the nonce route")
		}
		return nil
	}, save)
}

func TestConfirmProxyServesAndRestores(t *testing.T) {
	a := proxyRoute("r1", "a.example.com")
	stored := proxyConfig(t, "", a)
	admin := newFakeAdmin(stored)
	pc := newProxyCheck(t, admin, a)
	if err := pc.run(t, admin); err != nil || pc.checked != 1 {
		t.Fatalf("check %d: %v", pc.checked, err)
	}
	if !admin.runs(t, stored) || admin.applies != 2 {
		t.Fatalf("proxy after the check: %d applies, %s", admin.applies, admin.config)
	}
	// The snapshot it ends on is saved, so the next route change's
	// compare-and-swap matches.
	if len(pc.saved) != 1 || pc.saved[0].ETag != admin.snapshot().ETag || pc.state.Current.ETag != admin.snapshot().ETag {
		t.Fatalf("saved %+v, state %+v", pc.saved, pc.state.Current)
	}
}

func TestConfirmProxyRestoresAfterAnUnknownApply(t *testing.T) {
	a := proxyRoute("r1", "a.example.com")
	stored := proxyConfig(t, "", a)
	admin := newFakeAdmin(stored)
	admin.lostApply[1] = true
	pc := newProxyCheck(t, admin, a)
	if err := pc.run(t, admin); err == nil || pc.checked != 0 {
		t.Fatalf("check %d: %v", pc.checked, err)
	}
	if !admin.runs(t, stored) || len(pc.warned) != 0 {
		t.Fatalf("proxy %s, warned %v", admin.config, pc.warned)
	}
}

func TestConfirmProxyLeftoverHeals(t *testing.T) {
	a := proxyRoute("r1", "a.example.com")
	stored := proxyConfig(t, "", a)
	admin := newFakeAdmin(stored)
	admin.failApply[2] = errors.New("the fence moved")
	pc := newProxyCheck(t, admin, a)
	if err := pc.run(t, admin); err != nil {
		t.Fatal(err)
	}
	// The removal failed: the route is left, and the warning says what
	// takes it off.
	if !admin.serving() || len(pc.warned) != 1 || !strings.Contains(pc.warned[0], "the next address check or route change") || len(pc.saved) != 0 {
		t.Fatalf("proxy %s, warned %v, saved %v", admin.config, pc.warned, pc.saved)
	}
	// A route change applies over the proxy as it is now.
	if got := ingressExpected(pc.state.Current, admin.snapshot()); got.ETag != admin.snapshot().ETag {
		t.Fatalf("route change expects %+v", got)
	}
	// The next check takes the leftover off first, then runs.
	if err := pc.run(t, admin); err != nil || pc.checked != 2 {
		t.Fatalf("second check %d: %v", pc.checked, err)
	}
	if !admin.runs(t, stored) || len(pc.saved) != 1 || pc.state.Current.ETag != admin.snapshot().ETag {
		t.Fatalf("proxy %s, saved %v", admin.config, pc.saved)
	}
}

func TestConfirmProxyRefusesAChangedProxy(t *testing.T) {
	a := proxyRoute("r1", "a.example.com")
	admin := newFakeAdmin(proxyConfig(t, testNonce, a, proxyRoute("r2", "b.example.com")))
	pc := newProxyCheck(t, admin, a)
	if err := pc.run(t, admin); !errors.Is(err, ErrConflict) || admin.applies != 0 || pc.checked != 0 {
		t.Fatalf("%d applies, %d checks: %v", admin.applies, pc.checked, err)
	}
}

func TestConfirmProxyStartsAServerWithoutRoutes(t *testing.T) {
	initial, err := server.IngressConfig{OrgID: "org", ServerID: "srv_1", Version: "initial", Routes: []server.IngressRoute{}}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	admin := newFakeAdmin(initial)
	pc := newProxyCheck(t, admin)
	if err = pc.run(t, admin); err != nil || admin.ensured != 1 || pc.checked != 1 {
		t.Fatalf("ensured %d, checked %d: %v", admin.ensured, pc.checked, err)
	}
	// Nothing was stored for the server, so nothing is saved.
	if !admin.runs(t, initial) || len(pc.saved) != 0 {
		t.Fatalf("proxy %s, saved %v", admin.config, pc.saved)
	}
}

func TestConfirmProxyPortsTaken(t *testing.T) {
	admin := newFakeAdmin(nil)
	admin.ensureErr = fmt.Errorf("%w: docker returned HTTP 500: Bind for 0.0.0.0:80 failed: port is already allocated", server.ErrIngressPortsTaken)
	pc := newProxyCheck(t, admin)
	if err := pc.run(t, admin); !errors.Is(err, errProxyPortsTaken) || admin.applies != 0 {
		t.Fatalf("%d applies: %v", admin.applies, err)
	}
}
