package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// RouteRoutingPending is a route whose node moved to an address DNS doesn't
// name yet. Its proxy config stays as it was, so it serves wherever the
// name still points, and it is ready again once a check finds the name on
// the new address.
const RouteRoutingPending = "routing_pending"

// NodeAddressFollowWait bounds one FollowNodeAddress: the wait for another
// follow of the node, the address check and every route's operation, busy
// routes offered again included. It is longer than a hub hook's deadline,
// so the hook gives the follow a context of its own.
const NodeAddressFollowWait = 3 * time.Minute

const (
	// nodeAddressReconcileWait bounds one node's follow in a reconcile
	// pass, and nodeAddressReconcilePass the whole pass, which follows
	// nodeAddressReconcileWorkers nodes at a time.
	nodeAddressReconcileWait    = 30 * time.Second
	nodeAddressReconcilePass    = 2 * time.Minute
	nodeAddressReconcileWorkers = 4
	// nodeAddressRecheckEvery is how often a reconcile, or a node coming
	// back online, checks again an address that didn't answer, or a check
	// that couldn't run.
	nodeAddressRecheckEvery = 5 * time.Minute
	// nodeAddressReconcileBatch is how many nodes one pass looks at. The
	// next pass carries on after the last one.
	nodeAddressReconcileBatch = 50
	// nodeAddressSettleWait bounds each write after a follow's operations
	// are stored, which runs whatever happened to the caller's context.
	nodeAddressSettleWait = time.Minute
	// nodeAddressRollbackWait bounds putting a connection's address back.
	nodeAddressRollbackWait = 10 * time.Second
)

// heldRouteDetail starts what a node shows for routes another change held.
const heldRouteDetail = "route "

// NodeReader reads a node as stored. *nodes.PgStore satisfies it.
type NodeReader interface {
	GetByID(ctx context.Context, id string) (*nodes.Node, error)
}

// nodeIngressDetail records an address check's finding that settles
// nothing. *nodes.Service satisfies it.
type nodeIngressDetail interface {
	RecordIngressDetail(ctx context.Context, nodeID, detail string) error
}

// UnansweredAddressDetail is what a node shows when its new address didn't
// answer the address check.
func UnansweredAddressDetail(address string) string {
	return fmt.Sprintf("address changed to %s but it doesn't answer on port 80", address)
}

// FollowNodeAddress follows a node in direct ingress mode whose confirmed
// address its inventory no longer offers, as when its ISP hands out a new
// one. The new address is confirmed first, the way ConfirmNodeAddress does
// it. When none of the offered addresses answers, nothing changes and the
// node's ingress detail says so.
//
// Once one answers, every route on the node's server connection gets an
// automatic address operation, acting as Loomplane: inside an approved
// route nobody reviews it again, the way a capacity change inside an
// approved envelope isn't. In a managed zone it rewrites the route's own
// A or AAAA record and nothing else in the zone; in an external zone it
// says which record to set and waits for it. The proxy config is left
// alone, so a certificate that is renewing carries on. Only after the
// operations are stored does the node's server connection name the new
// address and the node record it as confirmed, and only then do the
// operations start. An operation that runs first, as recovery may run it,
// waits: its binding check reads the node's settled address, so none of
// them can point DNS at an address that wasn't confirmed.
//
// A route another change holds doesn't hold the node: it settles with the
// routes that took their operation, and the held ones are offered again
// until the context ends, each starting as soon as it is taken. One still
// held then keeps its address, the node's detail names it, and
// ReconcileNodeAddresses moves it later. So does a node whose follow
// didn't finish. A route whose last operation failed is left for a person.
//
// One follow runs per node at a time, here or on another replica. A second
// one waits for the first, then works from the node as stored, so it finds
// the address already followed or follows a newer one. The caller's
// context bounds the wait and the follow; NodeAddressFollowWait is enough.
func (s *Service) FollowNodeAddress(ctx context.Context, n *nodes.Node) error {
	return s.followNode(ctx, n, followMode{wait: true, retryHeld: true})
}

