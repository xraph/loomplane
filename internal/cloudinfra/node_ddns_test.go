package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
	"github.com/xraph/loomplane/pkg/provider"
)

const (
	ddnsOld = "203.0.113.7"
	ddnsNew = "198.51.100.9"
)

// ddnsLog is the order the follow's effects happened in.
type ddnsLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *ddnsLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *ddnsLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

// index is where the first entry starting with prefix is, or -1.
func (l *ddnsLog) index(prefix string) int {
	return slices.IndexFunc(l.list(), func(e string) bool { return strings.HasPrefix(e, prefix) })
}

// lastIndex is where the last entry starting with prefix is, or -1.
func (l *ddnsLog) lastIndex(prefix string) int {
	entries := l.list()
	for i := len(entries) - 1; i >= 0; i-- {
		if strings.HasPrefix(entries[i], prefix) {
			return i
		}
	}
	return -1
}

// ddnsDomains holds the zones and routes a follow reads, and the operations
// it accepts. busy makes AcceptRoute refuse a route.
type ddnsDomains struct {
	DomainStore
	mu        sync.Mutex
	log       *ddnsLog
	zones     []*Zone
	routes    []*Route
	accepted  []*RouteOperation
	published []Route
	busy      map[string]error
	// busyFor refuses a route's next n operations as busy.
	busyFor map[string]int
}

func (d *ddnsDomains) Zones(_ context.Context, org string) ([]*Zone, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []*Zone{}
	for _, z := range d.zones {
		if z.OrgID == org {
			copied := *z
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (d *ddnsDomains) OrgRoutes(_ context.Context, org string) ([]*Route, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []*Route{}
	for _, r := range d.routes {
		if r.OrgID == org && r.Status != "removed" {
			copied := *r
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (d *ddnsDomains) AcceptRoute(_ context.Context, op *RouteOperation) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.busy[op.Route.ID]; err != nil {
		return err
	}
	if d.busyFor[op.Route.ID] > 0 {
		d.busyFor[op.Route.ID]--
		return ErrBusy
	}
	copied := *op
	d.accepted = append(d.accepted, &copied)
	d.log.add("accept:" + op.Route.ID)
	return nil
}

func (d *ddnsDomains) LatestRouteOperations(_ context.Context, org string, ids []string) (map[string]*RouteOperation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]*RouteOperation{}
	for _, op := range d.accepted {
		if op.Route.OrgID == org && slices.Contains(ids, op.Route.ID) {
			out[op.Route.ID] = op
		}
	}
	return out, nil
}

func (d *ddnsDomains) PublishRoute(_ context.Context, op *RouteOperation) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	op.Route.Receipt, op.Route.OperationID = op.Receipt, op.ID
	op.Route.Revision++
	d.published = append(d.published, op.Route)
	for i, r := range d.routes {
		if r.ID == op.Route.ID {
			copied := op.Route
			d.routes[i] = &copied
		}
	}
	return nil
}

func (d *ddnsDomains) ops() []*RouteOperation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.accepted)
}

func (d *ddnsDomains) op(t *testing.T, route string) *RouteOperation {
	t.Helper()
	for _, op := range d.ops() {
		if op.Route.ID == route {
			return op
		}
	}
	t.Fatalf("no operation for %s in %+v", route, d.ops())
	return nil
}

func (d *ddnsDomains) zone(id string) *Zone {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, z := range d.zones {
		if z.ID == id {
			copied := *z
			return &copied
		}
	}
	return nil
}

func (d *ddnsDomains) lastPublished() Route {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.published[len(d.published)-1]
}

// ddnsNodes is the node store a follow rereads the node from.
type ddnsNodes struct {
	mu    sync.Mutex
	nodes map[string]*nodes.Node
	reads int
}

func (s *ddnsNodes) GetByID(_ context.Context, id string) (*nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	n, ok := s.nodes[id]
	if !ok {
		return nil, nodes.ErrNotFound
	}
	copied := *n
	copied.Inventory.PublicAddresses = slices.Clone(n.Inventory.PublicAddresses)
	return &copied, nil
}

func (s *ddnsNodes) set(fn func(n *nodes.Node)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.nodes {
		fn(n)
	}
}

// ddnsIngress is the nodes service: it logs what it settles, keeps the
// stored node current, and records a detail.
type ddnsIngress struct {
	*fakeNodeIngress
	log     *ddnsLog
	records *ddnsNodes
	mu      sync.Mutex
	details []string
}

func (f *ddnsIngress) SetIngress(ctx context.Context, nodeID, mode, address string) error {
	f.log.add("ingress:" + mode + ":" + address)
	if err := f.fakeNodeIngress.SetIngress(ctx, nodeID, mode, address); err != nil {
		return err
	}
	now := time.Now().UTC()
	f.records.set(func(n *nodes.Node) { n.IngressMode, n.PublicAddress, n.PublicAddressConfirmedAt = mode, address, &now })
	return nil
}

func (f *ddnsIngress) RecordIngressDetail(_ context.Context, nodeID, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.details = append(f.details, nodeID+": "+detail)
	return nil
}

func (f *ddnsIngress) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.details)
}

// ddnsAddresses logs the advertise_host writes.
type ddnsAddresses struct {
	inner NodeAddressWriter
	log   *ddnsLog
}

func (a ddnsAddresses) SetNodeAdvertiseHost(ctx context.Context, org, id, address string) error {
	a.log.add("advertise:" + address)
	return a.inner.SetNodeAdvertiseHost(ctx, org, id, address)
}

// ddnsWorkflow logs the route operations it starts.
type ddnsWorkflow struct {
	*adoptionWorkflow
	log *ddnsLog
	mu  sync.Mutex
	at  map[string]time.Time
}

func (w *ddnsWorkflow) StartRoute(_ context.Context, _, id string, _ int) error {
	w.log.add("enqueue:" + id)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.at == nil {
		w.at = map[string]time.Time{}
	}
	w.at[id] = time.Now()
	return nil
}

func (w *ddnsWorkflow) startedAt(id string) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.at[id]
}

type ddnsRig struct {
	*addressRig
	domains *ddnsDomains
	records *ddnsNodes
	nodeIn  *ddnsIngress
	log     *ddnsLog
	wf      *ddnsWorkflow
	warnMu  sync.Mutex
	warned  []string
}

