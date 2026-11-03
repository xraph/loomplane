package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
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
	// pass, so one node with a busy route doesn't hold up the others.
	nodeAddressReconcileWait = 30 * time.Second
	// nodeAddressRecheckEvery is how often a reconcile checks again an
	// address that didn't answer, or a check that couldn't run.
	nodeAddressRecheckEvery = 5 * time.Minute
	// nodeAddressReconcileBatch is how many nodes one pass looks at.
	nodeAddressReconcileBatch = 50
	// nodeAddressSettleWait bounds the writes after a follow's operations
	// are stored, which run whatever happened to the caller's context.
	nodeAddressSettleWait = time.Minute
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
// alone, so a certificate that is renewing carries on. Only after every
// operation is stored does the node's server connection name the new
// address and the node record it as confirmed, and only then do the
// operations start. An operation that runs first, as recovery may run it,
// waits: its binding check reads the node's settled address, so none of
// them can point DNS at an address that wasn't confirmed.
//
// A route another change holds is offered again until the context ends;
// one still held then keeps its address, the node's detail names it, and
// ReconcileNodeAddresses moves it later. So does a node whose follow
// didn't finish.
//
// One follow runs per node at a time, here or on another replica. A second
// one waits for the first, then works from the node as stored, so it finds
// the address already followed or follows a newer one. The caller's
// context bounds the wait and the follow; NodeAddressFollowWait is enough.
func (s *Service) FollowNodeAddress(ctx context.Context, n *nodes.Node) error {
	if n == nil {
		return nil
	}
	return s.followNode(ctx, n, true, false)
}

// ReconcileNodeAddresses finishes what a follow left: a node whose
// connection doesn't name its confirmed address, one whose inventory no
// longer offers that address, and one with a route still on another
// address and no operation moving it. A node that is consistent isn't
// looked at, and a node's address is checked again at most every
// nodeAddressRecheckEvery here. A node another follow holds is left for
// the next pass. Each node gets nodeAddressReconcileWait.
func (s *Service) ReconcileNodeAddresses(ctx context.Context) error {
	if s.NodeRecords == nil || s.NodeIngress == nil || s.NodeAddresses == nil {
		return nil
	}
	st, ok := s.Store.(interface {
		NodesBehindAddress(ctx context.Context, limit int) ([]*nodes.Node, error)
	})
	if !ok {
		return nil
	}
	behind, err := st.NodesBehindAddress(ctx, nodeAddressReconcileBatch)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range behind {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		nodeCtx, cancel := context.WithTimeout(ctx, nodeAddressReconcileWait)
		err = s.followNode(nodeCtx, n, false, true)
		cancel()
		if err != nil && !errors.Is(err, ErrBusy) && !errors.Is(err, ErrPending) {
			errs = append(errs, fmt.Errorf("node %s: %w", n.ID, err))
		}
	}
	return errors.Join(errs...)
}

// followNode follows n under its lock. wait waits for a follow that holds
// it; otherwise that follow is left to it. recheck limits how often the
// node's address is checked again.
func (s *Service) followNode(ctx context.Context, n *nodes.Node, wait, recheck bool) error {
	if n.ID == "" || s.NodeIngress == nil || s.NodeAddresses == nil {
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
			return s.followNodeAddress(ctx, n, recheck)
		})
		cancel()
		if !errors.Is(err, ErrBusy) || !wait {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: node %s's address is still being followed", ErrBusy, n.ID)
		case <-time.After(s.serverReadyPoll):
		}
	}
}

