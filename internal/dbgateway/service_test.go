package dbgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

// memStore stands in for Store. interfere makes LatestSnapshot report a
// concurrent writer's digest that many times.
type memStore struct {
	mu        sync.Mutex
	gateways  map[string]Gateway
	routes    map[string][]RouteRecord
	version   uint64
	digest    string
	bumps     int
	interfere int
	reports   []plane.Report
	live      int
}

func newMemStore(gws ...Gateway) *memStore {
	m := &memStore{gateways: map[string]Gateway{}, routes: map[string][]RouteRecord{}}
	for _, g := range gws {
		m.gateways[g.ID] = g
	}
	return m
}

func (m *memStore) GatewayByID(_ context.Context, id string) (*Gateway, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.gateways[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return &g, nil
}

func (m *memStore) Routes(_ context.Context, _, gatewayID string) ([]RouteRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RouteRecord{}, m.routes[gatewayID]...), nil
}

func (m *memStore) setState(gatewayID, routeID, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.routes[gatewayID] {
		if m.routes[gatewayID][i].ID == routeID {
			m.routes[gatewayID][i].State = state
		}
	}
}

func (m *memStore) BumpSnapshot(_ context.Context, _, _, digest string) (uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bumps++
	if digest == m.digest {
		return m.version, false, nil
	}
	m.version++
	m.digest = digest
	return m.version, true, nil
}

func (m *memStore) LatestSnapshot(_ context.Context, _, gatewayID string) (uint64, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.interfere > 0 {
		// Another control plane replica bumps with different content.
		m.interfere--
		m.version++
		m.digest = "another-writer"
	}
	if m.version == 0 {
		return 0, "", fmt.Errorf("%w: no snapshot for gateway %s", ErrNotFound, gatewayID)
	}
	return m.version, m.digest, nil
}

func (m *memStore) RaiseSnapshotVersion(_ context.Context, _, gatewayID, digest string, atLeast uint64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.version == 0 || digest != m.digest {
		return 0, fmt.Errorf("%w: no snapshot of gateway %s with that digest", ErrNotFound, gatewayID)
	}
	m.version = max(m.version, atLeast)
	return m.version, nil
}

func (m *memStore) Report(_ context.Context, _, _ string, r plane.Report, _ time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports = append(m.reports, r)
	return m.live, nil
}

// memURLs is a ResourceURLs over a map; err fails every lookup.
type memURLs struct {
	mu   sync.Mutex
	urls map[string]string
	err  error
}

func (u *memURLs) URL(_ context.Context, org, env, resource string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err != nil {
		return "", u.err
	}
	if org != "org_1" || env != "env_1" {
		return "", fmt.Errorf("asked for %s/%s", org, env)
	}
	v, ok := u.urls[resource]
	if !ok {
		return "", ErrNoURL
	}
	return v, nil
}

type serviceHarness struct {
	svc   *Service
	store *memStore
	urls  *memURLs
	gw    Gateway
	keys  gatewayKeys
	now   time.Time
}