var ddnsReceipt = provider.DNSRecord{ID: "rec_1", ZoneID: "zone", Name: "app", Type: "A", Value: ddnsOld, TTL: 60}

// newDDNSRig is a node in direct mode at ddnsOld whose inventory now offers
// ddnsNew, with a ready route in a managed zone, one in an external zone,
// and one on another server's connection.
func newDDNSRig(t *testing.T) *ddnsRig {
	t.Helper()
	a := newAddressRig(t)
	log := &ddnsLog{}
	r := &ddnsRig{addressRig: a, log: log}
	if err := a.conns.SetNodeAdvertiseHost(t.Context(), "org", a.conn.ID, ddnsOld); err != nil {
		t.Fatal(err)
	}
	confirmed := time.Now().Add(-time.Hour).UTC()
	stored := *a.node
	stored.IngressMode, stored.PublicAddress, stored.PublicAddressConfirmedAt = nodeproto.IngressDirect, ddnsOld, &confirmed
	stored.Inventory.PublicAddresses = []string{"10.0.0.4", ddnsNew}
	r.records = &ddnsNodes{nodes: map[string]*nodes.Node{stored.ID: &stored}}
	a.node = &stored
	now := a.svc.now()
	binding := RouteBinding{BindingID: "binding_web", ServerID: "srv_1", TargetID: "target", ConnectionID: a.conn.ID, ConnectionGeneration: 1, InstanceID: "instance", ReleaseID: "release", ContainerID: "container", RuntimeID: "daemon-1", Address: ddnsOld}
	verified := RouteVerification{DNS: true, TLS: true, Backend: true, CheckedAt: now.Add(-time.Hour), CertExpires: now.Add(60 * 24 * time.Hour)}
	receipt := ddnsReceipt
	r.domains = &ddnsDomains{log: log, busy: map[string]error{}, busyFor: map[string]int{},
		zones: []*Zone{
			{ID: "zone_m", OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "digitalocean", AccountID: "account", ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com"}, ProjectIDs: []string{"shop"}, VerifiedAt: now, Revision: 1},
			{ID: "zone_x", OrgID: "org", Name: "shop.dev", Mode: "external", ProjectIDs: []string{"shop"}, VerifiedAt: now, Revision: 1},
		},
		routes: []*Route{
			{ID: "route_m", OrgID: "org", ProjectID: "shop", Input: RouteInput{ZoneID: "zone_m", CloudID: "cloud", EnvironmentID: "prod", Service: "web", Hostname: "app.example.com", Port: 8080}, Binding: binding, Status: "ready", OperationID: "routeop_old_m", Receipt: &receipt, Verification: verified, Revision: 3, UpdatedAt: now},
			{ID: "route_x", OrgID: "org", ProjectID: "shop", Input: RouteInput{ZoneID: "zone_x", CloudID: "cloud", EnvironmentID: "prod", Service: "web", Hostname: "api.shop.dev", Port: 8080}, Binding: binding, Status: "ready", OperationID: "routeop_old_x", Verification: verified, Revision: 2, UpdatedAt: now},
			{ID: "route_other", OrgID: "org", ProjectID: "shop", Input: RouteInput{ZoneID: "zone_m", CloudID: "cloud", EnvironmentID: "prod", Service: "api", Hostname: "other.example.com", Port: 8080}, Binding: RouteBinding{ServerID: "srv_2", ConnectionID: "conn_ssh", Address: "192.0.2.50", RuntimeID: "daemon-2"}, Status: "ready", Receipt: &provider.DNSRecord{ID: "rec_2", ZoneID: "zone", Name: "other", Type: "A", Value: "192.0.2.50", TTL: 60}, Verification: verified, Revision: 1, UpdatedAt: now},
		},
	}
	r.nodeIn = &ddnsIngress{fakeNodeIngress: a.ingress, log: log, records: r.records}
	a.svc.Domains = r.domains
	a.svc.NodeRecords = r.records
	a.svc.NodeIngress = r.nodeIn
	a.svc.NodeAddresses = ddnsAddresses{inner: a.conns, log: log}
	r.wf = &ddnsWorkflow{adoptionWorkflow: a.workflow, log: log}
	a.svc.Workflow = r.wf
	a.svc.serverReadyPoll = time.Millisecond
	a.svc.Warn = func(msg string, err error) {
		r.warnMu.Lock()
		defer r.warnMu.Unlock()
		r.warned = append(r.warned, msg+": "+err.Error())
	}
	return r
}

func (r *ddnsRig) follow(t *testing.T) {
	t.Helper()
	if err := r.svc.FollowNodeAddress(t.Context(), r.node); err != nil {
		t.Fatalf("follow: %v", err)
	}
}

func TestManagedZoneRecordFollows(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.follow(t)

	op := r.domains.op(t, "route_m")
	if op.Kind != "address" || !op.Automatic || op.Status != "pending" || op.Attempt != 1 || op.Review != "" {
		t.Fatalf("operation %+v", op)
	}
	// Only the binding's address moves; the rest of the route is as it was.
	want := *r.domains.routes[0]
	want.Binding.Address = ddnsNew
	if !reflect.DeepEqual(op.Route, want) {
		t.Fatalf("route\n got %+v\nwant %+v", op.Route, want)
	}
	record := ddnsReceipt
	record.Value = ddnsNew
	if op.DesiredRecord != record || op.PreviousReceipt == nil || *op.PreviousReceipt != ddnsReceipt || op.Receipt != nil {
		t.Fatalf("desired %+v previous %+v receipt %+v", op.DesiredRecord, op.PreviousReceipt, op.Receipt)
	}
	if op.Message != "address changed to "+ddnsNew+", DNS updating" {
		t.Fatalf("message %q", op.Message)
	}
	if op.DesiredConfig != nil || op.PreviousConfig != nil || op.AppliedConfig != nil {
		t.Fatal("an address change touched the proxy config")
	}
	for _, o := range r.domains.ops() {
		if o.Route.ID == "route_other" {
			t.Fatal("a route on another server followed the node")
		}
	}

	// The node's own address goes last: after every operation is created,
	// before any of them runs.
	accepted, advertised, settled, enqueued := r.log.lastIndex("accept:"), r.log.index("advertise:"), r.log.index("ingress:"), r.log.index("enqueue:")
	if accepted < 0 || accepted > advertised || advertised > settled || settled > enqueued {
		t.Fatalf("order %v", r.log.list())
	}
	if a := r.advertised(t); a != ddnsNew {
		t.Fatalf("advertise_host %q", a)
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressDirect, ddnsNew}}) {
		t.Fatalf("ingress %+v", c)
	}

	// Running it changes the Loomplane-owned record, and only that one.
	adapter := &routeDNSAdapter{records: []provider.DNSRecord{
		{ID: "proof", ZoneID: "zone", Name: "_loomplane", Type: "TXT", Value: "proof", TTL: 60},
		{ID: "www", ZoneID: "zone", Name: "www", Type: "A", Value: "192.0.2.80", TTL: 300},
		ddnsReceipt,
	}}
	registry := NewAdapters()
	if err := registry.RegisterDNS("digitalocean", adapter); err != nil {
		t.Fatal(err)
	}
	dns := NewService(Deps{Adapters: registry, Connections: routeDNSCredentials{"digitalocean"}})
	if err := dns.applyRouteDNS(t.Context(), &routeDNSStore{}, op, r.domains.zone("zone_m")); err != nil {
		t.Fatal(err)
	}
	if adapter.writes != 1 || adapter.last.Expected == nil || *adapter.last.Expected != ddnsReceipt {
		t.Fatalf("writes %d, last %+v", adapter.writes, adapter.last)
	}
	for _, rec := range adapter.records {
		switch rec.Name {
		case "_loomplane":
			if rec.Value != "proof" {
				t.Fatalf("verification record changed: %+v", rec)
			}
		case "www":
			if rec.Value != "192.0.2.80" {
				t.Fatalf("another record changed: %+v", rec)
			}
		case "app":
			if rec.Type != "A" || rec.Value != ddnsNew {
				t.Fatalf("route record %+v", rec)
			}
		}
	}
}