// CatchUpNodeAddress is FollowNodeAddress for a node that came back
// online: its address is checked again at most every
// nodeAddressRecheckEvery, like a reconcile's.
func (s *Service) CatchUpNodeAddress(ctx context.Context, n *nodes.Node) error {
	return s.followNode(ctx, n, followMode{wait: true, recheck: true, retryHeld: true})
}

// followMode is how a follow runs. wait waits for a follow that holds the
// node, where otherwise that follow is left to it. recheck limits how often
// the node's address is checked again. retryHeld offers held routes again
// until the context ends.
type followMode struct{ wait, recheck, retryHeld bool }

// ReconcileNodeAddresses finishes what a follow left: a node whose
// connection doesn't name its confirmed address, one whose inventory no
// longer offers that address, and one with a route on another address, or
// routing pending, that no operation or handoff is moving. A node that is
// consistent isn't looked at, a node another follow holds is left for the
// next pass, and held routes aren't waited for. Each pass looks at the
// nodes after the last pass's, a few at a time, each for at most
// nodeAddressReconcileWait and all of them for nodeAddressReconcilePass.
func (s *Service) ReconcileNodeAddresses(ctx context.Context) error {
	if s.NodeRecords == nil || s.NodeIngress == nil || s.NodeAddresses == nil {
		return nil
	}
	st, ok := s.Store.(interface {
		NodesBehindAddress(ctx context.Context, after string, limit int) ([]*nodes.Node, error)
	})
	if !ok {
		return nil
	}
	f := s.nodeFollows
	if f == nil {
		return nil
	}
	f.mu.Lock()
	after := f.after
	f.mu.Unlock()
	behind, err := st.NodesBehindAddress(ctx, after, nodeAddressReconcileBatch)
	if err != nil {
		return err
	}
	next := ""
	if len(behind) == nodeAddressReconcileBatch {
		next = behind[len(behind)-1].ID
	}
	f.mu.Lock()
	f.after = next
	f.mu.Unlock()

	passCtx, cancel := context.WithTimeout(ctx, nodeAddressReconcilePass)
	defer cancel()
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	slots := make(chan struct{}, nodeAddressReconcileWorkers)
	for _, n := range behind {
		select {
		case <-passCtx.Done():
		case slots <- struct{}{}:
			wg.Add(1)
			go func() {
				defer func() { <-slots; wg.Done() }()
				nodeCtx, cancel := context.WithTimeout(passCtx, nodeAddressReconcileWait)
				defer cancel()
				if followErr := s.followNode(nodeCtx, n, followMode{recheck: true}); followErr != nil && !errors.Is(followErr, ErrBusy) && !errors.Is(followErr, ErrPending) {
					mu.Lock()
					errs = append(errs, fmt.Errorf("node %s: %w", n.ID, followErr))
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	return errors.Join(errs...)
}

// followNode follows n under its lock.
func (s *Service) followNode(ctx context.Context, n *nodes.Node, mode followMode) error {
	if n == nil || n.ID == "" || s.NodeIngress == nil || s.NodeAddresses == nil {
		return nil
	}
	// The lock lasts the follow and the writes after it, whatever happens
	// to ctx, so it can't go while the node settles.
	lockFor := NodeAddressFollowWait
	if deadline, ok := ctx.Deadline(); ok {
		lockFor = time.Until(deadline)
	}
	for {
		lockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockFor+nodeAddressSettleWait)
		err := s.Store.WithOperationLock(lockCtx, "node-address/"+n.ID, func() error {
			return s.followNodeAddress(ctx, n, mode)
		})
		cancel()
		if !errors.Is(err, ErrBusy) || !mode.wait {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: node %s's address is still being followed", ErrBusy, n.ID)
		case <-time.After(s.serverReadyPoll):
		}
	}
}

func (s *Service) followNodeAddress(ctx context.Context, hooked *nodes.Node, mode followMode) error {
	n := hooked
	if s.NodeRecords != nil {
		stored, err := s.NodeRecords.GetByID(ctx, hooked.ID)
		if errors.Is(err, nodes.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		n = stored
	}
	// Only a confirmed address is followed. A node in mode none stays
	// there until its owner or an adoption asks again.
	if n.Status == nodes.StatusRevoked || n.IngressMode != nodeproto.IngressDirect || n.PublicAddress == "" || n.PublicAddressConfirmedAt == nil {
		return nil
	}
	conn, err := s.nodeServerConnection(ctx, n)
	if err != nil || conn == nil {
		return err
	}
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	address := n.PublicAddress
	if offered := publicNodeAddresses(n.Inventory.PublicAddresses); len(offered) > 0 && !slices.Contains(offered, n.PublicAddress) {
		if mode.recheck && !s.nodeCheckDue(n.ID) {
			return nil
		}
		_, _, found, checkErr := s.checkNodeAddress(ctx, n.OrgID, conn.ID, n.Inventory.PublicAddresses)
		if checkErr != nil {
			return checkErr
		}
		if found == "" {
			return s.recordUnansweredAddress(ctx, n, offered[0])
		}
		address = found
	}
	offer := &routeOffer{done: map[string]bool{}}
	if offerErr := s.offerAddressOperations(ctx, st, n, conn, address, offer); offerErr != nil {
		return offerErr
	}
	// The operations are stored, so the node follows them even when the
	// caller's time is up. The node settles now: routes that are held
	// don't hold the others back.
	detached := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(ctx), nodeAddressSettleWait)
	}
	settleCtx, cancel := detached()
	err = s.settleNodeAddress(settleCtx, conn, n.ID, address, n.PublicAddress)
	cancel()
	if err != nil {
		return err
	}
	moved := address != n.PublicAddress
	start := func(ops []*RouteOperation) {
		for _, op := range ops {
			enqueueCtx, stop := detached()
			s.enqueueRoute(enqueueCtx, op)
			stop()
		}
	}
	start(offer.accepted)
	for mode.retryHeld && len(offer.held) > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(s.serverReadyPoll):
		}
		if ctx.Err() != nil {
			break
		}
		offer.accepted = nil
		if err = s.offerAddressOperations(ctx, st, n, conn, address, offer); err != nil {
			s.warn(fmt.Sprintf("Routes left busy stay where they are; node %s is on %s", n.Name, address), err)
			break
		}
		// The node is settled, so these start right away.
		start(offer.accepted)
	}
	reportCtx, cancel := detached()
	defer cancel()
	return s.reportHeldRoutes(reportCtx, n, offer, address, moved)
}