func newServiceHarness(t *testing.T) *serviceHarness {
	t.Helper()
	g, keys := testGateway(t, "dbgw_01test")
	st := newMemStore(g)
	st.routes[g.ID] = []RouteRecord{
		{ID: "dbgr_1", Resource: "orders", State: "active", Salt: testSalt},
		{ID: "dbgr_2", Resource: "billing", State: "active", Salt: testSalt},
	}
	urls := &memURLs{urls: map[string]string{
		"orders":  "postgres://app:pw@db.do.example:25060/orders?sslmode=require",
		"billing": "postgres://billing:pw2@db.do.example:25060/billing",
	}}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := plane.TrustKey{PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	h := &serviceHarness{store: st, urls: urls, gw: g, keys: keys, now: now}
	h.svc = newService(st, urls, priv, trust, func() time.Time { return h.now })
	return h
}

// The snapshot this control plane serves verifies and applies in the real
// plane code, after the trip through JSON a gateway makes it take.
func TestSnapshotVerifiesAndAppliesInThePlane(t *testing.T) {
	h := newServiceHarness(t)
	s, err := h.svc.Snapshot(t.Context(), &h.gw, 0)
	if err != nil || s == nil {
		t.Fatalf("Snapshot: %v %v", s, err)
	}
	if s.Version != 1 || !s.IssuedAt.Equal(h.now) || s.GatewayID != h.gw.ID {
		t.Fatalf("snapshot %d at %s for %s", s.Version, s.IssuedAt, s.GatewayID)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var got plane.Snapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := plane.VerifySnapshot(got, h.svc.TrustKeys(), h.gw.ID, h.now); err != nil {
		t.Fatalf("VerifySnapshot: %v", err)
	}
	if got.Routes[0].ID != "dbgr_1" {
		t.Fatalf("routes %+v", got.Routes)
	}
	if user, password := openCredential(t, h.keys.sealing, got.Routes[0]); user != "app" || password != "pw" {
		t.Fatalf("orders credential %q/%q", user, password)
	}

	// The router dials nothing until a client connects.
	r := plane.NewRouter(&plane.Store{}, h.keys.sealing, h.gw.ID, plane.PgDialer{}, nil, plane.Suffix(h.gw.Suffix))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.Drain(ctx)
	})
	if err := r.Apply(got); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	stats := r.Stats()
	if len(stats) != 2 {
		t.Fatalf("router stats %+v", stats)
	}
	for _, st := range stats {
		if !st.Reachable {
			t.Fatalf("route %s is unreachable: its credential didn't open", st.RouteID)
		}
	}

	// A snapshot signed for this gateway isn't trusted for another, or
	// under a key the gateway doesn't hold.
	if err := plane.VerifySnapshot(got, h.svc.TrustKeys(), "dbgw_other", h.now); err == nil {
		t.Fatal("verified for another gateway")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := plane.VerifySnapshot(got, []plane.TrustKey{{PublicKey: other, NotBefore: h.now.Add(-time.Hour), NotAfter: h.now.Add(time.Hour)}}, h.gw.ID, h.now); err == nil {
		t.Fatal("verified under another key")
	}
}

// Sealing is randomised, so the digest must not change with it: polling the
// same state again keeps the version.
func TestSnapshotVersionMovesOnlyOnChange(t *testing.T) {
	h := newServiceHarness(t)
	ctx := t.Context()
	first, err := h.svc.Snapshot(ctx, &h.gw, 0)
	if err != nil || first.Version != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	again, err := h.svc.Snapshot(ctx, &h.gw, 0)
	if err != nil || again.Version != 1 {
		t.Fatalf("same state: %+v %v", again, err)
	}
	none, err := h.svc.Snapshot(ctx, &h.gw, 1)
	if err != nil || none != nil {
		t.Fatalf("nothing newer than 1: %+v %v", none, err)
	}
	h.urls.mu.Lock()
	h.urls.urls["orders"] = "postgres://app:rotated@db.do.example:25060/orders?sslmode=require"
	h.urls.mu.Unlock()
	s, err := h.svc.Snapshot(ctx, &h.gw, 1)
	if err != nil || s == nil || s.Version != 2 {
		t.Fatalf("after a password change: %+v %v", s, err)
	}
	if _, password := openCredential(t, h.keys.sealing, s.Routes[0]); password != "rotated" {
		t.Fatalf("password %q", password)
	}
}

func TestPausedRouteInSnapshot(t *testing.T) {
	h := newServiceHarness(t)
	ctx := t.Context()
	if _, err := h.svc.Snapshot(ctx, &h.gw, 0); err != nil {
		t.Fatal(err)
	}
	h.store.setState(h.gw.ID, "dbgr_1", "paused")
	s, err := h.svc.Snapshot(ctx, &h.gw, 1)
	if err != nil || s == nil || s.Version != 2 {
		t.Fatalf("after pausing: %+v %v", s, err)
	}
	for _, rt := range s.Routes {
		want := plane.RouteStateActive
		if rt.ID == "dbgr_1" {
			want = plane.RouteStatePaused
		}
		if rt.State != want {
			t.Fatalf("route %s is %q, want %q", rt.ID, rt.State, want)
		}
	}
	if err := plane.VerifySnapshot(*s, h.svc.TrustKeys(), h.gw.ID, h.now); err != nil {
		t.Fatal(err)
	}
}

// R71: when another writer's digest is stored, the service compiles again
// rather than signing its content under the other writer's version.
func TestSnapshotRecompilesWhenAnotherWriterWins(t *testing.T) {
	h := newServiceHarness(t)
	h.store.interfere = 1
	s, err := h.svc.Snapshot(t.Context(), &h.gw, 0)
	if err != nil || s == nil {
		t.Fatalf("Snapshot: %+v %v", s, err)
	}
	if s.Version != h.store.version || h.store.digest == "another-writer" || h.store.bumps != 2 {
		t.Fatalf("signed version %d; stored %d %q after %d bumps", s.Version, h.store.version, h.store.digest, h.store.bumps)
	}
	if err := plane.VerifySnapshot(*s, h.svc.TrustKeys(), h.gw.ID, h.now); err != nil {
		t.Fatal(err)
	}

	// A writer that keeps winning gets an error, never a mismatched pair.
	h.store.interfere = 100
	if s, err := h.svc.Snapshot(t.Context(), &h.gw, 0); err == nil {
		t.Fatalf("signed %+v while the stored digest never matched", s)
	}
}

// A Vault failure is an error, not a snapshot that drops the route.
func TestSnapshotFailsWhenAnAddressCantBeRead(t *testing.T) {
	h := newServiceHarness(t)
	h.urls.err = errors.New("vault is down")
	if s, err := h.svc.Snapshot(t.Context(), &h.gw, 0); err == nil {
		t.Fatalf("served %+v without the addresses", s)
	}
	if h.store.bumps != 0 {
		t.Fatalf("bumped the snapshot %d times", h.store.bumps)
	}
}

func TestSnapshotReportsSkippedRoutes(t *testing.T) {
	h := newServiceHarness(t)
	delete(h.urls.urls, "billing")
	var got []Skipped
	h.svc.OnSkip = func(gatewayID string, s []Skipped) {
		if gatewayID != h.gw.ID {
			t.Errorf("skipped for %s", gatewayID)
		}
		got = append(got, s...)
	}
	s, err := h.svc.Snapshot(t.Context(), &h.gw, 0)
	if err != nil || len(s.Routes) != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	if len(got) != 1 || got[0] != (Skipped{Resource: "billing", Reason: "no address stored yet"}) {
		t.Fatalf("skipped %+v", got)
	}
	// Reported when the snapshot changes, not on every poll.
	if _, err := h.svc.Snapshot(t.Context(), &h.gw, 0); err != nil || len(got) != 1 {
		t.Fatalf("reported again on an unchanged snapshot: %+v %v", got, err)
	}
}

func TestReportThroughTheService(t *testing.T) {
	h := newServiceHarness(t)
	h.store.live = 3
	resp, err := h.svc.Report(t.Context(), &h.gw, plane.Report{InstanceID: "inst-1", SnapshotVersion: 4})
	if err != nil || resp.LiveInstances != 3 {
		t.Fatalf("%+v %v", resp, err)
	}
	if len(h.store.reports) != 1 || h.store.reports[0].InstanceID != "inst-1" {
		t.Fatalf("stored %+v", h.store.reports)
	}
}

func TestHoldPhase1(t *testing.T) {
	h := newServiceHarness(t)
	if err := h.svc.Hold(t.Context(), &h.gw, plane.Label{DB: "orders", Branch: "dev"}); !errors.Is(err, ErrNoHoldHandler) {
		t.Fatalf("Hold: %v, want ErrNoHoldHandler", err)
	}
}

func TestServiceWithoutASigningKey(t *testing.T) {
	g, _ := testGateway(t, "dbgw_01test")
	svc := newService(newMemStore(g), &memURLs{}, nil, plane.TrustKey{}, time.Now)
	if svc.Configured() || len(svc.TrustKeys()) != 0 {
		t.Fatalf("configured %v, trust keys %+v", svc.Configured(), svc.TrustKeys())
	}
	if _, err := svc.Snapshot(t.Context(), &g, 0); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Snapshot: %v, want ErrNotConfigured", err)
	}
}

