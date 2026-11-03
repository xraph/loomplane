package cloudinfra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/pkg/nodeproto"
	"github.com/xraph/loomplane/pkg/provider"
)

// errProxyPortsTaken is a check whose proxy couldn't start.
var errProxyPortsTaken = errors.New("the node's proxy can't start")

// ErrNoPublicAddress is an address check that no public address of the
// node answered. The node runs without direct ingress.
var ErrNoPublicAddress = errors.New(nodes.NoPublicAddressDetail)

const (
	// nodeAddressTimeout bounds each fetch of the nonce.
	nodeAddressTimeout = 5 * time.Second
	// maxNodeNonceBody is how much of an answer is read. A nonce is 32 bytes.
	maxNodeNonceBody = 256
	// nodeNonceRestoreTimeout bounds taking the temporary route away, which
	// runs even after the caller's context ends.
	nodeNonceRestoreTimeout = 30 * time.Second
	// adoptedNodeConfirmWait bounds how long an adoption waits for its
	// server to be ready before checking the node's address.
	adoptedNodeConfirmWait = 3 * time.Minute
)

// NodeIngress records how traffic reaches a node. *nodes.Service satisfies it.
type NodeIngress interface {
	SetIngress(ctx context.Context, nodeID, mode, publicAddress string) error
}

// NodeAddressWriter writes a node connection's confirmed address.
// *connections.Service satisfies it.
type NodeAddressWriter interface {
	SetNodeAdvertiseHost(ctx context.Context, orgID, connectionID, address string) error
}

// nodeNonceTarget is the server whose proxy serves an address check.
type nodeNonceTarget struct {
	OrgID      string
	ServerID   string
	Connection *connections.Connection
	Credential []byte
}

// notPublic are ranges netip counts as global unicast that can't be a
// node's public address: this network, CGNAT, IETF protocol assignments,
// benchmarking, the reserved 240/4, NAT64, 6to4 and IPv4-compatible IPv6.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("::/96"),
}

// publicAddress reports an address Loomplane may fetch from: public
// unicast, outside every range in notPublic.
func publicAddress(a netip.Addr) bool {
	a = a.Unmap()
	if a.Zone() != "" || !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// publicNodeAddresses keeps the candidates worth fetching from outside:
// public unicast IP addresses, IPv4 first and then IPv6, each once, in the
// order the node gave them. Private, loopback, link-local, CGNAT,
// unspecified, multicast and reserved addresses, IPv6 unique local and
// transition ones, and anything that isn't an IP address are left out.
func publicNodeAddresses(candidates []string) []string {
	var v4, v6 []string
	for _, c := range candidates {
		a, err := netip.ParseAddr(strings.TrimSpace(c))
		if err != nil || !publicAddress(a) {
			continue
		}
		a = a.Unmap()
		s := a.String()
		if a.Is4() && !slices.Contains(v4, s) {
			v4 = append(v4, s)
		} else if a.Is6() && !slices.Contains(v6, s) {
			v6 = append(v6, s)
		}
	}
	return append(v4, v6...)
}

// newNodeNonce is 16 random bytes in hex.
func newNodeNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// nodeAddressURL is where the control plane fetches the nonce: plain HTTP
// on port 80 of the address.
func nodeAddressURL(address, nonce string) string {
	host := address
	if strings.Contains(address, ":") {
		host = "[" + address + "]"
	}
	return "http://" + host + server.NodeAddressPath + nonce
}

// newNodeAddressClient fetches nonces. It follows no redirect, uses no
// proxy and keeps no connection. control, when set, checks each address
// it dials.
func newNodeAddressClient(control func(network, address string, c syscall.RawConn) error) *http.Client {
	return &http.Client{
		Timeout:       nodeAddressTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: nodeAddressTimeout, Control: control}).DialContext,
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: nodeAddressTimeout,
		},
	}
}

// dialPublicOnly refuses to dial anything publicAddress refuses, so the
// fetch can't reach an address the candidate filter would have dropped.
func dialPublicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if a, parseErr := netip.ParseAddr(host); parseErr != nil || !publicAddress(a) {
		return fmt.Errorf("refusing to fetch an address check from %s", host)
	}
	return nil
}

var nodeAddressClient = newNodeAddressClient(dialPublicOnly)

// fetchNodeNonce GETs url from a public address and returns at most
// maxNodeNonceBody bytes of a 200 answer. Anything else, a redirect
// included, is an error.
func fetchNodeNonce(ctx context.Context, url string) ([]byte, error) {
	return fetchNodeNonceWith(ctx, nodeAddressClient, url)
}