func TestManagedZoneRefusesToSwapRecordType(t *testing.T) {
	r := newDDNSRig(t)
	r.records.set(func(n *nodes.Node) { n.Inventory.PublicAddresses = []string{"2001:db8::9"} })
	r.answering["2001:db8::9"] = true
	r.follow(t)
	op := r.domains.op(t, "route_m")
	want := "address changed to 2001:db8::9, and Loomplane doesn't turn the A record for app.example.com into an AAAA record by itself; remove the route and add it again"
	if op.Message != want {
		t.Fatalf("message %q", op.Message)
	}
	err := checkAddressRecord(op, r.domains.zone("zone_m"))
	var notice *routeNoticeError
	if !errors.Is(err, ErrConflict) || !errors.As(err, &notice) || notice.message != want {
		t.Fatalf("record check %v", err)
	}
	same := r.domains.op(t, "route_x")
	if checkErr := checkAddressRecord(same, r.domains.zone("zone_x")); checkErr != nil {
		t.Fatalf("external zone %v", checkErr)
	}
}

func TestExternalZoneShowsRecordToSet(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.follow(t)
	op := r.domains.op(t, "route_x")
	if op.Kind != "address" || !op.Automatic || op.Route.Binding.Address != ddnsNew || op.PreviousReceipt != nil || op.Receipt != nil {
		t.Fatalf("operation %+v", op)
	}
	want := "set the A record for api.shop.dev to " + ddnsNew
	if op.Message != want {
		t.Fatalf("message %q", op.Message)
	}
	zone := r.domains.zone("zone_x")
	before := r.domains.routes[1].Verification

	// Until the name resolves to the new address, the route waits for it
	// and keeps its last check.
	err := r.svc.settleRouteAddress(t.Context(), r.domains, op, zone, RouteVerification{TLS: true, Backend: true, DNSError: "DNS does not resolve exclusively to the accepted server address."})
	var notice *routeNoticeError
	if !errors.Is(err, ErrPending) || !errors.As(err, &notice) || notice.message != want {
		t.Fatalf("settle %v", err)
	}
	got := r.domains.lastPublished()
	if got.Status != RouteRoutingPending || got.Verification != before {
		t.Fatalf("published %+v", got)
	}
	// A second wait publishes nothing new.
	count := len(r.domains.published)
	if err = r.svc.settleRouteAddress(t.Context(), r.domains, op, zone, RouteVerification{}); !errors.Is(err, ErrPending) || len(r.domains.published) != count {
		t.Fatalf("second wait %v, %d publishes", err, len(r.domains.published))
	}
	// Once it does, the route is ready on the new address.
	seen := RouteVerification{DNS: true, TLS: true, Backend: true, CheckedAt: time.Now().UTC()}
	if err = r.svc.settleRouteAddress(t.Context(), r.domains, op, zone, seen); err != nil {
		t.Fatal(err)
	}
	got = r.domains.lastPublished()
	if got.Status != "ready" || got.Verification != seen || got.Binding.Address != ddnsNew {
		t.Fatalf("published %+v", got)
	}
}

func TestExternalZoneShowsAnAAAARecordForIPv6(t *testing.T) {
	r := newDDNSRig(t)
	r.records.set(func(n *nodes.Node) { n.Inventory.PublicAddresses = []string{"2001:db8::9"} })
	r.answering["2001:db8::9"] = true
	r.follow(t)
	if m := r.domains.op(t, "route_x").Message; m != "set the AAAA record for api.shop.dev to 2001:db8::9 and remove its A record" {
		t.Fatalf("message %q", m)
	}
	// From one IPv6 address to another, only the AAAA record changes.
	r2 := newDDNSRig(t)
	r2.records.set(func(n *nodes.Node) {
		n.PublicAddress, n.Inventory.PublicAddresses = "2001:db8::1", []string{"2001:db8::9"}
	})
	r2.domains.routes[1].Binding.Address = "2001:db8::1"
	r2.answering["2001:db8::9"] = true
	r2.follow(t)
	if m := r2.domains.op(t, "route_x").Message; m != "set the AAAA record for api.shop.dev to 2001:db8::9" {
		t.Fatalf("message %q", m)
	}
}