// nodeFollowState is this process's record of node follows: when each
// node's address was last checked, and the last node a reconcile pass
// reached, so the next pass carries on after it.
type nodeFollowState struct {
	mu      sync.Mutex
	checked map[string]time.Time
	after   string
}

// nodeCheckDue reports whether nodeID's address may be checked again now,
// and if so marks it checked. Marks older than nodeAddressRecheckEvery are
// dropped as it goes.
func (s *Service) nodeCheckDue(nodeID string) bool {
	f := s.nodeFollows
	if f == nil {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := s.now()
	for id, at := range f.checked {
		if now.Sub(at) >= nodeAddressRecheckEvery {
			delete(f.checked, id)
		}
	}
	if _, ok := f.checked[nodeID]; ok {
		return false
	}
	f.checked[nodeID] = now
	return true
}

// nodeServerConnection is the node's ready server connection, nil when it
// has none.
func (s *Service) nodeServerConnection(ctx context.Context, n *nodes.Node) (*connections.Connection, error) {
	if s.NodeConnections == nil {
		return nil, nil
	}
	conns, err := s.NodeConnections.NodeConnections(ctx, n.OrgID, n.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range conns {
		if c.OrgID == n.OrgID && c.Cloud == connections.Server && c.Method == connections.MethodNode && c.Status == connections.StatusReady {
			return c, nil
		}
	}
	return nil, nil
}

// recordUnansweredAddress puts the finding on the node and in the audit
// trail. The node keeps its mode and its confirmed address.
func (s *Service) recordUnansweredAddress(ctx context.Context, n *nodes.Node, address string) error {
	if d, ok := s.NodeIngress.(nodeIngressDetail); ok {
		if err := d.RecordIngressDetail(ctx, n.ID, UnansweredAddressDetail(address)); err != nil {
			return err
		}
	}
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "node.address_unanswered", Severity: "warning", Principal: security.SystemPrincipal(n.OrgID),
			Attributes: map[string]any{"node_id": n.ID, "address": address, "previous_address": n.PublicAddress, "automatic": true, "via": "node_address"}})
	}
	return nil
}

