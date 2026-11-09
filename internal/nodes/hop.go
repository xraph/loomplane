package nodes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// HopPath is where a replica serves the hop to the sessions it holds.
const HopPath = "/_/nodes/hop"

// The hop's request headers and values. A hop is a GET that carries the node,
// the org, a payload (the stream header JSON for an open, or a control line
// for a notify), a timestamp and an HMAC over all of them. An open upgrades
// the connection and splices it onto a local stream; a notify answers 204.
const (
	hdrHopNode      = "X-Loomplane-Node"
	hdrHopOrg       = "X-Loomplane-Org"
	hdrHopKind      = "X-Loomplane-Hop-Kind"
	hdrHopPayload   = "X-Loomplane-Header"
	hdrHopTimestamp = "X-Loomplane-Timestamp"
	hdrHopAuth      = "X-Loomplane-Hop-Auth"
	// hdrHopRefusal says why the node refused a hop's stream, beyond its
	// reason: "not-allowed" for an endpoint it doesn't allow. A replica
	// that doesn't know the header reads the plain refusal.
	hdrHopRefusal = "X-Loomplane-Refusal"

	hopUpgrade      = "loomplane-node-hop"
	hopKindOpen     = "open"
	hopKindNotify   = "notify"
	hopNotifyPrefix = "notify\n"
	hopMaxAge       = 30 * time.Second
	hopTimeout      = 10 * time.Second
	hopMaxPayload   = nodeproto.MaxHeaderSize + 1024
)

// hopMAC is HMAC-SHA256(secret, replica|node|org|ts|hex(sha256(payload))).
// replica is the target's session address, so a hop signed for one replica
// is refused by every other.
func hopMAC(secret []byte, replica, node, org, ts string, payload []byte) []byte {
	sum := sha256.Sum256(payload)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(replica + "|" + node + "|" + org + "|" + ts + "|" + hex.EncodeToString(sum[:])))
	return m.Sum(nil)
}

// hopTarget reports a session address another replica can be reached at.
func hopTarget(replica string) bool {
	return strings.HasPrefix(replica, "http://") || strings.HasPrefix(replica, "https://")
}

// hopAllowed reports whether this replica may dial target: https always,
// plain http only with HopInsecure. Private hosts get no exception.
func (h *Hub) hopAllowed(target string) bool { return hopSchemeAllowed(target, h.cfg) }

func hopSchemeAllowed(target string, cfg HubConfig) bool {
	return strings.HasPrefix(target, "https://") || (cfg.HopInsecure && strings.HasPrefix(target, "http://"))
}

// hopRequest is a verified hop.
type hopRequest struct {
	kind    string
	node    string
	org     string
	payload []byte
}

// verifyHop checks a hop's MAC with hmac.Equal and its age, either way, and
// that its kind matches its payload.
func (h *Hub) verifyHop(r *http.Request) (*hopRequest, error) {
	if len(h.cfg.HopSecret) == 0 {
		return nil, errors.New("no hop secret")
	}
	q := &hopRequest{kind: r.Header.Get(hdrHopKind), node: r.Header.Get(hdrHopNode), org: r.Header.Get(hdrHopOrg)}
	ts := r.Header.Get(hdrHopTimestamp)
	enc := r.Header.Get(hdrHopPayload)
	if q.node == "" || ts == "" || len(enc) > base64.RawURLEncoding.EncodedLen(hopMaxPayload) {
		return nil, errors.New("missing or oversized fields")
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, errors.New("bad payload encoding")
	}
	mac, err := hex.DecodeString(r.Header.Get(hdrHopAuth))
	if err != nil || !hmac.Equal(mac, hopMAC(h.cfg.HopSecret, h.self, q.node, q.org, ts, payload)) {
		return nil, errors.New("bad MAC")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil, errors.New("bad timestamp")
	}
	if age := h.now().Sub(time.Unix(sec, 0)); age > hopMaxAge || age < -hopMaxAge {
		return nil, errors.New("timestamp outside 30 seconds")
	}
	switch q.kind {
	case hopKindOpen:
		if q.org == "" || bytes.HasPrefix(payload, []byte(hopNotifyPrefix)) {
			return nil, errors.New("open without an org or with a notify payload")
		}
	case hopKindNotify:
		if !bytes.HasPrefix(payload, []byte(hopNotifyPrefix)) {
			return nil, errors.New("notify without a notify payload")
		}
	default:
		return nil, errors.New("unknown kind")
	}
	if !h.firstUse(hex.EncodeToString(mac), time.Unix(sec, 0).Add(hopMaxAge)) {
		return nil, errors.New("replayed")
	}
	q.payload = payload
	return q, nil
}