// signed returns a request signed by key as gateway id at signedAt. The
// server sees uri; sign is what the gateway signed when it differs.
func signed(t *testing.T, method, uri string, body []byte, id string, key ed25519.PrivateKey, signedAt time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, uri, bytes.NewReader(body))
	plane.SignRequest(req, body, id, key, signedAt)
	return req
}

func TestVerifyRequest(t *testing.T) {
	h := newServiceHarness(t)
	id := h.gw.ID
	uri := "/api/v1/db-gateways/" + id + "/snapshot?after=3"
	body := []byte(`{"instance_id":"inst-1"}`)
	report := "/api/v1/db-gateways/" + id + "/report"

	got, err := h.svc.VerifyRequest(signed(t, http.MethodGet, uri, nil, id, h.keys.signing, h.now), nil, h.now)
	if err != nil || got.ID != id || got.OrgID != "org_1" {
		t.Fatalf("a valid request: %+v %v", got, err)
	}
	if _, err := h.svc.VerifyRequest(signed(t, http.MethodPost, report, body, id, h.keys.signing, h.now.Add(-300*time.Second)), body, h.now); err != nil {
		t.Fatalf("300s of skew: %v", err)
	}

	_, wrongKey, _ := ed25519.GenerateKey(rand.Reader)
	other, otherKeys := testGateway(t, "dbgw_02other")
	h.store.gateways[other.ID] = other

	// Each failure is a request and the body the server read with it.
	type attempt struct {
		req  *http.Request
		body []byte
	}
	get := func(signer string, key ed25519.PrivateKey, at time.Time) *http.Request {
		return signed(t, http.MethodGet, uri, nil, signer, key, at)
	}
	header := func(req *http.Request, k, v string) *http.Request {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
		return req
	}
	moved := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/db-gateways/"+id+"/snapshot?after=0", nil)
	moved.Header = get(id, h.keys.signing, h.now).Header
	put := signed(t, http.MethodPost, report, body, id, h.keys.signing, h.now)
	put.Method = http.MethodPut
	unknown := signed(t, http.MethodGet, "/api/v1/db-gateways/dbgw_missing/snapshot", nil, "dbgw_missing", h.keys.signing, h.now)
	outside := signed(t, http.MethodGet, "/api/v1/projects/"+id+"/snapshot", nil, id, h.keys.signing, h.now)

	failures := map[string]attempt{
		"301s behind":                      {get(id, h.keys.signing, h.now.Add(-301*time.Second)), nil},
		"301s ahead":                       {get(id, h.keys.signing, h.now.Add(301*time.Second)), nil},
		"the wrong key":                    {get(id, wrongKey, h.now), nil},
		"a tampered body":                  {signed(t, http.MethodPost, report, body, id, h.keys.signing, h.now), []byte(`{"instance_id":"inst-2"}`)},
		"a tampered query":                 {moved, nil},
		"another method":                   {put, body},
		"an unknown gateway":               {unknown, nil},
		"another gateway's id in the path": {get(other.ID, otherKeys.signing, h.now), nil},
		"no signature":                     {header(get(id, h.keys.signing, h.now), "Loomplane-Signature", ""), nil},
		"a signature that isn't base64":    {header(get(id, h.keys.signing, h.now), "Loomplane-Signature", "!!!"), nil},
		"a timestamp that isn't a number":  {header(get(id, h.keys.signing, h.now), "Loomplane-Timestamp", "noon"), nil},
		"no gateway header":                {header(get(id, h.keys.signing, h.now), "Loomplane-Gateway", ""), nil},
		"a path outside the gateway API":   {outside, nil},
	}
	for name, a := range failures {
		t.Run(name, func(t *testing.T) {
			gw, err := h.svc.VerifyRequest(a.req, a.body, h.now)
			if !errors.Is(err, ErrUnauthorized) || gw != nil {
				t.Fatalf("got %+v %v, want ErrUnauthorized", gw, err)
			}
			if err.Error() != ErrUnauthorized.Error() {
				t.Fatalf("error %q says more than the fixed message", err)
			}
		})
	}
}