// HeldRouteDetail is what a node shows for a route another change held,
// so it stayed on its old address.
func HeldRouteDetail(hostname, old string) string {
	return StuckRouteDetail(hostname, old, "another change held it")
}

// StuckRouteDetail is what a node shows for a route that stayed on its old
// address, and why.
func StuckRouteDetail(hostname, old, reason string) string {
	return fmt.Sprintf("%s%s stays on %s; %s", heldRouteDetail, hostname, old, reason)
}

// routeOffer is what a follow's offers of address operations came to:
// accepted operations, routes another change holds, and routes left for a
// person with the reason. done is every route that needs no further offer.
type routeOffer struct {
	accepted []*RouteOperation
	held     []*Route
	stuck    []stuckRoute
	done     map[string]bool
}

type stuckRoute struct {
	route  *Route
	reason string
}

// reportHeldRoutes says which routes stayed behind, on the node and in the
// audit trail, or clears what an earlier follow said once none did. A route
// the node's detail already names isn't reported again; after the node
// moved, every route is.
func (s *Service) reportHeldRoutes(ctx context.Context, n *nodes.Node, offer *routeOffer, address string, moved bool) error {
	shown := n.IngressDetail
	if moved {
		shown = ""
	}
	d, ok := s.NodeIngress.(nodeIngressDetail)
	stayed := make([]stuckRoute, 0, len(offer.held)+len(offer.stuck))
	for _, r := range offer.held {
		stayed = append(stayed, stuckRoute{route: r, reason: "another change held it"})
	}
	stayed = append(stayed, offer.stuck...)
	if len(stayed) == 0 {
		if ok && strings.HasPrefix(shown, heldRouteDetail) {
			return d.RecordIngressDetail(ctx, n.ID, "")
		}
		return nil
	}
	details := make([]string, 0, len(stayed))
	for _, r := range stayed {
		line := StuckRouteDetail(r.route.Input.Hostname, r.route.Binding.Address, r.reason)
		details = append(details, line)
		if strings.Contains(shown, line) {
			continue
		}
		s.warn(fmt.Sprintf("Route %s stays on %s and didn't follow node %s to %s", r.route.Input.Hostname, r.route.Binding.Address, n.Name, address), errors.New(r.reason))
		if s.Audit != nil {
			s.Audit.Record(ctx, projects.AuditEvent{Action: "domain.route.address_held", Severity: "warning", Principal: security.SystemPrincipal(n.OrgID), Attributes: map[string]any{
				"resource_id": r.route.ID, "project_id": r.route.ProjectID, "hostname": r.route.Input.Hostname, "node_id": n.ID, "address": address,
				"previous_address": r.route.Binding.Address, "reason": r.reason, "automatic": true, "via": "node_address",
			}})
		}
	}
	detail := strings.Join(details, "; ")
	if !ok || detail == shown {
		return nil
	}
	return d.RecordIngressDetail(ctx, n.ID, detail)
}

// followsNode reports a route on conn that an address change moves to
// address: one on another address, or one still routing pending.
func followsNode(r *Route, conn *connections.Connection, address string) bool {
	return r.Binding.ConnectionID == conn.ID && r.Binding.BackendName() == RouteBackendServer &&
		((r.Status == "ready" && r.Binding.Address != address) || r.Status == RouteRoutingPending)
}

// movingTo reports an operation that is already moving its route to
// address.
func movingTo(op *RouteOperation, address string) bool {
	return op != nil && op.Kind == "address" && op.Route.Binding.Address == address && (op.Status == "pending" || op.Status == "running" || op.Status == "waiting")
}

// failedHold is why a failed operation keeps its route from following, or
// "" when the next address operation may replace it. An earlier address
// change or check that failed is replaced; one that failed on this very
// address, one whose DNS write has no answer, or any other failed
// operation needs a person.
func failedHold(op *RouteOperation, address string) string {
	if op == nil || op.Status != "failed" {
		return ""
	}
	switch {
	case op.DNSAttempted && op.Receipt == nil:
		return "its " + op.Kind + " operation failed with a DNS write unanswered; retry it"
	case op.Kind == "address" && op.Route.Binding.Address == address:
		return "its move there failed; retry it, or remove the route and add it again"
	case op.Kind != "address" && op.Kind != "verify":
		return "its " + op.Kind + " operation failed; retry it or remove the route"
	}
	return ""
}