func TestUnconfirmedAddressChangesNothing(t *testing.T) {
	r := newDDNSRig(t)
	r.follow(t)
	if f := r.fetches(); !slices.Equal(f, []string{ddnsNew}) {
		t.Fatalf("fetched %v", f)
	}
	if len(r.proxy.removed) != 1 {
		t.Fatalf("temporary route left on the proxy: %v", r.proxy.removed)
	}
	if ops := r.domains.ops(); len(ops) != 0 {
		t.Fatalf("operations %+v", ops)
	}
	if a := r.advertised(t); a != ddnsOld {
		t.Fatalf("advertise_host %q", a)
	}
	if c := r.ingress.list(); len(c) != 0 {
		t.Fatalf("ingress %+v", c)
	}
	if d := r.nodeIn.recorded(); !slices.Equal(d, []string{"node_1: address changed to " + ddnsNew + " but it doesn't answer on port 80"}) {
		t.Fatalf("details %v", d)
	}

	// A check that can't run says nothing about the address, and still
	// changes nothing.
	r2 := newDDNSRig(t)
	r2.answering[ddnsNew] = true
	r2.proxy.err = errors.New("proxy busy")
	if err := r2.svc.FollowNodeAddress(t.Context(), r2.node); err == nil {
		t.Fatal("a failed check passed")
	}
	if len(r2.domains.ops()) != 0 || r2.advertised(t) != ddnsOld || len(r2.ingress.list()) != 0 || len(r2.nodeIn.recorded()) != 0 {
		t.Fatalf("changed: ops %v ingress %v details %v", r2.domains.ops(), r2.ingress.list(), r2.nodeIn.recorded())
	}
}

func TestDDNSActorIsAutomation(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.follow(t)
	system := security.SystemPrincipal("org")
	ops := r.domains.ops()
	if len(ops) != 2 {
		t.Fatalf("operations %+v", ops)
	}
	for _, op := range ops {
		if op.Principal != system || op.ExecutionPrincipal == nil || *op.ExecutionPrincipal != system || !op.Automatic {
			t.Fatalf("operation %s by %+v / %+v", op.ID, op.Principal, op.ExecutionPrincipal)
		}
	}
	var audited int
	for _, e := range r.audit.events {
		if e.Action != "domain.route.address" {
			continue
		}
		audited++
		if e.Principal != system || e.Attributes["automatic"] != true || e.Attributes["via"] != "node_address" || e.Attributes["node_id"] != "node_1" || e.Attributes["address"] != ddnsNew || e.Attributes["previous_address"] != ddnsOld {
			t.Fatalf("audit %+v", e)
		}
	}
	if audited != 2 {
		t.Fatalf("audited %d of 2: %v", audited, r.audit.actions())
	}
}

func TestRenewalUnaffected(t *testing.T) {
	r := newDDNSRig(t)
	renewing := RouteVerification{DNS: true, TLS: true, Backend: true, CertState: "issuing", CheckedAt: time.Now().Add(-time.Minute).UTC(), CertExpires: time.Now().Add(3 * 24 * time.Hour).UTC()}
	r.domains.routes[0].Verification = renewing
	r.answering[ddnsNew] = true
	r.follow(t)
	op := r.domains.op(t, "route_m")
	if op.Route.Verification != renewing || op.DesiredConfig != nil {
		t.Fatalf("operation %+v", op)
	}
	// DNS hasn't moved yet: the certificate's state is left as it was.
	err := r.svc.settleRouteAddress(t.Context(), r.domains, op, r.domains.zone("zone_m"), RouteVerification{TLS: true, Backend: true})
	if !errors.Is(err, ErrPending) {
		t.Fatalf("settle %v", err)
	}
	if got := r.domains.lastPublished(); got.Verification != renewing || got.Status != RouteRoutingPending {
		t.Fatalf("published %+v", got)
	}
}

func TestDDNSFollowsOneChangeAtATime(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	// Another follow holds the node.
	r.store.mu.Lock()
	r.store.locks["node-address/node_1"] = true
	r.store.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := r.svc.FollowNodeAddress(ctx, r.node); err == nil {
		t.Fatal("followed while another follow held the node")
	}
	if len(r.fetches()) != 0 || len(r.domains.ops()) != 0 {
		t.Fatal("checked while another follow held the node")
	}
	r.store.mu.Lock()
	delete(r.store.locks, "node-address/node_1")
	r.store.mu.Unlock()

	// The follow works from the stored node, not the hook's snapshot.
	stale := *r.node
	stale.Inventory.PublicAddresses = []string{ddnsOld}
	if err := r.svc.FollowNodeAddress(t.Context(), &stale); err != nil {
		t.Fatal(err)
	}
	if len(r.domains.ops()) != 2 || r.advertised(t) != ddnsNew {
		t.Fatalf("ops %d advertise %q", len(r.domains.ops()), r.advertised(t))
	}
	// A second hook for the same change finds the node already there.
	if err := r.svc.FollowNodeAddress(t.Context(), &stale); err != nil {
		t.Fatal(err)
	}
	if len(r.domains.ops()) != 2 || len(r.fetches()) != 1 || len(r.ingress.list()) != 1 {
		t.Fatalf("followed twice: ops %d fetches %v ingress %v", len(r.domains.ops()), r.fetches(), r.ingress.list())
	}
}