// countDerivations makes the service's verifier cache count its PBKDF2 runs.
func countDerivations(svc *Service) *int {
	n := new(int)
	svc.verifiers.derive = func(role, password string, salt []byte, iterations int) plane.Verifier {
		*n++
		return plane.NewVerifier(role, password, salt, iterations)
	}
	return n
}

// R74: a poll that finds nothing changed derives no verifier, and a new
// password does.
func TestSnapshotCachesVerifiers(t *testing.T) {
	h := newServiceHarness(t)
	ctx := t.Context()
	derived := countDerivations(h.svc)
	first, err := h.svc.Snapshot(ctx, &h.gw, 0)
	if err != nil || *derived != 2 {
		t.Fatalf("first snapshot derived %d verifiers: %v", *derived, err)
	}
	again, err := h.svc.Snapshot(ctx, &h.gw, 0)
	if err != nil || *derived != 2 {
		t.Fatalf("an unchanged snapshot derived %d verifiers, want still 2: %v", *derived, err)
	}
	if again.Version != first.Version {
		t.Fatalf("cached verifiers moved the version: %d then %d", first.Version, again.Version)
	}
	if err = cleartextLogin(t, again.Routes[0].Verifiers[0], "pw"); err != nil {
		t.Fatalf("a cached verifier doesn't log in: %v", err)
	}

	h.urls.mu.Lock()
	h.urls.urls["orders"] = "postgres://app:rotated@db.do.example:25060/orders?sslmode=require"
	h.urls.mu.Unlock()
	rotated, err := h.svc.Snapshot(ctx, &h.gw, first.Version)
	if err != nil || rotated == nil || *derived != 3 {
		t.Fatalf("a new password derived %d verifiers, want 3: %+v %v", *derived, rotated, err)
	}
	if err = cleartextLogin(t, rotated.Routes[0].Verifiers[0], "rotated"); err != nil {
		t.Fatalf("the new password doesn't log in: %v", err)
	}
	// The old password's entry went with the compile that stopped using it.
	if n := h.svc.verifiers.size(); n != 2 {
		t.Fatalf("cache holds %d verifiers, want 2", n)
	}
	// So does a route that's gone.
	h.store.mu.Lock()
	h.store.routes[h.gw.ID] = h.store.routes[h.gw.ID][:1]
	h.store.mu.Unlock()
	if _, err := h.svc.Snapshot(ctx, &h.gw, 0); err != nil {
		t.Fatal(err)
	}
	if n := h.svc.verifiers.size(); n != 1 {
		t.Fatalf("cache holds %d verifiers after a route went, want 1", n)
	}
}