// offerAddressOperations offers an address operation, once, to every route
// that follows the node and isn't done, and records in offer what came of
// each.
func (s *Service) offerAddressOperations(ctx context.Context, st DomainStore, n *nodes.Node, conn *connections.Connection, address string, offer *routeOffer) error {
	routes, err := st.OrgRoutes(ctx, conn.OrgID)
	if err != nil {
		return err
	}
	var follow []*Route
	var ids []string
	for _, r := range routes {
		if !offer.done[r.ID] && followsNode(r, conn, address) {
			follow, ids = append(follow, r), append(ids, r.ID)
		}
	}
	latest := map[string]*RouteOperation{}
	if len(ids) > 0 {
		if latest, err = st.LatestRouteOperations(ctx, conn.OrgID, ids); err != nil {
			return err
		}
	}
	offer.held = offer.held[:0]
	for _, r := range follow {
		if movingTo(latest[r.ID], address) {
			offer.done[r.ID] = true
			continue
		}
		if reason := failedHold(latest[r.ID], address); reason != "" {
			offer.done[r.ID] = true
			offer.stuck = append(offer.stuck, stuckRoute{route: r, reason: reason})
			continue
		}
		op, opErr := s.newAddressOperation(ctx, st, r, address)
		if opErr != nil {
			offer.done[r.ID] = true
			offer.stuck = append(offer.stuck, stuckRoute{route: r, reason: opErr.Error()})
			continue
		}
		switch opErr = st.AcceptRoute(ctx, op); {
		case opErr == nil:
			offer.done[r.ID] = true
			offer.accepted = append(offer.accepted, op)
			s.auditAddressOperation(ctx, op, n.ID)
		case errors.Is(opErr, ErrBusy) || errors.Is(opErr, ErrConflict):
			offer.held = append(offer.held, r)
		default:
			offer.done[r.ID] = true
			offer.stuck = append(offer.stuck, stuckRoute{route: r, reason: opErr.Error()})
		}
	}
	return nil
}

// newAddressOperation moves r to address, as Loomplane. Only the binding's
// address changes. In a managed zone the desired record is the route's own
// record with the new address, replacing exactly the one it has now.
func (s *Service) newAddressOperation(ctx context.Context, st DomainStore, r *Route, address string) (*RouteOperation, error) {
	zone, err := s.linkedZone(ctx, st, r.OrgID, r.Input.ZoneID)
	if err != nil {
		return nil, err
	}
	record, err := routeDNSRecord(zone, r.Input.Hostname, address, r.ID)
	if err != nil {
		return nil, err
	}
	id, err := randomID("routeop")
	if err != nil {
		return nil, err
	}
	system := security.SystemPrincipal(r.OrgID)
	route := *r
	route.Binding.Address = address
	op := &RouteOperation{ID: id, Kind: "address", Automatic: true, Status: "pending", Attempt: 1, Revision: 1, Route: route, PreviousAddress: r.Binding.Address,
		Principal: system, ExecutionPrincipal: &system}
	if zone.Mode == "managed" {
		if r.Receipt == nil {
			return nil, fmt.Errorf("%w: Loomplane has no record of the DNS record it wrote for %s", ErrInvalid, r.Input.Hostname)
		}
		record.ID, record.Owner, record.Proxied = r.Receipt.ID, r.Receipt.Owner, r.Receipt.Proxied
		if record.Proxied {
			record.TTL = 300
		}
		op.PreviousReceipt = r.Receipt
	} else {
		op.Receipt = r.Receipt
	}
	op.DesiredRecord = record
	op.Message = addressMessage(op, zone)
	return op, nil
}

// addressRecordType is the record type that names address.
func addressRecordType(address string) string {
	if a, err := netip.ParseAddr(address); err == nil && a.Unmap().Is6() {
		return "AAAA"
	}
	return "A"
}