// firstUse records a hop's MAC until it could no longer pass the age check,
// and reports whether this is its first use. The MAC names this replica, so
// memory here is enough: no other replica would accept it.
func (h *Hub) firstUse(mac string, until time.Time) bool {
	now := h.now()
	h.hopMu.Lock()
	defer h.hopMu.Unlock()
	if h.hopSeen == nil {
		h.hopSeen = map[string]time.Time{}
	}
	for k, exp := range h.hopSeen {
		if now.After(exp) {
			delete(h.hopSeen, k)
		}
	}
	if _, ok := h.hopSeen[mac]; ok {
		return false
	}
	h.hopSeen[mac] = until
	return true
}

// HopHandler serves HopPath: other replicas reach the sessions this one holds
// through it. Every request is authenticated with the hop secret first.
func (h *Hub) HopHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := h.verifyHop(r)
		if err != nil {
			h.log.Warn("nodes: refusing a hop", "remote", r.RemoteAddr, "reason", err.Error())
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if h.store == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		if q.kind == hopKindNotify {
			h.serveNotify(w, q)
			return
		}
		h.serveOpen(w, r, q)
	})
}

func (h *Hub) serveNotify(w http.ResponseWriter, q *hopRequest) {
	msg, err := nodeproto.DecodeControl(bytes.TrimSpace(q.payload[len(hopNotifyPrefix):]))
	if err != nil || !notifiable(msg) {
		http.Error(w, "not a control plane message", http.StatusBadRequest)
		return
	}
	s := h.local(q.node)
	if s == nil {
		http.Error(w, "no session here", http.StatusServiceUnavailable)
		return
	}
	s.deliver(msg)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) serveOpen(w http.ResponseWriter, r *http.Request, q *hopRequest) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), hopUpgrade) {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	var hdr nodeproto.StreamHeader
	if err := json.Unmarshal(q.payload, &hdr); err != nil || hdr.OrgID != q.org {
		http.Error(w, "bad stream header", http.StatusBadRequest)
		return
	}
	st, err := h.open(r.Context(), q.org, q.node, hdr, false)
	var re *refusedError
	var off ErrNodeOffline
	switch {
	case err == nil:
	case errors.As(err, &re):
		if re.notAllowed {
			w.Header().Set(hdrHopRefusal, "not-allowed")
		}
		http.Error(w, re.reason, http.StatusConflict)
		return
	case errors.As(err, &off):
		http.Error(w, "no session here", http.StatusServiceUnavailable)
		return
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case errors.Is(err, ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	default:
		h.log.Warn("nodes: opening a stream for a hop", "node", q.node, "error", err)
		http.Error(w, "could not open the stream", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = st.Close()
		http.Error(w, "cannot upgrade", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		_ = st.Close()
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		_ = st.Close()
		return
	}
	if _, err := brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + hopUpgrade + "\r\n\r\n"); err != nil || brw.Flush() != nil {
		_ = conn.Close()
		_ = st.Close()
		return
	}
	go splice(&bufConn{Conn: conn, r: brw.Reader}, st)
}

// splice copies both ways until both sides finish. A clean end on one side
// half-closes the other, so a stream's EOF crosses the hop; an error closes
// both.
func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = a.Close(); _ = b.Close() }) }
	pipe := func(dst, src net.Conn) {
		defer wg.Done()
		if _, err := io.Copy(dst, src); err != nil {
			closeBoth()
			return
		}
		closeWrite(dst)
	}
	wg.Add(2)
	go pipe(a, b)
	go pipe(b, a)
	wg.Wait()
	closeBoth()
}

// closeWrite half-closes c when it can, else closes it.
func closeWrite(c net.Conn) {
	type halfCloser interface{ CloseWrite() error }
	if hc, ok := c.(halfCloser); ok && hc.CloseWrite() == nil {
		return
	}
	_ = c.Close() // a yamux stream's Close is already a half close
}

// bufConn is a net.Conn whose first bytes come from a buffered reader.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// CloseWrite half-closes the connection underneath, when it can.
func (c *bufConn) CloseWrite() error {
	type halfCloser interface{ CloseWrite() error }
	if hc, ok := c.Conn.(halfCloser); ok {
		return hc.CloseWrite()
	}
	return c.Close()
}

// hopDial connects to the replica at target, with TLS for an https target.
func hopDial(ctx context.Context, target string, cfg HubConfig) (net.Conn, error) {
	if !hopSchemeAllowed(target, cfg) {
		return nil, ErrHopInsecure
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	addr := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		addr = net.JoinHostPort(u.Hostname(), port)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return conn, nil
	}
	tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12, RootCAs: cfg.HopRootCAs})
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