// A node its owner put in mode none after its address was confirmed
// stays there. One that never had an address confirmed, because its first
// check found nothing or it was adopted by review, has it checked.
func TestDDNSLeavesANodeWithoutDirectIngress(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.records.set(func(n *nodes.Node) { n.IngressMode = nodeproto.IngressNone })
	r.follow(t)
	if len(r.fetches()) != 0 || len(r.domains.ops()) != 0 || len(r.ingress.list()) != 0 {
		t.Fatal("followed a node its owner took out of direct ingress")
	}

	r = newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.records.set(func(n *nodes.Node) {
		n.IngressMode, n.PublicAddress, n.PublicAddressConfirmedAt = nodeproto.IngressNone, "", nil
	})
	r.follow(t)
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressDirect, ddnsNew}}) || r.advertised(t) != ddnsNew {
		t.Fatalf("an unconfirmed node wasn't confirmed: %+v, advertise %q", c, r.advertised(t))
	}
	// Its routes are a reconcile's to move; the check moves none.
	if len(r.domains.ops()) != 0 {
		t.Fatalf("operations %+v", r.domains.ops())
	}

	// An unconfirmed node its owner put in mode none, say to stop a check,
	// stays there: no follow or reconcile checks it, after the window too.
	r = newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.records.set(func(n *nodes.Node) {
		n.IngressMode, n.PublicAddress, n.PublicAddressConfirmedAt, n.IngressModeByOwner = nodeproto.IngressNone, "", nil, true
	})
	r.follow(t)
	r.svc.Store = &behindStore{adoptionStore: r.store, behind: []*nodes.Node{{ID: "node_1", OrgID: "org"}}}
	later := time.Now().Add(2 * nodeAddressRecheckEvery)
	r.svc.now = func() time.Time { return later }
	if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(r.fetches()) != 0 || len(r.ingress.list()) != 0 || len(r.proxy.served) != 0 {
		t.Fatalf("checked a node its owner put in mode none: fetched %v ingress %+v", r.fetches(), r.ingress.list())
	}
	// Asking for direct again has it checked.
	r.records.set(func(n *nodes.Node) { n.IngressMode = nodeproto.IngressDirect })
	if err := r.svc.CheckNodeAddress(t.Context(), r.node); err != nil {
		t.Fatal(err)
	}
	if c := r.ingress.list(); !slices.Equal(c, []ingressCall{{"node_1", nodeproto.IngressDirect, ddnsNew}}) {
		t.Fatalf("direct again: %+v", c)
	}
}

func TestDDNSHoldsABusyRouteUntilItsDeadline(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.domains.busy["route_x"] = ErrBusy
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	began := time.Now()
	if err := r.svc.FollowNodeAddress(ctx, r.node); err != nil {
		t.Fatal(err)
	}
	took := time.Since(began)
	ops := r.domains.ops()
	if len(ops) != 1 || ops[0].Route.ID != "route_m" {
		t.Fatalf("operations %+v", ops)
	}
	// The node settles and the free route starts at once; only the held
	// one is offered until the deadline.
	started := r.wf.startedAt(ops[0].ID)
	if r.advertised(t) != ddnsNew || len(r.ingress.list()) != 1 || started.IsZero() || started.Sub(began) > 100*time.Millisecond || took < 250*time.Millisecond {
		t.Fatalf("settled %v after, follow took %v: %v", started.Sub(began), took, r.log.list())
	}
	want := "node_1: route api.shop.dev stays on " + ddnsOld + "; another change held it"
	if d := r.nodeIn.recorded(); !slices.Equal(d, []string{want}) {
		t.Fatalf("details %v", d)
	}
	if len(r.warned) != 1 || !strings.Contains(r.warned[0], "api.shop.dev") || !slices.Contains(r.audit.actions(), "domain.route.address_held") {
		t.Fatalf("warned %v, audit %v", r.warned, r.audit.actions())
	}
}

func TestDDNSWaitsForARunningChange(t *testing.T) {
	// A to B is still running on route_x when B to C arrives: the follow
	// offers it again until that run lets go, and nothing is held.
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.domains.busyFor["route_x"] = 3
	r.follow(t)
	if len(r.domains.ops()) != 2 || r.domains.busyFor["route_x"] != 0 || len(r.nodeIn.recorded()) != 0 || len(r.warned) != 0 {
		t.Fatalf("ops %d busy %d details %v warned %v", len(r.domains.ops()), r.domains.busyFor["route_x"], r.nodeIn.recorded(), r.warned)
	}
	// route_m started with the settle; route_x's operation, taken later,
	// started as soon as it was taken.
	late := r.domains.op(t, "route_x").ID
	if r.log.index("ingress:") > r.log.index("enqueue:"+r.domains.op(t, "route_m").ID) || r.log.index("accept:route_x") < r.log.index("ingress:") || r.log.index("enqueue:"+late) < r.log.index("accept:route_x") {
		t.Fatalf("order %v", r.log.list())
	}
}

// addressRunStore is the route store one address operation runs against.
type addressRunStore struct {
	DomainStore
	mu         sync.Mutex
	op         RouteOperation
	route      Route
	zone       *Zone
	binding    RouteBinding
	resolveErr error
	publishes  int
}

func (s *addressRunStore) RouteOperation(context.Context, string, string) (*RouteOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.op
	return &op, nil
}

func (s *addressRunStore) SaveRouteOperation(_ context.Context, op *RouteOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.op = *op
	return nil
}

func (s *addressRunStore) BeginIngress(_ context.Context, op *RouteOperation) (*IngressState, error) {
	op.Generation = 7
	if err := s.SaveRouteOperation(context.Background(), op); err != nil {
		return nil, err
	}
	return &IngressState{Generation: 7, Current: &server.IngressSnapshot{ContainerID: "proxy", ETag: "1"}}, nil
}

func (*addressRunStore) GuardRouteOwnership(context.Context, Route) error { return nil }

func (s *addressRunStore) ResolveRouteBinding(context.Context, string, string, RouteInput) (RouteBinding, error) {
	return s.binding, s.resolveErr
}

func (s *addressRunStore) Zones(context.Context, string) ([]*Zone, error) {
	copied := *s.zone
	return []*Zone{&copied}, nil
}

func (s *addressRunStore) PublishRoute(_ context.Context, op *RouteOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op.Route.Receipt, op.Route.OperationID = op.Receipt, op.ID
	op.Route.Revision++
	s.route, s.publishes = op.Route, s.publishes+1
	s.op = *op
	return nil
}

