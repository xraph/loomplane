package cloudinfra

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	copied := *op
	d.accepted = append(d.accepted, &copied)
	d.log.add("accept:" + op.Route.ID)
	return nil
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
}

func (w *ddnsWorkflow) StartRoute(_ context.Context, _, id string, _ int) error {
	w.log.add("enqueue:" + id)
	return nil
}

type ddnsRig struct {
	*addressRig
	domains *ddnsDomains
	records *ddnsNodes
	nodeIn  *ddnsIngress
	log     *ddnsLog
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
	r.domains = &ddnsDomains{log: log, busy: map[string]error{},
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
	a.svc.Workflow = &ddnsWorkflow{adoptionWorkflow: a.workflow, log: log}
	a.svc.serverReadyPoll = time.Millisecond
	a.svc.Warn = func(msg string, err error) { r.warned = append(r.warned, msg+": "+err.Error()) }
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

func TestDDNSLeavesANodeWithoutDirectIngress(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.records.set(func(n *nodes.Node) {
		n.IngressMode, n.PublicAddress, n.PublicAddressConfirmedAt = nodeproto.IngressNone, "", nil
	})
	r.follow(t)
	if len(r.fetches()) != 0 || len(r.domains.ops()) != 0 || len(r.ingress.list()) != 0 {
		t.Fatal("followed a node that has no confirmed address")
	}
}

func TestDDNSSkipsABusyRoute(t *testing.T) {
	r := newDDNSRig(t)
	r.answering[ddnsNew] = true
	r.domains.busy["route_x"] = ErrBusy
	r.follow(t)
	ops := r.domains.ops()
	if len(ops) != 1 || ops[0].Route.ID != "route_m" {
		t.Fatalf("operations %+v", ops)
	}
	// The node still moves, so the routes that did follow match it.
	if r.advertised(t) != ddnsNew || len(r.ingress.list()) != 1 {
		t.Fatal("a busy route held the node back")
	}
	if len(r.warned) != 1 || !strings.Contains(r.warned[0], "api.shop.dev") {
		t.Fatalf("warned %v", r.warned)
	}
}

func TestRenewalStateShowsWhileDNSFollows(t *testing.T) {
	r := newDDNSRig(t)
	now := time.Now().UTC()
	renewing := RouteVerification{DNS: true, TLS: true, Backend: true, CheckedAt: now.Add(-time.Minute), CertExpires: now.Add(3 * 24 * time.Hour)}
	r.domains.routes[0].Verification, r.domains.routes[1].Verification = renewing, renewing
	r.answering[ddnsNew] = true
	r.follow(t)
	for _, c := range []struct{ route, zone string }{{"route_m", "zone_m"}, {"route_x", "zone_x"}} {
		op := r.domains.op(t, c.route)
		zone := r.domains.zone(c.zone)
		zone.ProofName, zone.ProofValue, zone.ProofExpiresAt = "_loomplane."+zone.Name, "proof", now.Add(time.Hour)
		// Pending, before it wrote anything.
		if got := DeriveRouteStatus(op.Route, zone, op, now); got.Certificate != DomainRenewing || got.Message != op.Message {
			t.Fatalf("%s pending: %+v", c.route, got)
		}
		if err := r.svc.settleRouteAddress(t.Context(), r.domains, op, zone, RouteVerification{}); !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		got := DeriveRouteStatus(r.domains.lastPublished(), zone, op, now)
		wantDNS := DomainProvisioning
		if zone.Mode == "external" {
			wantDNS = DomainAwaitingVerification
		}
		if got.Certificate != DomainRenewing || got.DNS != wantDNS || got.Route != DomainProvisioning {
			t.Fatalf("%s routing pending: %+v", c.route, got)
		}
	}
}