func (s *Service) followNodeAddress(ctx context.Context, hooked *nodes.Node, recheck bool) error {
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
	address := n.PublicAddress
	if offered := publicNodeAddresses(n.Inventory.PublicAddresses); len(offered) > 0 && !slices.Contains(offered, n.PublicAddress) {
		if recheck && !s.nodeCheckDue(n.ID) {
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
	ops, held, err := s.addressOperations(ctx, n, conn, address)
	if err != nil {
		return err
	}
	// The operations are stored, so the node follows them even when the
	// caller's time is up.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeAddressSettleWait)
	defer cancel()
	if settleErr := s.settleNodeAddress(settleCtx, conn, n.ID, address, n.PublicAddress); settleErr != nil {
		return settleErr
	}
	for _, op := range ops {
		s.enqueueRoute(settleCtx, op)
	}
	return s.reportHeldRoutes(settleCtx, n, held, address)
}

// nodeCheckDue reports whether a reconcile may check nodeID's address now,
// and if so marks it checked.
func (s *Service) nodeCheckDue(nodeID string) bool {
	s.nodeConfirmsMu.Lock()
	defer s.nodeConfirmsMu.Unlock()
	if s.nodeConfirms == nil {
		s.nodeConfirms = map[string]time.Time{}
	}
	now := s.now()
	if last, ok := s.nodeConfirms[nodeID]; ok && now.Sub(last) < nodeAddressRecheckEvery {
		return false
	}
	s.nodeConfirms[nodeID] = now
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
	return fmt.Sprintf("%s%s stays on %s; another change held it", heldRouteDetail, hostname, old)
}

// reportHeldRoutes says which routes stayed behind, on the node and in the
// audit trail, or clears what an earlier follow said once none did.
func (s *Service) reportHeldRoutes(ctx context.Context, n *nodes.Node, held []*Route, address string) error {
	d, ok := s.NodeIngress.(nodeIngressDetail)
	if len(held) == 0 {
		if ok && strings.HasPrefix(n.IngressDetail, heldRouteDetail) {
			return d.RecordIngressDetail(ctx, n.ID, "")
		}
		return nil
	}
	details := make([]string, 0, len(held))
	for _, r := range held {
		details = append(details, HeldRouteDetail(r.Input.Hostname, r.Binding.Address))
		s.warn(fmt.Sprintf("Route %s stays on %s; another change holds it, so it didn't follow node %s to %s yet", r.Input.Hostname, r.Binding.Address, n.Name, address), ErrBusy)
		if s.Audit != nil {
			s.Audit.Record(ctx, projects.AuditEvent{Action: "domain.route.address_held", Severity: "warning", Principal: security.SystemPrincipal(n.OrgID), Attributes: map[string]any{
				"resource_id": r.ID, "project_id": r.ProjectID, "hostname": r.Input.Hostname, "node_id": n.ID, "address": address, "previous_address": r.Binding.Address, "automatic": true, "via": "node_address",
			}})
		}
	}
	if !ok {
		return nil
	}
	return d.RecordIngressDetail(ctx, n.ID, strings.Join(details, "; "))
}

// followsNode reports a route an address change on conn moves to address.
func followsNode(r *Route, conn *connections.Connection, address string) bool {
	return r.Binding.ConnectionID == conn.ID && r.Binding.BackendName() == RouteBackendServer &&
		(r.Status == "ready" || r.Status == RouteRoutingPending) && r.Binding.Address != address
}

// movingTo reports an operation that is already moving its route to
// address.
func movingTo(op *RouteOperation, address string) bool {
	return op != nil && op.Kind == "address" && op.Route.Binding.Address == address && (op.Status == "pending" || op.Status == "running" || op.Status == "waiting")
}

// addressOperations stores an address operation for every route that
// follows the node and isn't already moving there. A busy route is offered
// again until ctx ends; the routes still held then are returned, and keep
// their address. A route that can't take one at all is reported.
func (s *Service) addressOperations(ctx context.Context, n *nodes.Node, conn *connections.Connection, address string) (accepted []*RouteOperation, held []*Route, err error) {
	st, err := s.domainStore()
	if err != nil {
		return nil, nil, err
	}
	done := map[string]bool{}
	for {
		routes, listErr := st.OrgRoutes(ctx, conn.OrgID)
		if listErr != nil && len(accepted) == 0 {
			return nil, nil, listErr
		}
		if listErr != nil {
			// Routes that already have their operation follow the node.
			s.warn(fmt.Sprintf("Routes left busy stay where they are; node %s moves to %s", n.Name, address), listErr)
			return accepted, held, nil
		}
		var follow []*Route
		var ids []string
		for _, r := range routes {
			if !done[r.ID] && followsNode(r, conn, address) {
				follow, ids = append(follow, r), append(ids, r.ID)
			}
		}
		latest := map[string]*RouteOperation{}
		if len(ids) > 0 {
			if latest, err = st.LatestRouteOperations(ctx, conn.OrgID, ids); err != nil {
				return accepted, nil, err
			}
		}
		held = held[:0]
		for _, r := range follow {
			if movingTo(latest[r.ID], address) {
				done[r.ID] = true
				continue
			}
			op, opErr := s.newAddressOperation(ctx, st, r, address)
			if opErr == nil {
				opErr = st.AcceptRoute(ctx, op)
			}
			switch {
			case opErr == nil:
				done[r.ID] = true
				accepted = append(accepted, op)
				s.auditAddressOperation(ctx, op, n.ID)
			case errors.Is(opErr, ErrBusy) || errors.Is(opErr, ErrConflict):
				held = append(held, r)
			default:
				done[r.ID] = true
				s.warn(fmt.Sprintf("Route %s stays on %s; it couldn't follow node %s to %s", r.Input.Hostname, r.Binding.Address, n.Name, address), opErr)
			}
		}
		if len(held) == 0 || ctx.Err() != nil {
			return accepted, held, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(s.serverReadyPoll):
		}
	}
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

// addressNotSettled makes an address operation that runs before its node
// settled wait rather than fail: the binding still names the address it
// moves from, or no address while the node's two writes land. It never
// writes DNS, so recovery may run it early.
func addressNotSettled(op *RouteOperation, current RouteBinding, err error) error {
	if errors.Is(err, errUnconfirmedNodeAddress) {
		return &routeNoticeError{err: ErrPending, message: "Waiting for the node to settle on " + op.Route.Binding.Address + "."}
	}
	if err != nil || current.Address != op.PreviousAddress || current.Address == op.Route.Binding.Address {
		return nil
	}
	current.Address = op.Route.Binding.Address
	if !reflect.DeepEqual(current, op.Route.Binding) {
		return nil
	}
	return &routeNoticeError{err: ErrPending, message: "Waiting for the node to settle on " + op.Route.Binding.Address + "."}
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

// NodesBehindAddress lists, across organizations, at most limit nodes in
// direct ingress mode with a confirmed address that a follow left behind:
// the node's server connection names another address, its inventory
// doesn't offer that address, or a route on the connection names another
// address and no operation is moving it.
func (s *PgStore) NodesBehindAddress(ctx context.Context, limit int) ([]*nodes.Node, error) {
	rows, err := s.db.Query(ctx, `SELECT n.id,n.org_id FROM loomplane_nodes n
 JOIN loomplane_cloud_connections c ON c.org_id=n.org_id AND c.details->>'node_id'=n.id AND c.cloud='server' AND c.status='ready'
 WHERE n.revoked_at IS NULL AND n.ingress_mode='direct' AND n.public_address<>'' AND n.public_address_confirmed_at IS NOT NULL
 AND (coalesce(c.details->>'advertise_host','')<>n.public_address
  OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(coalesce(n.inventory->'public_addresses','[]'::jsonb)) a(address) WHERE a.address=n.public_address)
  OR EXISTS(SELECT 1 FROM loomplane_domain_routes r WHERE r.org_id=n.org_id AND r.connection_id=c.id AND r.status IN ('ready','routing_pending') AND r.backend='server'
   AND coalesce(r.body->'binding'->>'address','')<>n.public_address
   AND NOT EXISTS(SELECT 1 FROM loomplane_route_operations o WHERE o.org_id=r.org_id AND o.route_id=r.id AND o.status IN ('pending','running','waiting'))))
 GROUP BY n.id,n.org_id ORDER BY n.id LIMIT $1`, limit)
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