func TestDDNSOperationRunBeforeSettleWaits(t *testing.T) {
	now := time.Now().UTC()
	zone := &Zone{ID: "zone_m", OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "digitalocean", AccountID: "account",
		ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com"}, ProjectIDs: []string{"shop"}, ProofName: "_loomplane.example.com", ProofValue: "proof", ProofExpiresAt: now.Add(time.Hour), VerifiedAt: now, Revision: 1}
	old := RouteBinding{BindingID: "binding_web", ServerID: "srv_1", ConnectionID: "conn_node_1", ConnectionGeneration: 1, InstanceID: "instance", ReleaseID: "release", ContainerID: "container", RuntimeID: "daemon-1", Address: ddnsOld}
	route := Route{ID: "route_m", OrgID: "org", ProjectID: "shop", Input: RouteInput{ZoneID: "zone_m", CloudID: "cloud", EnvironmentID: "prod", Service: "web", Hostname: "app.example.com", Port: 8080},
		Binding: old, Status: "ready", Receipt: &ddnsReceipt, Revision: 3}
	moved := route
	moved.Binding.Address = ddnsNew
	record := ddnsReceipt
	record.Value = ddnsNew
	system := security.SystemPrincipal("org")
	op := RouteOperation{ID: "routeop_m", Kind: "address", Automatic: true, Status: "pending", Attempt: 1, Revision: 1, Route: moved, PreviousAddress: ddnsOld,
		DesiredRecord: record, PreviousReceipt: &ddnsReceipt, Principal: system, ExecutionPrincipal: &system, Message: "address changed to " + ddnsNew + ", DNS updating"}
	st := &addressRunStore{op: op, route: route, zone: zone, binding: old}
	adapter := &routeDNSAdapter{records: []provider.DNSRecord{ddnsReceipt}}
	registry := NewAdapters()
	if err := registry.RegisterDNS("digitalocean", adapter); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{Domains: st, Store: newAdoptionStore(), Authz: adoptionAuthz{}, Adapters: registry, Connections: routeDNSCredentials{"digitalocean"}, Resolver: proofResolver{[]string{"proof"}}})
	var probes int
	var seen RouteVerification
	svc.routeAddressProbe = func(_ context.Context, op *RouteOperation, state *IngressState) (func(context.Context, Route) RouteVerification, func(), error) {
		if op.Generation != 7 || state.Current == nil {
			t.Errorf("probe of %+v / %+v", op, state)
		}
		probes++
		return func(context.Context, Route) RouteVerification { return seen }, func() {}, nil
	}
	run := func() error {
		t.Helper()
		return svc.ReconcileRoute(t.Context(), "org", "routeop_m", nil)
	}
	waiting := "Waiting for the node to settle on " + ddnsNew + "."

	// Recovery runs it before the node settled: the binding still names
	// the old address, and then none while the node's two writes land.
	for _, c := range []struct {
		name string
		err  error
	}{{"old address", nil}, {"between the writes", errUnconfirmedNodeAddress}} {
		st.resolveErr = c.err
		if err := run(); !errors.Is(err, ErrPending) {
			t.Fatalf("%s: %v", c.name, err)
		}
		if st.op.Status != "waiting" || st.op.Message != waiting || adapter.writes != 0 || probes != 0 || st.publishes != 0 {
			t.Fatalf("%s: op %s %q, writes %d, probes %d", c.name, st.op.Status, st.op.Message, adapter.writes, probes)
		}
	}
	// A binding that moved some other way is a conflict, as before.
	st.resolveErr = nil
	other := old
	other.ReleaseID = "release_2"
	st.binding = other
	if err := run(); err != nil || st.op.Status != "failed" || adapter.writes != 0 {
		t.Fatalf("other change: %v, op %s", err, st.op.Status)
	}

	// Settled: the record moves, and the route waits for DNS.
	op.Status = "waiting"
	defer func() {
		// Once its node leaves direct ingress, a waiting change is
		// cancelled, which frees the route.
		st.op, st.binding, st.resolveErr = op, RouteBinding{}, errUnconfirmedNodeAddress
		svc.Connections = nodeConnCredentials{routeDNSCredentials{"digitalocean"}}
		svc.NodeRecords = &ddnsNodes{nodes: map[string]*nodes.Node{}}
		if err := run(); err != nil || st.op.Status != "cancelled" || !strings.Contains(st.op.Message, "stays where it is") {
			t.Fatalf("left node: %v, op %s %q", err, st.op.Status, st.op.Message)
		}
	}()
	st.op, st.binding = op, moved.Binding
	if err := run(); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if adapter.writes != 1 || adapter.records[0].Value != ddnsNew || probes != 1 || st.route.Status != RouteRoutingPending || st.op.Status != "waiting" || st.op.Message != op.Message {
		t.Fatalf("writes %d records %+v probes %d route %s op %s %q", adapter.writes, adapter.records, probes, st.route.Status, st.op.Status, st.op.Message)
	}
	// Seen on the new address: ready, and the record isn't written again.
	seen = RouteVerification{DNS: true, TLS: true, Backend: true, CheckedAt: now}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if adapter.writes != 1 || st.route.Status != "ready" || st.route.Verification != seen || st.route.Binding.Address != ddnsNew || st.op.Status != "succeeded" {
		t.Fatalf("writes %d route %+v op %s", adapter.writes, st.route, st.op.Status)
	}
}

// behindStore lists the nodes a reconcile looks at, and records where
// each pass started.
type behindStore struct {
	*adoptionStore
	behind []*nodes.Node
	afters []string
}

func (s *behindStore) NodesBehindAddress(_ context.Context, after string, limit int) ([]*nodes.Node, error) {
	s.afters = append(s.afters, after)
	out := []*nodes.Node{}
	for _, n := range s.behind {
		if n.ID > after && len(out) < limit {
			out = append(out, n)
		}
	}
	return out, nil
}

func TestDDNSSettleFailureLeavesTheConnectionAsItWas(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.ingress.err = errors.New("database went away")
	if err := r.svc.FollowNodeAddress(t.Context(), r.node); err == nil {
		t.Fatal("a failed settle passed")
	}
	// The connection names the address the node still holds, so routes
	// keep reaching it there, and nothing started.
	if a := r.advertised(t); a != ddnsOld {
		t.Fatalf("advertise_host %q", a)
	}
	if r.log.index("enqueue:") >= 0 || len(r.domains.ops()) != 2 {
		t.Fatalf("log %v", r.log.list())
	}

	// The reconcile finishes it, reusing the operations already stored.
	r.ingress.err = nil
	r.svc.Store = &behindStore{adoptionStore: r.store, behind: []*nodes.Node{{ID: "node_1", OrgID: "org"}}}
	if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.advertised(t) != ddnsNew || len(r.domains.ops()) != 2 || len(r.ingress.list()) != 2 {
		t.Fatalf("advertise %q ops %d ingress %v", r.advertised(t), len(r.domains.ops()), r.ingress.list())
	}
	// A node that is consistent costs no check.
	fetched := len(r.fetches())
	if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(r.fetches()) != fetched || len(r.ingress.list()) != 2 || len(r.domains.ops()) != 2 {
		t.Fatal("a consistent node was followed again")
	}
}