func TestVerifierCacheIsBounded(t *testing.T) {
	c := newVerifierCache()
	c.derive = func(role, _ string, _ []byte, _ int) plane.Verifier {
		return plane.Verifier{Role: role, StoredKey: []byte{1}}
	}
	fill := func(gatewayID string, n int) {
		verify, done := c.compileFor(gatewayID)
		for i := range n {
			verify(RouteRecord{ID: fmt.Sprintf("%s-r%d", gatewayID, i), Salt: testSalt}, "app", "pw")
		}
		done()
	}
	fill("dbgw_a", 6000)
	fill("dbgw_b", 6000)
	if n := c.size(); n != maxCachedVerifiers {
		t.Fatalf("cache holds %d, want the cap %d", n, maxCachedVerifiers)
	}
	if n := len(c.byGateway["dbgw_b"]); n != 6000 {
		t.Fatalf("the gateway just compiled kept %d, want all 6000", n)
	}
	fill("dbgw_c", maxCachedVerifiers+1)
	if n := len(c.byGateway["dbgw_c"]); n != 0 || c.size() > maxCachedVerifiers {
		t.Fatalf("a gateway over the cap alone kept %d; cache %d", n, c.size())
	}
}

// R75: a gateway ahead of the stored version gets a snapshot past it.
func TestSnapshotRaisesAStaleStoredVersion(t *testing.T) {
	h := newServiceHarness(t)
	ctx := t.Context()
	if s, err := h.svc.Snapshot(ctx, &h.gw, 0); err != nil || s.Version != 1 {
		t.Fatalf("first: %+v %v", s, err)
	}
	s, err := h.svc.Snapshot(ctx, &h.gw, 7)
	if err != nil || s == nil || s.Version != 8 {
		t.Fatalf("a gateway at 7 got %+v %v, want version 8", s, err)
	}
	if err := plane.VerifySnapshot(*s, h.svc.TrustKeys(), h.gw.ID, h.now); err != nil {
		t.Fatal(err)
	}
	if h.store.version != 8 {
		t.Fatalf("stored version %d, want 8", h.store.version)
	}
	if s, err := h.svc.Snapshot(ctx, &h.gw, 8); err != nil || s != nil {
		t.Fatalf("at 8 with nothing new: %+v %v", s, err)
	}
	if _, err := h.svc.Snapshot(ctx, &h.gw, math.MaxInt64); !errors.Is(err, ErrInvalid) {
		t.Fatalf("after past the version range: %v, want ErrInvalid", err)
	}
}

// R1: deployments will import this package, so this package can't import it.
func TestDoesNotImportDeployments(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/deployments") {
			t.Fatalf("internal/dbgateway depends on %s", dep)
		}
	}
}