func fetchNodeNonceWith(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxNodeNonceBody))
}

// ConfirmNodeAddress finds the public address a node answers on. The node's
// own proxy serves a fresh nonce on port 80 through the fenced ingress
// apply, the control plane fetches it from each public candidate in turn,
// public IPv4 first, and the temporary route is taken away again, whatever
// the outcome. No control message is involved.
//
// The first address that answers with the nonce becomes the connection's
// advertise_host and the node's confirmed public address, in direct
// ingress mode. When none answers, the node is set to ingress mode none,
// its detail says so, and the error is ErrNoPublicAddress. Any other error,
// such as a server that isn't ready or a proxy that can't be changed now,
// changes nothing and can be retried.
func (s *Service) ConfirmNodeAddress(ctx context.Context, org, connectionID string, candidates []string) (string, error) {
	conn, nodeID, found, err := s.checkNodeAddress(ctx, org, connectionID, candidates)
	if err != nil {
		return "", err
	}
	if found == "" {
		if noneErr := s.NodeIngress.SetIngress(ctx, nodeID, nodeproto.IngressNone, ""); noneErr != nil {
			return "", noneErr
		}
		return "", ErrNoPublicAddress
	}
	if settleErr := s.settleNodeAddress(ctx, conn, nodeID, found, ""); settleErr != nil {
		return "", settleErr
	}
	return found, nil
}

// checkNodeAddress is ConfirmNodeAddress without the settling: it returns
// the node's server connection, the node and the first public candidate
// that answered with the nonce, "" when none did. It changes nothing but
// the proxy, and only while the check runs.
func (s *Service) checkNodeAddress(ctx context.Context, org, connectionID string, candidates []string) (conn *connections.Connection, nodeID, found string, err error) {
	if s.NodeIngress == nil || s.NodeAddresses == nil {
		return nil, "", "", fmt.Errorf("%w: node ingress isn't wired", ErrInvalid)
	}
	conn, raw, err := s.Connections.Credential(ctx, org, connectionID)
	if err != nil {
		return nil, "", "", err
	}
	nodeID = conn.Details["node_id"]
	if conn.OrgID != org || conn.Cloud != connections.Server || conn.Method != connections.MethodNode || nodeID == "" {
		return nil, "", "", fmt.Errorf("%w: only a node's server connection has its address checked", ErrInvalid)
	}
	v, err := s.nodeServer(ctx, conn)
	if err != nil {
		return nil, "", "", err
	}
	if addresses := publicNodeAddresses(candidates); len(addresses) > 0 {
		nonce, nonceErr := newNodeNonce()
		if nonceErr != nil {
			return nil, "", "", nonceErr
		}
		target := nodeNonceTarget{OrgID: org, ServerID: v.ID, Connection: conn, Credential: raw}
		err = s.serveNodeNonce(ctx, target, nonce, func(ctx context.Context) error {
			found = s.firstAnswering(ctx, addresses, nonce)
			return ctx.Err()
		})
		if errors.Is(err, errProxyPortsTaken) {
			// The admin reads why on the node; nothing else changes.
			if d, ok := s.NodeIngress.(nodeIngressDetail); ok {
				if detailErr := d.RecordIngressDetail(ctx, nodeID, ProxyPortsTakenDetail); detailErr != nil {
					err = errors.Join(err, detailErr)
				}
			}
		}
		if err != nil {
			return nil, "", "", err
		}
	}
	return conn, nodeID, found, nil
}

// settleNodeAddress makes a confirmed address the node's: the connection
// names it, then the node marks it confirmed. A route reaches the node only
// while both name the same address, so until the second write it reaches
// none; when that write fails, the connection goes back to the address it
// named, which the node still holds. current is the address the node holds
// now: a write that is already in place isn't made again, so a node keeps
// the time its address was confirmed. "" always writes both.
func (s *Service) settleNodeAddress(ctx context.Context, conn *connections.Connection, nodeID, address, current string) error {
	previous := conn.Details["advertise_host"]
	if previous != address {
		if err := s.NodeAddresses.SetNodeAdvertiseHost(ctx, conn.OrgID, conn.ID, address); err != nil {
			return err
		}
	}
	if current == address {
		return nil
	}
	err := s.NodeIngress.SetIngress(ctx, nodeID, nodeproto.IngressDirect, address)
	if err != nil && previous != address && net.ParseIP(previous) != nil {
		backCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeAddressRollbackWait)
		if backErr := s.NodeAddresses.SetNodeAdvertiseHost(backCtx, conn.OrgID, conn.ID, previous); backErr != nil {
			err = errors.Join(err, backErr)
		}
		cancel()
	}
	return err
}