func TestDDNSReconcileMovesALeftBehindRoute(t *testing.T) {
	r := newDDNSRig(t)
	// The node settled on the new address, but route_x is still on the old.
	r.records.set(func(n *nodes.Node) { n.PublicAddress = ddnsNew })
	if err := r.conns.SetNodeAdvertiseHost(t.Context(), "org", r.conn.ID, ddnsNew); err != nil {
		t.Fatal(err)
	}
	r.records.set(func(n *nodes.Node) { n.IngressDetail = HeldRouteDetail("api.shop.dev", ddnsOld) })
	r.svc.Store = &behindStore{adoptionStore: r.store, behind: []*nodes.Node{{ID: "node_1", OrgID: "org"}}}
	if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
		t.Fatal(err)
	}
	// No check: the address is the node's confirmed one already.
	if len(r.fetches()) != 0 || len(r.ingress.list()) != 0 {
		t.Fatalf("checked again: %v %v", r.fetches(), r.ingress.list())
	}
	if len(r.domains.ops()) != 2 || r.domains.op(t, "route_x").Route.Binding.Address != ddnsNew || r.log.index("enqueue:") < 0 {
		t.Fatalf("ops %+v log %v", r.domains.ops(), r.log.list())
	}
	// What the node said about the held route is cleared.
	if d := r.nodeIn.recorded(); !slices.Equal(d, []string{"node_1: "}) {
		t.Fatalf("details %v", d)
	}
}

func TestDDNSReconcileChecksAnUnansweredAddressSeldom(t *testing.T) {
	r := newDDNSRig(t)
	r.svc.Store = &behindStore{adoptionStore: r.store, behind: []*nodes.Node{{ID: "node_1", OrgID: "org"}}}
	for range 3 {
		if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if f := r.fetches(); len(f) != 1 {
		t.Fatalf("fetched %v", f)
	}
	// Nor does the hook, for the same addresses: a node whose addresses
	// flap can't have its proxy changed over and over.
	r.follow(t)
	if f := r.fetches(); len(f) != 1 {
		t.Fatalf("fetched %v", f)
	}
	// A new address is checked as soon as it is offered, but a node gets
	// only so many checks in a window, whatever it offers.
	for i := range nodeChecksPerWindow + 2 {
		r.records.set(func(n *nodes.Node) { n.Inventory.PublicAddresses = []string{fmt.Sprintf("198.51.100.%d", 20+i)} })
		r.follow(t)
	}
	if f := r.fetches(); len(f) != nodeChecksPerWindow {
		t.Fatalf("fetched %v", f)
	}
	// After the window, each is checked again.
	now := time.Now().Add(nodeAddressRecheckEvery)
	r.svc.now = func() time.Time { return now }
	r.follow(t)
	if f := r.fetches(); len(f) != nodeChecksPerWindow+1 {
		t.Fatalf("fetched %v", f)
	}
}

func TestDDNSHandoffProceedsOnARoutingPendingRoute(t *testing.T) {
	svc, st, _ := subdomainFixture()
	in := subdomainInput()
	in.CloudID = "cloud_1"
	st.routes = append(st.routes, &Route{ID: "route_app", OrgID: "org", ProjectID: "shop", Status: RouteRoutingPending, Revision: 1, Input: in,
		Binding: RouteBinding{BindingID: "binding_old", ServerID: "srv_old", Address: "192.0.2.9"}})
	svc.Resolver = proofResolver{[]string{"proof"}}
	op, err := svc.PrepareRouteHandoff(t.Context(), overviewDev, "shop", "route_app", HandoffInput{Revision: 1, DestinationCloudID: "cloud_1"})
	if err != nil || op.Kind != "prepare" {
		t.Fatalf("handoff on a routing pending route: %+v %v", op, err)
	}
}

func TestDDNSReportsAHeldRouteOnce(t *testing.T) {
	r := newDDNSRig(t)
	// The node settled, but route_x was held, and is still.
	r.records.set(func(n *nodes.Node) { n.PublicAddress = ddnsNew })
	if err := r.conns.SetNodeAdvertiseHost(t.Context(), "org", r.conn.ID, ddnsNew); err != nil {
		t.Fatal(err)
	}
	r.domains.busy["route_x"] = ErrBusy
	r.domains.busy["route_m"] = ErrBusy
	r.svc.Store = &behindStore{adoptionStore: r.store, behind: []*nodes.Node{{ID: "node_1", OrgID: "org"}}}
	for range 3 {
		began := time.Now()
		if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
			t.Fatal(err)
		}
		// A pass doesn't wait for held routes.
		if took := time.Since(began); took > time.Second {
			t.Fatalf("a pass took %v", took)
		}
		// The node's detail keeps naming them, as the stored node would.
		if d := r.nodeIn.recorded(); len(d) > 0 {
			r.records.set(func(n *nodes.Node) { n.IngressDetail = strings.TrimPrefix(d[len(d)-1], "node_1: ") })
		}
	}
	var held int
	for _, a := range r.audit.actions() {
		if a == "domain.route.address_held" {
			held++
		}
	}
	if held != 2 || len(r.warned) != 2 || len(r.nodeIn.recorded()) != 1 {
		t.Fatalf("held audits %d, warned %v, details %v", held, r.warned, r.nodeIn.recorded())
	}
}