// addressMessage is what an address operation tells the route's owner.
// The same operation always says the same thing.
func addressMessage(op *RouteOperation, zone *Zone) string {
	host, address := op.Route.Input.Hostname, op.Route.Binding.Address
	kind, was := addressRecordType(address), addressRecordType(op.PreviousAddress)
	if zone.Mode != "managed" {
		message := fmt.Sprintf("set the %s record for %s to %s", kind, host, address)
		if was != kind {
			message += fmt.Sprintf(" and remove its %s record", was)
		}
		return message
	}
	if op.PreviousReceipt != nil {
		was = op.PreviousReceipt.Type
	}
	if was != kind {
		return fmt.Sprintf("address changed to %s, and Loomplane doesn't turn the %s record for %s into an %s record by itself; remove the route and add it again", address, was, host, kind)
	}
	return fmt.Sprintf("address changed to %s, DNS updating", address)
}

// checkAddressRecord refuses an address operation whose record can't be
// changed in place: a managed zone's A record becomes an AAAA record only
// by deleting one and creating the other, which Loomplane leaves to the
// owner.
func checkAddressRecord(op *RouteOperation, zone *Zone) error {
	if zone.Mode != "managed" {
		return nil
	}
	if op.PreviousReceipt == nil || op.PreviousReceipt.Type != op.DesiredRecord.Type {
		return &routeNoticeError{err: ErrConflict, message: addressMessage(op, zone)}
	}
	return nil
}

// followRouteAddress runs an address operation: the route's record in a
// managed zone, then a check of the route on the new address. Its proxy
// config isn't touched: it is the same proxy on the same machine.
func (s *Service) followRouteAddress(ctx context.Context, st DomainStore, op *RouteOperation, state *IngressState, zone *Zone) error {
	check, done, err := s.routeAddressProbe(ctx, op, state)
	if err != nil {
		return err
	}
	defer done()
	if state.Current == nil {
		return ErrConflict
	}
	if checkErr := checkAddressRecord(op, zone); checkErr != nil {
		return checkErr
	}
	if dnsErr := s.applyRouteDNS(ctx, st, op, zone); dnsErr != nil {
		return dnsErr
	}
	return s.settleRouteAddress(ctx, st, op, zone, check(ctx, op.Route))
}

// probeRouteAddress reaches the route's server under the operation's
// fence and returns the check of the route there, and what ends it.
func (s *Service) probeRouteAddress(ctx context.Context, op *RouteOperation, state *IngressState) (check func(context.Context, Route) RouteVerification, done func(), err error) {
	c, err := s.routeClient(ctx, op.Route)
	if err != nil {
		return nil, nil, err
	}
	fence := server.EffectFence{Operation: op.Route.OrgID + "/ingress/" + op.Route.Binding.ServerID, Generation: op.Generation}
	if err = c.ArmEffectFence(server.WithEffectFence(ctx, fence), fence); err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	check = func(ctx context.Context, route Route) RouteVerification {
		return s.verifyRouteDestination(server.WithEffectFence(ctx, fence), c, route, state, false)
	}
	return check, func() { _ = c.Close() }, nil
}

// errAddressAbandoned is an address operation whose node no longer takes
// traffic directly. It is cancelled, which frees its route.
var errAddressAbandoned = errors.New("address change abandoned")

// addressNotSettled makes an address operation that runs before its node
// settled on the operation's address wait rather than fail: the binding
// differs from the operation's only in its address, or has none while the
// node's two writes land. It never writes DNS, so recovery may run it
// early. When the node no longer takes traffic directly, revoked or in
// mode none, the operation is abandoned instead.
func (s *Service) addressNotSettled(ctx context.Context, op *RouteOperation, current RouteBinding, err error) error {
	waiting := &routeNoticeError{err: ErrPending, message: "Waiting for the node to settle on " + op.Route.Binding.Address + "."}
	if errors.Is(err, errUnconfirmedNodeAddress) {
		if s.nodeLeftDirect(ctx, op) {
			return &routeNoticeError{err: errAddressAbandoned, message: "The node no longer takes traffic directly, so " + op.Route.Input.Hostname + " stays where it is."}
		}
		return waiting
	}
	if err != nil || current.Address == op.Route.Binding.Address {
		return nil
	}
	current.Address = op.Route.Binding.Address
	if !reflect.DeepEqual(current, op.Route.Binding) {
		return nil
	}
	return waiting
}