// firstAnswering is the first address whose answer is exactly the nonce,
// or "" when none is.
func (s *Service) firstAnswering(ctx context.Context, addresses []string, nonce string) string {
	for _, a := range addresses {
		if ctx.Err() != nil {
			return ""
		}
		fetchCtx, cancel := context.WithTimeout(ctx, nodeAddressTimeout)
		body, err := s.fetchNodeNonce(fetchCtx, nodeAddressURL(a, nonce))
		cancel()
		if err == nil && string(body) == nonce {
			return a
		}
	}
	return ""
}

// nodeServer is the ready Cloud server that runs on conn. A server that
// is still being adopted is ErrPending: its proxy must not start before the
// adoption has checked that ports 80 and 443 are free.
func (s *Service) nodeServer(ctx context.Context, conn *connections.Connection) (*Server, error) {
	st, ok := s.Store.(interface {
		RuntimeServers(ctx context.Context, org string) ([]*Server, error)
	})
	if !ok {
		return nil, fmt.Errorf("%w: this store can't list servers by connection", ErrInvalid)
	}
	servers, err := st.RuntimeServers(ctx, conn.OrgID)
	if err != nil {
		return nil, err
	}
	for _, v := range servers {
		if v.RuntimeConnectionID != conn.ID {
			continue
		}
		if v.Status != "ready" {
			return nil, fmt.Errorf("%w: server %s is %s", ErrPending, v.Name, v.Status)
		}
		if v.ResourceID != "" && v.ResourceID != conn.Details["docker_id"] {
			return nil, provider.ErrInfrastructureIdentity
		}
		return v, nil
	}
	return nil, fmt.Errorf("%w: no server of a Cloud runs on connection %s", ErrNotFound, conn.Name)
}

// nodeProxy is a server's proxy as an address check uses it: Caddy over
// its loopback admin, or a fake admin in tests.
type nodeProxy interface {
	Ensure(ctx context.Context) (*server.IngressSnapshot, error)
	Read(ctx context.Context) (*server.IngressSnapshot, error)
	Apply(ctx context.Context, expected *server.IngressSnapshot, v server.IngressConfig) (*server.IngressSnapshot, error)
}

// clientProxy is the server's Caddy through the fenced ingress admin.
type clientProxy struct {
	c           *server.Client
	org, server string
}

func (p clientProxy) Ensure(ctx context.Context) (*server.IngressSnapshot, error) {
	return server.EnsureIngress(ctx, p.c, p.org, p.server)
}

func (p clientProxy) Read(ctx context.Context) (*server.IngressSnapshot, error) {
	return server.ReadIngress(ctx, p.c, p.org, p.server)
}

func (p clientProxy) Apply(ctx context.Context, expected *server.IngressSnapshot, v server.IngressConfig) (*server.IngressSnapshot, error) {
	return server.ApplyIngress(ctx, p.c, expected, v)
}

// proxyState is what a server's proxy runs, held against the config
// Loomplane stored for it.
type proxyState int

const (
	// proxyAsStored runs the stored config.
	proxyAsStored proxyState = iota
	// proxyWithAddressCheck runs the stored config plus address check
	// routes, from a check in flight or one that couldn't take its route
	// off again.
	proxyWithAddressCheck
	// proxyChanged runs something else, such as a route change in flight.
	proxyChanged
)

// proxyAgainst says what live is next to stored.
func proxyAgainst(stored, live []byte) proxyState {
	if server.SameIngressConfig(stored, live) {
		return proxyAsStored
	}
	if stripped, changed := server.WithoutAddressChecks(live); changed && server.SameIngressConfig(stored, stripped) {
		return proxyWithAddressCheck
	}
	return proxyChanged
}

// ingressExpected is the snapshot a route change applies its config over.
// A proxy that runs previous's config, or that config plus an address
// check left behind, is taken as it is now: the change's complete config
// replaces it, check and all, so a lost check never blocks the change.
// Anything else is left to the change's own compare-and-swap.
func ingressExpected(previous, actual *server.IngressSnapshot) *server.IngressSnapshot {
	if previous == nil || actual == nil || previous.ContainerID != actual.ContainerID || proxyAgainst(previous.Config, actual.Config) == proxyChanged {
		return previous
	}
	return actual
}

// ProxyPortsTakenDetail is what a node shows when its proxy can't start
// because another program holds port 80 or 443.
const ProxyPortsTakenDetail = "ports 80/443 on this machine are taken by another program, so Loomplane's proxy can't start; free them for direct ingress"

