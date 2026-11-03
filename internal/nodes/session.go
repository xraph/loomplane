package nodes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// session is one node connection this replica holds: a yamux client over the
// node's WebSocket, with stream 0 as the control stream.
type session struct {
	hub    *Hub
	nodeID string
	orgID  string
	epoch  int64
	mux    *yamux.Session
	ctrl   net.Conn
	// conn is the node's WebSocket, for the close frame that follows Revoked.
	conn *websocket.Conn
	// remote is the address the node connected from, as this replica saw it.
	remote string

	out  chan any
	done chan struct{}
	once sync.Once
	// lastBeat is the hub clock's time of the last heartbeat, in unix nanos.
	lastBeat atomic.Int64

	// Touched only by the read loop.
	inventoried bool
	nodeVersion string
	protocol    int
}

func newSession(h *Hub, n *Node, epoch int64, mux *yamux.Session, ctrl net.Conn) *session {
	s := &session{hub: h, nodeID: n.ID, orgID: n.OrgID, epoch: epoch, mux: mux, ctrl: ctrl,
		out: make(chan any, controlQueue), done: make(chan struct{})}
	s.lastBeat.Store(h.now().UnixNano())
	return s
}

func (s *session) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// close ends the session. Closing the yamux session closes the WebSocket.
func (s *session) close() {
	s.once.Do(func() {
		close(s.done)
		_ = s.mux.Close()
	})
}

// closeUnauthorized ends a revoked node's session after Revoked was written.
// The close frame with the unauthorized code goes out behind it on the same
// connection, so a node that missed the message still learns it was revoked.
func (s *session) closeUnauthorized() {
	if s.conn != nil {
		msg := websocket.FormatCloseMessage(nodeproto.CloseUnauthorized, "unauthorized")
		_ = s.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second)) //nolint:errcheck // the session closes either way
	}
	s.close()
}

func (s *session) closeAfter(d time.Duration) { time.AfterFunc(d, s.close) }

// writeNow writes one control message on stream 0. Only Accept calls it,
// before the writer starts.
func (s *session) writeNow(msg any) error {
	if err := s.ctrl.SetWriteDeadline(time.Now().Add(controlWriteWait)); err != nil {
		return err
	}
	if err := nodeproto.EncodeControl(s.ctrl, msg); err != nil {
		return err
	}
	return s.ctrl.SetWriteDeadline(time.Time{})
}

// deliver queues a control message for the node. A full queue means the node
// stopped reading, so the session is closed: on reconnect the node gets a
// fresh Narrow anyway. A Revoked closes the session once written, and at the
// latest after revokeGrace.
func (s *session) deliver(msg any) {
	if s.closed() {
		return
	}
	select {
	case s.out <- msg:
	default:
		s.hub.log.Warn("nodes: a node's control queue is full; closing its session", "node", s.nodeID)
		s.close()
		return
	}
	if _, ok := msg.(nodeproto.Revoked); ok {
		s.closeAfter(revokeGrace)
	}
}

func (s *session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case msg := <-s.out:
			if err := s.writeNow(msg); err != nil {
				s.hub.log.Info("nodes: writing to a node's control stream", "node", s.nodeID, "error", err)
				s.close()
				return
			}
			if _, ok := msg.(nodeproto.Revoked); ok {
				s.closeUnauthorized()
				return
			}
		}
	}
}

// readLoop handles what the node sends on stream 0 until it closes. A line
// that doesn't parse is logged and skipped; unknown types are ignored.
func (s *session) readLoop() {
	sc := bufio.NewScanner(s.ctrl)
	sc.Buffer(make([]byte, 0, 64<<10), maxControlLine)
	for sc.Scan() {
		msg, err := nodeproto.DecodeControl(sc.Bytes())
		if err != nil {
			s.hub.log.Debug("nodes: skipping a control line that doesn't parse", "node", s.nodeID, "error", err)
			continue
		}
		switch m := msg.(type) {
		case nodeproto.Hello:
			s.nodeVersion, s.protocol = nodeproto.BoundVersion(m.NodeVersion), m.Version
		case nodeproto.Heartbeat:
			s.lastBeat.Store(s.hub.now().UnixNano())
			s.hub.heartbeat(s, m)
		case nodeproto.InventoryReport:
			s.hub.inventory(s, m.Inventory.Bounded())
		case nodeproto.Allowed:
			s.hub.allowed(s, m.Endpoints)
		case nodeproto.NarrowApplied:
			s.hub.log.Debug("nodes: node applied a narrow", "node", s.nodeID, "endpoints", m.Endpoints)
		case nodeproto.IngressApplied:
			s.hub.ingressApplied(s, m.Mode)
		case nodeproto.Leave:
			s.hub.leave(s)
		}
	}
	if err := sc.Err(); err != nil && !s.closed() {
		s.hub.log.Info("nodes: a node's control stream ended", "node", s.nodeID, "error", err)
	}
}

