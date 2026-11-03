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
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/pkg/nodeproto"
	"github.com/xraph/loomplane/pkg/provider"
)

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

// cgnat is carrier-grade NAT space, 100.64.0.0/10. It isn't reachable from
// outside the carrier, and netip doesn't count it as private.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicNodeAddresses keeps the candidates worth fetching from outside:
// public unicast IP addresses, IPv4 first and then IPv6, each once, in the
// order the node gave them. Private, loopback, link-local, CGNAT,
// unspecified and multicast addresses, IPv6 unique local ones, and
// anything that isn't an IP address are left out.
func publicNodeAddresses(candidates []string) []string {
	var v4, v6 []string
	for _, c := range candidates {
		a, err := netip.ParseAddr(strings.TrimSpace(c))
		if err != nil || a.Zone() != "" {
			continue
		}
		a = a.Unmap()
		if !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a) {
			continue
		}
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

// nodeAddressClient fetches nonces. It follows no redirect, uses no proxy
// and keeps no connection.
var nodeAddressClient = &http.Client{
	Timeout:       nodeAddressTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: nodeAddressTimeout}).DialContext,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: nodeAddressTimeout,
	},
}

// fetchNodeNonce GETs url and returns at most maxNodeNonceBody bytes of a
// 200 answer. Anything else, a redirect included, is an error.
func fetchNodeNonce(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := nodeAddressClient.Do(req)
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
	if s.NodeIngress == nil || s.NodeAddresses == nil {
		return "", fmt.Errorf("%w: node ingress isn't wired", ErrInvalid)
	}
	conn, raw, err := s.Connections.Credential(ctx, org, connectionID)
	if err != nil {
		return "", err
	}
	nodeID := conn.Details["node_id"]
	if conn.OrgID != org || conn.Cloud != connections.Server || conn.Method != connections.MethodNode || nodeID == "" {
		return "", fmt.Errorf("%w: only a node's server connection has its address checked", ErrInvalid)
	}
	v, err := s.nodeServer(ctx, conn)
	if err != nil {
		return "", err
	}
	found := ""
	if addresses := publicNodeAddresses(candidates); len(addresses) > 0 {
		nonce, nonceErr := newNodeNonce()
		if nonceErr != nil {
			return "", nonceErr
		}
		target := nodeNonceTarget{OrgID: org, ServerID: v.ID, Connection: conn, Credential: raw}
		err = s.serveNodeNonce(ctx, target, nonce, func(ctx context.Context) error {
			found = s.firstAnswering(ctx, addresses, nonce)
			return ctx.Err()
		})
		if err != nil {
			return "", err
		}
	}
	if found == "" {
		if noneErr := s.NodeIngress.SetIngress(ctx, nodeID, nodeproto.IngressNone, ""); noneErr != nil {
			return "", noneErr
		}
		return "", ErrNoPublicAddress
	}
	// The connection names the address before the node marks it confirmed,
	// so a route never reads a confirmed node with an older address.
	if hostErr := s.NodeAddresses.SetNodeAdvertiseHost(ctx, org, conn.ID, found); hostErr != nil {
		return "", hostErr
	}
	if directErr := s.NodeIngress.SetIngress(ctx, nodeID, nodeproto.IngressDirect, found); directErr != nil {
		return "", directErr
	}
	return found, nil
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

// serveNodeNonceThroughIngress serves nonce through the server's proxy
// while check runs, then takes it away. It holds the server's ingress
// lock, the one route changes take, for all of it, and starts a new fence
// generation like any ingress effect. The live config must be the one
// Loomplane last stored for the server, so a change in flight is never
// overwritten: the check adds its route to that config, and removing it
// puts that config back. The removal runs on a fresh context, so a caller
// that has gone still leaves the proxy as it was.
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
		// A server without routes runs the initial config, which is what a
		// route's first apply expects to find.
		base := state.Config
		var live *server.IngressSnapshot
		if state.Current == nil {
			base = server.IngressConfig{OrgID: t.OrgID, ServerID: t.ServerID, Version: "initial", Routes: []server.IngressRoute{}}
			live, err = server.EnsureIngress(ctx, c, t.OrgID, t.ServerID)
		} else {
			live, err = server.ReadIngress(ctx, c, t.OrgID, t.ServerID)
		}
		if err != nil {
			return err
		}
		stored, err := base.JSON()
		if err != nil {
			return err
		}
		if !server.SameIngressConfig(stored, live.Config) {
			return fmt.Errorf("%w: the server's proxy isn't running the config Loomplane last applied; finish its route changes first", ErrConflict)
		}
		withCheck := base
		withCheck.AddressCheck = nonce
		checking, err := withCheck.JSON()
		if err != nil {
			return err
		}
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeNonceRestoreTimeout)
		defer cancel()
		// Whatever the apply did, even an outcome it never heard, the
		// check comes off before the lock goes.
		defer func() {
			if restoreErr := s.restoreNodeNonce(restoreCtx, st, c, t, state, base, stored, checking); restoreErr != nil {
				s.warn("Couldn't take a node's address check off its proxy; the next route change replaces it", restoreErr)
			}
		}()
		if _, err = server.ApplyIngress(ctx, c, live, withCheck); err != nil {
			return err
		}
		return check(ctx)
	})
}

// restoreNodeNonce puts the stored config back where the check left it.
// A proxy found running anything but the stored config or the check is
// left alone. When the server has a stored config, the snapshot it now
// runs is saved with it, so the next route change applies on top of it.
func (s *Service) restoreNodeNonce(ctx context.Context, st DomainStore, c *server.Client, t nodeNonceTarget, state *IngressState, base server.IngressConfig, stored, checking []byte) error {
	current, err := server.ReadIngress(ctx, c, t.OrgID, t.ServerID)
	if err != nil {
		return err
	}
	switch {
	case server.SameIngressConfig(stored, current.Config):
	case server.SameIngressConfig(checking, current.Config):
		if current, err = server.ApplyIngress(ctx, c, current, base); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: the proxy of server %s changed during an address check", ErrConflict, t.ServerID)
	}
	if state.Current == nil || (state.Current.ETag == current.ETag && state.Current.ContainerID == current.ContainerID) {
		return nil
	}
	state.Current = current
	return st.SaveServerIngress(ctx, t.OrgID, t.ServerID, state)
}