// serveNodeNonceThroughIngress serves nonce through the server's proxy
// while check runs, then takes it away. It holds the server's ingress
// lock, the one route changes take, for all of it, and starts a new fence
// generation like any ingress effect.
func (s *Service) serveNodeNonceThroughIngress(ctx context.Context, t nodeNonceTarget, nonce string, check func(context.Context) error) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	return s.Store.WithOperationLock(ctx, "ingress/"+t.OrgID+"/"+t.ServerID, func() error {
		state, err := st.BeginServerIngress(ctx, t.OrgID, t.ServerID)
		if err != nil {
			return err
		}
		cfg, err := server.ParseConfig(t.Credential, t.Connection.Details)
		if err != nil {
			return err
		}
		// The fence journal is the daemon's; ParseConfig doesn't carry it.
		cfg.Details["docker_id"] = t.Connection.Details["docker_id"]
		c, err := server.Connect(ctx, cfg, s.NodeStreams)
		if err != nil {
			return err
		}
		defer c.Close()
		c.RequireFence = true
		fence := server.EffectFence{Operation: t.OrgID + "/ingress/" + t.ServerID, Generation: state.Generation}
		ctx = server.WithEffectFence(ctx, fence)
		if fenceErr := c.ArmEffectFence(ctx, fence); fenceErr != nil {
			return fenceErr
		}
		save := func(ctx context.Context, state *IngressState) error {
			return st.SaveServerIngress(ctx, t.OrgID, t.ServerID, state)
		}
		return s.checkThroughProxy(ctx, clientProxy{c: c, org: t.OrgID, server: t.ServerID}, t, state, nonce, check, save)
	})
}

// checkThroughProxy runs check while p serves nonce. The proxy must run
// the config Loomplane last stored for the server, so a change in flight
// is never overwritten; an address check an earlier run left on it comes
// off first. The check's route is added to the stored config by
// compare-and-swap, and taken off again on a fresh context however the
// apply or the check ended, an apply whose outcome never came back
// included. When the server has a stored config, the snapshot the proxy
// ends on is saved with it.
func (s *Service) checkThroughProxy(ctx context.Context, p nodeProxy, t nodeNonceTarget, state *IngressState, nonce string, check func(context.Context) error, save func(context.Context, *IngressState) error) error {
	// A server without routes runs the initial config, which is what a
	// route's first apply expects to find.
	base := state.Config
	var live *server.IngressSnapshot
	var err error
	if state.Current == nil {
		base = server.IngressConfig{OrgID: t.OrgID, ServerID: t.ServerID, Version: "initial", Routes: []server.IngressRoute{}}
		live, err = p.Ensure(ctx)
	} else {
		live, err = p.Read(ctx)
	}
	if errors.Is(err, server.ErrIngressPortsTaken) {
		return fmt.Errorf("%w: %w", errProxyPortsTaken, err)
	}
	if err != nil {
		return err
	}
	stored, err := base.JSON()
	if err != nil {
		return err
	}
	switch proxyAgainst(stored, live.Config) {
	case proxyAsStored:
	case proxyWithAddressCheck:
		// An earlier check couldn't take its route off. It comes off now.
		if live, err = p.Apply(ctx, live, base); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: the server's proxy isn't running the config Loomplane last applied; finish its route changes first", ErrConflict)
	}
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeNonceRestoreTimeout)
	defer cancel()
	defer func() {
		if restoreErr := s.restoreProxy(restoreCtx, p, state, base, stored, save); restoreErr != nil {
			s.warn("Couldn't take a node's address check off its proxy; the next address check or route change on that server takes it off", restoreErr)
		}
	}()
	withCheck := base
	withCheck.AddressCheck = nonce
	if _, err = p.Apply(ctx, live, withCheck); err != nil {
		return err
	}
	return check(ctx)
}

// restoreProxy puts the stored config back where an address check left
// it. A proxy running anything but the stored config, with or without
// address checks, is left alone.
func (s *Service) restoreProxy(ctx context.Context, p nodeProxy, state *IngressState, base server.IngressConfig, stored []byte, save func(context.Context, *IngressState) error) error {
	current, err := p.Read(ctx)
	if err != nil {
		return err
	}
	switch proxyAgainst(stored, current.Config) {
	case proxyAsStored:
	case proxyWithAddressCheck:
		if current, err = p.Apply(ctx, current, base); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: the proxy changed during an address check", ErrConflict)
	}
	if state.Current == nil || (state.Current.ETag == current.ETag && state.Current.ContainerID == current.ContainerID) {
		return nil
	}
	state.Current = current
	return save(ctx, state)
}