// open opens a data stream, writes the header and waits for the node's
// verdict: 0 accepts; anything else is followed by a reason line.
func (s *session) open(ctx context.Context, hdr nodeproto.StreamHeader) (net.Conn, error) {
	st, err := s.mux.OpenStream()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(verdictTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := st.SetDeadline(deadline); err != nil {
		_ = st.Close()
		return nil, err
	}
	// Cancelling ctx expires the deadline, which unblocks the verdict read.
	stop := context.AfterFunc(ctx, func() {
		if st.SetDeadline(time.Unix(1, 0)) != nil {
			_ = st.Close()
		}
	})
	fail := func(err error) (net.Conn, error) {
		stop()
		_ = st.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if err := nodeproto.WriteHeader(st, hdr); err != nil {
		return fail(err)
	}
	var v [1]byte
	if _, err := io.ReadFull(st, v[:]); err != nil {
		return fail(err)
	}
	if v[0] != 0 {
		return fail(&refusedError{reason: readReason(st)})
	}
	if !stop() {
		return fail(ctx.Err())
	}
	if err := st.SetDeadline(time.Time{}); err != nil {
		return fail(err)
	}
	return st, nil
}

// readReason reads the node's reason line, at most 512 bytes.
func readReason(r io.Reader) string {
	var b strings.Builder
	var c [1]byte
	for b.Len() < 512 {
		if _, err := r.Read(c[:]); err != nil || c[0] == '\n' {
			break
		}
		b.WriteByte(c[0])
	}
	if reason := sanitizeReason(b.String()); reason != "" {
		return reason
	}
	return "no reason given"
}

// sanitizeReason drops control and non-printable runes and invalid UTF-8, so
// a hostile node cannot put escape sequences into logs or API errors.
func sanitizeReason(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	return strings.TrimSpace(s)
}

// register makes s the node's local session and closes an older one. It
// refuses s when a newer session is already registered.
func (h *Hub) register(s *session) bool {
	h.mu.Lock()
	old := h.sessions[s.nodeID]
	if old != nil && old.epoch > s.epoch {
		h.mu.Unlock()
		return false
	}
	h.sessions[s.nodeID] = s
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	return true
}

func (h *Hub) unregister(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[s.nodeID] == s {
		delete(h.sessions, s.nodeID)
	}
}

// local returns this replica's live session for the node, or nil.
func (h *Hub) local(nodeID string) *session {
	h.mu.Lock()
	s := h.sessions[nodeID]
	h.mu.Unlock()
	if s == nil || s.closed() {
		return nil
	}
	return s
}

func (h *Hub) markOnline(s *session) {
	ctx, cancel := opCtx()
	defer cancel()
	if err := h.store.SetStatus(ctx, s.nodeID, StatusOnline); err != nil {
		h.log.Warn("nodes: marking a node online", "node", s.nodeID, "error", err)
	}
	if err := h.store.Touch(ctx, s.nodeID, h.now().UTC(), nodeproto.Usage{}); err != nil {
		h.log.Warn("nodes: stamping a node's last seen time", "node", s.nodeID, "error", err)
	}
}

func (h *Hub) release(nodeID string, epoch int64) {
	ctx, cancel := opCtx()
	defer cancel()
	if err := h.store.ReleaseSession(ctx, nodeID, epoch); err != nil {
		h.log.Warn("nodes: releasing a session", "node", nodeID, "epoch", epoch, "error", err)
	}
}

// ended marks the node offline, in one statement that matches this
// session's epoch, so a newer session's status isn't clobbered. Then it
// releases the row.
func (h *Hub) ended(s *session) {
	ctx, cancel := opCtx()
	defer cancel()
	if err := h.store.MarkOfflineIfEpoch(ctx, s.nodeID, s.epoch); err != nil {
		h.log.Warn("nodes: marking a node offline", "node", s.nodeID, "error", err)
	}
	h.release(s.nodeID, s.epoch)
}

// heartbeat stamps last seen, and on the way checks that the node is still
// trusted and this session is still the current one, as a backstop for a
// revoke or takeover whose notice never arrived.
func (h *Hub) heartbeat(s *session, hb nodeproto.Heartbeat) {
	ctx, cancel := opCtx()
	defer cancel()
	if err := h.store.Touch(ctx, s.nodeID, h.now().UTC(), hb.Usage); err != nil {
		h.log.Warn("nodes: recording a heartbeat", "node", s.nodeID, "error", err)
	}
	n, err := h.store.GetByID(ctx, s.nodeID)
	if err != nil {
		return
	}
	if n.RevokedAt != nil || n.Status == StatusRevoked {
		s.deliver(nodeproto.Revoked{})
		return
	}
	cur, err := h.store.Session(ctx, s.nodeID)
	if errors.Is(err, ErrNotFound) || (err == nil && cur.Epoch != s.epoch) {
		h.log.Info("nodes: closing a superseded session", "node", s.nodeID, "epoch", s.epoch)
		s.close()
		return
	}
	if n.Status != StatusOnline {
		if err := h.store.SetStatus(ctx, s.nodeID, StatusOnline); err != nil {
			h.log.Warn("nodes: marking a node online", "node", s.nodeID, "error", err)
		}
	}
}

// inventory stores a report. Inventory.Endpoints is kept as part of the
// inventory and never written to Node.Endpoints. After the session's first
// report it sends Narrow with the stored endpoints, so a narrow made while
// the node was away applies, then fires OnOnline.
func (h *Hub) inventory(s *session, inv nodeproto.Inventory) {
	inv = withRemoteAddress(inv, s.remote)
	ctx, cancel := opCtx()
	defer cancel()
	old, err := h.store.GetByID(ctx, s.nodeID)
	if err != nil {
		h.log.Warn("nodes: reading a node for its inventory", "node", s.nodeID, "error", err)
		return
	}
	version, proto := old.Version, old.ProtocolVersion
	if s.nodeVersion != "" {
		version = s.nodeVersion
	}
	if s.protocol != 0 {
		proto = s.protocol
	}
	if err = h.store.ApplyInventory(ctx, s.nodeID, inv, version, proto); err != nil {
		if errors.Is(err, ErrNotFound) {
			s.deliver(nodeproto.Revoked{})
			return
		}
		h.log.Warn("nodes: storing an inventory", "node", s.nodeID, "error", err)
		return
	}
	n, err := h.store.GetByID(ctx, s.nodeID)
	if err != nil {
		h.log.Warn("nodes: reading a node after its inventory", "node", s.nodeID, "error", err)
		return
	}
	first := !s.inventoried
	s.inventoried = true
	if first {
		eps := n.Endpoints
		if eps == nil {
			eps = []nodeproto.Endpoint{}
		}
		s.deliver(nodeproto.Narrow{Endpoints: eps})
		// The ingress mode may have changed while the node was away.
		mode := n.IngressMode
		if mode == "" {
			mode = nodeproto.IngressNone
		}
		s.deliver(nodeproto.SetIngress{Mode: mode})
		// Sent now, so the node gets its grace before a mismatch shows.
		if err = h.store.MarkIngressSent(ctx, s.nodeID, h.now()); err != nil && !errors.Is(err, ErrNotFound) {
			h.log.Warn("nodes: recording that a node was sent its ingress mode", "node", s.nodeID, "error", err)
		}
	}
	if !inventoryEqual(old.Inventory, inv) {
		h.fireInventory(n, old.Inventory, inv)
	}
	if first {
		h.fireOnline(n)
	}
}

// ingressApplied records the ingress mode a node says it now runs. A mode
// this Loomplane doesn't know is logged and dropped.
func (h *Hub) ingressApplied(s *session, mode string) {
	switch mode {
	case nodeproto.IngressNone, nodeproto.IngressDirect, nodeproto.IngressRelay:
	default:
		h.log.Debug("nodes: skipping an ingress mode this Loomplane doesn't know", "node", s.nodeID)
		return
	}
	ctx, cancel := opCtx()
	defer cancel()
	// An answer from a superseded session doesn't count: its node may
	// have been sent a newer mode since.
	if err := h.store.RecordIngressApplied(ctx, s.nodeID, mode, s.epoch); err != nil && !errors.Is(err, ErrNotFound) {
		h.log.Warn("nodes: recording the ingress mode a node applied", "node", s.nodeID, "error", err)
	}
}

// allowed adds endpoints the owner allowed on the machine. Control, public
// and unknown endpoints are dropped. Every Allowed that could be weighed is
// answered with a Narrow of the stored set whose Ack names what was asked,
// taken or not, so the node can stop carrying it as pending. A store error
// that may pass sends nothing, and the node asks again on its next connect.
func (h *Hub) allowed(s *session, eps []nodeproto.Endpoint) {
	asked, add := []nodeproto.Endpoint{}, []nodeproto.Endpoint{}
	for _, e := range eps {
		if !slices.Contains(asked, e) {
			asked = append(asked, e)
		}
		if slices.Contains(nodeproto.AllowableEndpoints(), e) && !slices.Contains(add, e) {
			add = append(add, e)
		}
	}
	if len(asked) == 0 {
		return
	}
	ctx, cancel := opCtx()
	defer cancel()
	before, err := h.store.GetByID(ctx, s.nodeID)
	if err != nil {
		h.log.Warn("nodes: reading a node before allowing endpoints", "node", s.nodeID, "error", err)
		return
	}
	n := before
	if len(add) > 0 {
		got, aerr := h.store.AllowEndpoints(ctx, s.nodeID, add)
		switch {
		case errors.Is(aerr, ErrNotFound):
			// Revoked meanwhile: nothing taken, and the answer says so.
		case aerr != nil:
			h.log.Warn("nodes: adding endpoints a node allowed", "node", s.nodeID, "error", aerr)
			return
		default:
			n = got
		}
	}
	stored := n.Endpoints
	if stored == nil {
		stored = []nodeproto.Endpoint{}
	}
	s.deliver(nodeproto.Narrow{Endpoints: stored, Ack: asked})
	if slices.Equal(before.Endpoints, n.Endpoints) {
		return // nothing taken, or already allowed: nothing to record
	}
	names := make([]string, len(add))
	for i, e := range add {
		names[i] = string(e)
	}
	if h.svc != nil {
		h.svc.record(ctx, "node.allowed", security.Principal{OrgID: n.OrgID, UserID: n.ID}, *n, map[string]string{"endpoints": strings.Join(names, ",")})
	}
}

// leave revokes the node whose verified session asked, and no other. It then
// queues Revoked on that session; the write loop flushes it, sends the close
// frame and closes. Nothing else closes the session here, so no close can race
// the write.
func (h *Hub) leave(s *session) {
	if h.svc == nil {
		s.close()
		return
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := h.svc.Leave(ctx, s.nodeID); err != nil {
		h.log.Warn("nodes: revoking a node that asked to leave", "node", s.nodeID, "error", err)
		s.close()
		return
	}
	// The revoke's own notice may have gone to another replica or been
	// dropped; a second Revoked on this session is harmless.
	s.deliver(nodeproto.Revoked{})
}

// withRemoteAddress adds the address the node connected from to its public
// address candidates, after the ones it reported, when it is a public IP.
// Many cloud machines have their public IP translated to a private one, so
// no interface carries it. Behind a proxy the address is the proxy's; it
// never answers the address check with the node's nonce, so it is never
// confirmed.
func withRemoteAddress(inv nodeproto.Inventory, remote string) nodeproto.Inventory {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return inv
	}
	a, err := netip.ParseAddr(host)
	if err != nil || !nodeproto.PublicAddress(a) {
		return inv
	}
	ip := a.Unmap().String()
	if slices.Contains(inv.PublicAddresses, ip) {
		return inv
	}
	inv.PublicAddresses = append(slices.Clone(inv.PublicAddresses), ip)
	return inv.Bounded()
}

// inventoryEqual compares inventories by their wire encoding, so a nil slice
// and an empty one, which encode the same, count as equal.
func inventoryEqual(a, b nodeproto.Inventory) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}