// hopDo sends one signed hop to the replica at target. On 101 it returns the
// upgraded connection; otherwise it returns the status, up to 1 KiB of the
// body and the response's refusal header, and closes the connection.
func (h *Hub) hopDo(ctx context.Context, target, node, org, kind string, payload []byte) (upgraded net.Conn, status int, body, refusal string, err error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, hopTimeout)
		defer cancel()
	}
	conn, err := hopDial(ctx, target, h.cfg)
	if err != nil {
		return nil, 0, "", "", err
	}
	defer func() {
		if upgraded == nil {
			_ = conn.Close()
		}
	}()
	// Cancelling ctx expires the deadline, which unblocks the exchange.
	stop := context.AfterFunc(ctx, func() {
		if conn.SetDeadline(time.Unix(1, 0)) != nil {
			_ = conn.Close()
		}
	})
	defer stop()
	if dl, ok := ctx.Deadline(); ok {
		if derr := conn.SetDeadline(dl); derr != nil {
			return nil, 0, "", "", derr
		}
	}
	// The target comes from a session row a replica wrote, and the request is
	// signed for that replica alone.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(target, "/")+HopPath, http.NoBody) //nolint:gosec // see above
	if err != nil {
		return nil, 0, "", "", err
	}
	ts := strconv.FormatInt(h.now().Unix(), 10)
	req.Header.Set(hdrHopNode, node)
	req.Header.Set(hdrHopOrg, org)
	req.Header.Set(hdrHopKind, kind)
	req.Header.Set(hdrHopPayload, base64.RawURLEncoding.EncodeToString(payload))
	req.Header.Set(hdrHopTimestamp, ts)
	req.Header.Set(hdrHopAuth, hex.EncodeToString(hopMAC(h.cfg.HopSecret, target, node, org, ts, payload)))
	if kind == hopKindOpen {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", hopUpgrade)
	}
	if werr := req.Write(conn); werr != nil {
		return nil, 0, "", "", werr
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, 0, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if rerr != nil {
			raw = nil
		}
		return nil, resp.StatusCode, strings.TrimSpace(string(raw)), resp.Header.Get(hdrHopRefusal), nil
	}
	if !stop() {
		return nil, 0, "", "", ctx.Err()
	}
	if derr := conn.SetDeadline(time.Time{}); derr != nil {
		return nil, 0, "", "", derr
	}
	return &bufConn{Conn: conn, r: br}, resp.StatusCode, "", "", nil
}

// hopOpen opens a stream through the replica that holds the node's session.
func (h *Hub) hopOpen(ctx context.Context, target string, n *Node, hdr nodeproto.StreamHeader) (net.Conn, error) {
	payload, err := json.Marshal(hdr)
	if err != nil {
		return nil, err
	}
	conn, status, body, refusal, err := h.hopDo(ctx, target, n.ID, n.OrgID, hopKindOpen, payload)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		h.log.Warn("nodes: reaching the replica that holds a node", "node", n.ID, "replica", target, "error", err)
		return nil, offline(n)
	}
	switch status {
	case http.StatusSwitchingProtocols:
		return conn, nil
	case http.StatusConflict:
		reason := sanitizeReason(body)
		if reason == "" {
			reason = "no reason given"
		}
		return nil, &refusedError{reason: reason, notAllowed: refusal == "not-allowed"}
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusServiceUnavailable:
		return nil, offline(n)
	case http.StatusBadRequest:
		return nil, fmt.Errorf("%w: %s", ErrInvalid, body)
	}
	return nil, fmt.Errorf("nodes: the replica that holds node %s answered %d", n.Name, status)
}

// notifyRemote sends msg through the hop to the replica the session row
// names, when that is another replica.
func (h *Hub) notifyRemote(nodeID string, msg any) {
	if h.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hopTimeout)
	defer cancel()
	row, err := h.store.Session(ctx, nodeID)
	if err != nil || row.Replica == h.self || !hopTarget(row.Replica) {
		return
	}
	if !h.hopAllowed(row.Replica) {
		h.log.Warn("nodes: not notifying a replica over plain http", "node", nodeID, "replica", row.Replica)
		return
	}
	var line bytes.Buffer
	line.WriteString(hopNotifyPrefix)
	if err = nodeproto.EncodeControl(&line, msg); err != nil {
		return
	}
	_, status, body, _, err := h.hopDo(ctx, row.Replica, nodeID, "", hopKindNotify, line.Bytes())
	if err != nil || status != http.StatusNoContent {
		h.log.Warn("nodes: notifying a node through its replica", "node", nodeID, "replica", row.Replica, "status", status, "body", body, "error", err)
	}
}