func TestDDNSLeavesAFailedOperationToAPerson(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	// route_m's move to the new address failed; route_x's last check failed.
	moved := *r.domains.routes[0]
	moved.Binding.Address = ddnsNew
	r.domains.accepted = append(r.domains.accepted,
		&RouteOperation{ID: "routeop_failed_m", Kind: "address", Status: "failed", Route: moved},
		&RouteOperation{ID: "routeop_failed_x", Kind: "verify", Status: "failed", Route: *r.domains.routes[1]})
	r.follow(t)
	ops := r.domains.ops()
	if len(ops) != 3 || ops[2].Route.ID != "route_x" || ops[2].Kind != "address" {
		t.Fatalf("operations %+v", ops)
	}
	want := "node_1: " + StuckRouteDetail("app.example.com", ddnsOld, "its move there failed; retry it, or remove the route and add it again")
	if d := r.nodeIn.recorded(); !slices.Equal(d, []string{want}) {
		t.Fatalf("details %v", d)
	}
}

func TestDDNSReportsAnOperationItCantMakeWithItsReason(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.domains.routes[0].Receipt = nil
	r.follow(t)
	d := r.nodeIn.recorded()
	if len(d) != 1 || !strings.Contains(d[0], "route app.example.com stays on "+ddnsOld+"; ") || !strings.Contains(d[0], "Loomplane has no record of the DNS record it wrote for app.example.com") || strings.Contains(d[0], "held") {
		t.Fatalf("details %v", d)
	}
}

// A store's or provider's error behind a stuck route is shown in the
// words every other failure uses, not as it came.
func TestDDNSStuckRouteReasonIsSafe(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.domains.busy["route_x"] = errors.New("dial tcp 10.0.0.9:5432: password authentication failed for user loomplane")
	r.follow(t)
	d := r.nodeIn.recorded()
	if len(d) != 1 || !strings.Contains(d[0], "route api.shop.dev stays on "+ddnsOld+"; "+safeFailure(errors.New("x"))) || strings.Contains(d[0], "10.0.0.9") || strings.Contains(d[0], "password") {
		t.Fatalf("details %v", d)
	}
}

func TestDDNSReconcileMovesAStrandedRoutingPendingRoute(t *testing.T) {
	r := newDDNSRig(t)
	// A handoff cancelled route_x's address change after it published
	// routing pending on the new address, and never cut over.
	r.records.set(func(n *nodes.Node) { n.PublicAddress = ddnsNew })
	if err := r.conns.SetNodeAdvertiseHost(t.Context(), "org", r.conn.ID, ddnsNew); err != nil {
		t.Fatal(err)
	}
	r.domains.routes[0].Binding.Address = ddnsNew
	r.domains.routes[1].Binding.Address, r.domains.routes[1].Status = ddnsNew, RouteRoutingPending
	r.svc.Store = &behindStore{adoptionStore: r.store, behind: []*nodes.Node{{ID: "node_1", OrgID: "org"}}}
	if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
		t.Fatal(err)
	}
	ops := r.domains.ops()
	if len(ops) != 1 || ops[0].Route.ID != "route_x" || ops[0].Route.Binding.Address != ddnsNew || r.log.index("enqueue:") < 0 {
		t.Fatalf("operations %+v", ops)
	}
}

func TestDDNSReconcileCarriesOnAfterTheLastNode(t *testing.T) {
	r := newDDNSRig(t)
	st := &behindStore{adoptionStore: r.store}
	for i := range nodeAddressReconcileBatch + 5 {
		st.behind = append(st.behind, &nodes.Node{ID: fmt.Sprintf("node_%03d", i), OrgID: "org"})
	}
	r.svc.Store = st
	for range 3 {
		if err := r.svc.ReconcileNodeAddresses(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	last := st.behind[nodeAddressReconcileBatch-1].ID
	if !slices.Equal(st.afters, []string{"", last, ""}) {
		t.Fatalf("passes started after %v", st.afters)
	}
}

func TestDDNSOperationWaitsOnAnyAddressAndQuitsALeftNode(t *testing.T) {
	r := newDDNSRig(t)
	binding := RouteBinding{BindingID: "binding_web", ServerID: "srv_1", ConnectionID: r.conn.ID, ConnectionGeneration: 1, InstanceID: "instance", ReleaseID: "release", ContainerID: "container", RuntimeID: "daemon-1", Address: "192.0.2.99"}
	op := &RouteOperation{Kind: "address", PreviousAddress: ddnsOld, Route: Route{OrgID: "org", Input: RouteInput{Hostname: "app.example.com"}, Binding: binding}}
	op.Route.Binding.Address = ddnsNew
	var notice *routeNoticeError
	// A to B to C: the binding names C, which this operation never saw.
	if err := r.svc.addressNotSettled(t.Context(), op, binding, nil); !errors.Is(err, ErrPending) {
		t.Fatalf("another address: %v", err)
	}
	// Between the node's writes it waits too, while the node is direct.
	if err := r.svc.addressNotSettled(t.Context(), op, RouteBinding{}, errUnconfirmedNodeAddress); !errors.Is(err, ErrPending) {
		t.Fatalf("between writes: %v", err)
	}
	// A node that left direct ingress, or was revoked, abandons it.
	for _, leave := range []func(n *nodes.Node){
		func(n *nodes.Node) {
			n.IngressMode, n.PublicAddress, n.PublicAddressConfirmedAt = nodeproto.IngressNone, "", nil
		},
		func(n *nodes.Node) { n.Status = nodes.StatusRevoked },
	} {
		rig := newDDNSRig(t)
		rig.records.set(leave)
		err := rig.svc.addressNotSettled(t.Context(), op, RouteBinding{}, errUnconfirmedNodeAddress)
		if !errors.Is(err, errAddressAbandoned) || errors.Is(err, ErrPending) || !errors.As(err, &notice) || !strings.Contains(notice.message, "app.example.com stays where it is") {
			t.Fatalf("left node: %v", err)
		}
	}
	// Anything else that changed is still a conflict for the caller.
	other := binding
	other.ReleaseID = "release_2"
	if err := r.svc.addressNotSettled(t.Context(), op, other, nil); err != nil {
		t.Fatalf("other change: %v", err)
	}
}

// nodeConnCredentials answers for the node's connection too.
type nodeConnCredentials struct{ routeDNSCredentials }

func (c nodeConnCredentials) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	if id == "conn_node_1" {
		return &connections.Connection{ID: id, OrgID: org, Cloud: connections.Server, Method: connections.MethodNode, Generation: 1, Details: map[string]string{"node_id": "node_gone"}}, nil, nil
	}
	return c.routeDNSCredentials.Credential(ctx, org, id)
}