// nodeLeftDirect reports whether the node behind op's connection is gone,
// revoked, or no longer in direct ingress mode with a confirmed address.
// When that can't be read, it says no, and the operation waits.
func (s *Service) nodeLeftDirect(ctx context.Context, op *RouteOperation) bool {
	if s.NodeRecords == nil || s.Connections == nil {
		return false
	}
	conn, _, err := s.Connections.Credential(ctx, op.Route.OrgID, op.Route.Binding.ConnectionID)
	if err != nil || conn.Details["node_id"] == "" {
		return false
	}
	n, err := s.NodeRecords.GetByID(ctx, conn.Details["node_id"])
	if errors.Is(err, nodes.ErrNotFound) {
		return true
	}
	if err != nil {
		return false
	}
	return n.OrgID != op.Route.OrgID || n.Status == nodes.StatusRevoked || n.IngressMode != nodeproto.IngressDirect || n.PublicAddress == "" || n.PublicAddressConfirmedAt == nil
}

// settleRouteAddress publishes what the check v found. A route seen on its
// new address is ready with that check. Until then it is routing pending
// and keeps its last check, a certificate's renewal state included, and
// the operation waits.
func (s *Service) settleRouteAddress(ctx context.Context, st DomainStore, op *RouteOperation, zone *Zone, v RouteVerification) error {
	route := &op.Route
	if v.DNS && v.TLS && v.Backend {
		route.Verification, route.Status, route.UpdatedAt = v, "ready", s.now().UTC()
		return st.PublishRoute(ctx, op)
	}
	if route.Status != RouteRoutingPending {
		route.Status, route.UpdatedAt = RouteRoutingPending, s.now().UTC()
		if err := st.PublishRoute(ctx, op); err != nil {
			return err
		}
	}
	return &routeNoticeError{err: ErrPending, message: addressMessage(op, zone)}
}

// auditAddressOperation records an address operation as the automation's.
func (s *Service) auditAddressOperation(ctx context.Context, op *RouteOperation, nodeID string) {
	if s.Audit == nil {
		return
	}
	s.Audit.Record(ctx, projects.AuditEvent{Action: "domain.route." + op.Kind, Severity: "info", Principal: op.Principal, Attributes: map[string]any{
		"resource_id": op.ID, "project_id": op.Route.ProjectID, "route_id": op.Route.ID, "hostname": op.Route.Input.Hostname, "node_id": nodeID,
		"address": op.Route.Binding.Address, "previous_address": op.PreviousAddress, "automatic": true, "via": "node_address",
	}})
}

// acceptAddressRoute lets an address operation onto a route only while
// nothing else changes it. An earlier address operation that hasn't
// finished, or a check, is cancelled; a record the earlier one already
// wrote is the record this one replaces. A handoff, a removal, a running
// operation or one whose DNS write has no answer yet makes the route busy.
func acceptAddressRoute(ctx context.Context, tx driver.Tx, op *RouteOperation) error {
	route := op.Route
	var retained bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_route_handoffs WHERE org_id=$1 AND route_id=$2 AND NOT released)`, route.OrgID, route.ID).Scan(&retained); err != nil {
		return err
	}
	if retained {
		return fmt.Errorf("%w: the route is being handed off", ErrBusy)
	}
	rows, err := tx.Query(ctx, `SELECT body,principal,checkpoint FROM loomplane_route_operations WHERE org_id=$1 AND route_id=$2 AND status NOT IN ('preview','succeeded','cancelled') ORDER BY id FOR UPDATE`, route.OrgID, route.ID)
	if err != nil {
		return err
	}
	var active []*RouteOperation
	for rows.Next() {
		a, scanErr := scanRouteOperation(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		active = append(active, a)
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	for _, a := range active {
		if (a.Kind != "address" && a.Kind != "verify") || a.Status == "running" || (a.DNSAttempted && a.Receipt == nil) {
			return fmt.Errorf("%w: another operation on the route is in flight", ErrBusy)
		}
	}
	for _, a := range active {
		if a.Kind == "address" && a.Receipt != nil && op.PreviousReceipt != nil {
			op.PreviousReceipt = a.Receipt
			op.DesiredRecord.ID, op.DesiredRecord.Owner = a.Receipt.ID, a.Receipt.Owner
		}
		a.Status = "cancelled"
		a.Revision++
		body, _, _, encodeErr := routeBytes(a)
		if encodeErr != nil {
			return encodeErr
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_route_operations SET status='cancelled',revision=$3,body=$4 WHERE org_id=$1 AND id=$2`, route.OrgID, a.ID, a.Revision, body); err != nil {
			return err
		}
	}
	return nil
}

// yieldAddressRoute cancels an address operation waiting on the route, so
// another change, such as a handoff, can go ahead; the address change is
// made again after it. One that is running, or that wrote a record the
// route doesn't hold yet, keeps the route busy.
func yieldAddressRoute(ctx context.Context, tx driver.Tx, op *RouteOperation) error {
	rows, err := tx.Query(ctx, `SELECT body,principal,checkpoint FROM loomplane_route_operations WHERE org_id=$1 AND route_id=$2 AND status NOT IN ('preview','succeeded','cancelled') AND body->>'kind'='address' ORDER BY id FOR UPDATE`, op.Route.OrgID, op.Route.ID)
	if err != nil {
		return err
	}
	var active []*RouteOperation
	for rows.Next() {
		a, scanErr := scanRouteOperation(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		active = append(active, a)
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	for _, a := range active {
		if a.Status == "running" || (a.DNSAttempted && a.Receipt == nil) || (a.Receipt != nil && !reflect.DeepEqual(a.Receipt, op.Route.Receipt)) {
			return fmt.Errorf("%w: the route is following its node to a new address", ErrBusy)
		}
		a.Status = "cancelled"
		a.Revision++
		body, _, _, encodeErr := routeBytes(a)
		if encodeErr != nil {
			return encodeErr
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_route_operations SET status='cancelled',revision=$3,body=$4 WHERE org_id=$1 AND id=$2`, op.Route.OrgID, a.ID, a.Revision, body); err != nil {
			return err
		}
	}
	return nil
}

// NodesBehindAddress lists, across organizations and after the node ID
// after, at most limit nodes in direct ingress mode with a confirmed
// address that a follow left behind: the node's server connection names
// another address, its inventory doesn't offer that address, or a server
// route on the connection is on another address or still routing pending
// with no operation of any status but succeeded or cancelled, and no
// handoff, on it. A route whose operation failed waits for a person, so it
// doesn't bring its node back.
func (s *PgStore) NodesBehindAddress(ctx context.Context, after string, limit int) ([]*nodes.Node, error) {
	rows, err := s.db.Query(ctx, `SELECT n.id,n.org_id FROM loomplane_nodes n
 JOIN loomplane_cloud_connections c ON c.org_id=n.org_id AND c.details->>'node_id'=n.id AND c.cloud='server' AND c.status='ready'
 WHERE n.id>$1 AND n.revoked_at IS NULL AND n.ingress_mode='direct' AND n.public_address<>'' AND n.public_address_confirmed_at IS NOT NULL
 AND (coalesce(c.details->>'advertise_host','')<>n.public_address
  OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(coalesce(n.inventory->'public_addresses','[]'::jsonb)) a(address) WHERE a.address=n.public_address)
  OR EXISTS(SELECT 1 FROM loomplane_domain_routes r WHERE r.org_id=n.org_id AND r.connection_id=c.id AND r.backend='server'
   AND (r.status='routing_pending' OR (r.status='ready' AND coalesce(r.body->'binding'->>'address','')<>n.public_address))
   AND NOT EXISTS(SELECT 1 FROM loomplane_route_operations o WHERE o.org_id=r.org_id AND o.route_id=r.id AND o.status NOT IN ('preview','succeeded','cancelled'))
   AND NOT EXISTS(SELECT 1 FROM loomplane_route_handoffs h WHERE h.org_id=r.org_id AND h.route_id=r.id AND NOT h.released)))
 GROUP BY n.id,n.org_id ORDER BY n.id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*nodes.Node{}
	for rows.Next() {
		n := &nodes.Node{}
		if scanErr := rows.Scan(&n.ID, &n.OrgID); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
