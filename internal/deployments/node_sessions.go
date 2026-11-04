package deployments

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// serverConnect is server.Connect's shape, which tests replace.
type serverConnect func(context.Context, *server.Config, server.NodeStreams) (*server.Client, error)

// A deploy that finds its node offline waits nodeReconnectWindow for it to
// come back, trying again every nodeReconnectEvery, before it fails. Run
// heartbeats the activity every 30 seconds meanwhile, and the window stays
// well inside the action activity's 3 minute heartbeat timeout.
const (
	nodeReconnectWindow = 2 * time.Minute
	nodeReconnectEvery  = 5 * time.Second
)

// nodeReconnect is how a session waits for an offline node. The zero value
// waits nodeReconnectWindow on the real clock; tests shorten it.
type nodeReconnect struct {
	window, every time.Duration
	// sleep waits d, or returns ctx's error when ctx ends first.
	sleep func(ctx context.Context, d time.Duration) error
}

func (r nodeReconnect) orDefault() nodeReconnect {
	if r.window == 0 {
		r.window = nodeReconnectWindow
	}
	if r.every <= 0 {
		r.every = nodeReconnectEvery
	}
	if r.sleep == nil {
		r.sleep = sleepContext
	}
	return r
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ServerClientFor reaches the Docker daemon a server connection names: over
// its pinned SSH server, or through streams when it is a node's. raw is the
// connection's credential, the node's marker for a node connection.
func ServerClientFor(ctx context.Context, conn *connections.Connection, raw []byte, streams server.NodeStreams) (*server.Client, error) {
	client, _, err := serverClientFor(ctx, conn, raw, streams, nil, nodeReconnect{})
	return client, err
}

// serverClientFor is ServerClientFor with the connect and the reconnect
// wait injected. For a node connection it also returns the streams it
// reached the node through, which remember why the node couldn't be
// reached.
func serverClientFor(ctx context.Context, conn *connections.Connection, raw []byte, streams server.NodeStreams, connect serverConnect, reconnect nodeReconnect) (*server.Client, *nodeSession, error) {
	if conn == nil || conn.Cloud != connections.Server {
		return nil, nil, errors.New("the connection isn't a server")
	}
	cfg, err := server.ParseConfig(raw, conn.Details)
	if err != nil {
		return nil, nil, err
	}
	// A marker only reaches the node of a node connection in its own
	// organization, and a node connection only through its marker.
	if (cfg.Node != nil) != (conn.Method == connections.MethodNode) || cfg.Node != nil && cfg.Node.OrgID != conn.OrgID {
		return nil, nil, fmt.Errorf("%w: the credential doesn't match the connection", server.ErrInvalid)
	}
	var session *nodeSession
	if cfg.Node != nil && streams != nil {
		session = &nodeSession{streams: streams, name: conn.Name, reconnect: reconnect.orDefault()}
		streams = session
	}
	if connect == nil {
		connect = server.Connect
	}
	client, err := connect(ctx, cfg, streams)
	if err != nil {
		return nil, nil, err
	}
	return client, session, nil
}

// nodeSession is the hub's streams for one node connection. It names an
// endpoint the node refused, which the hub's error leaves out, and keeps
// why the node can't be reached: Client.Check reads an unreachable daemon
// as a missing docker:access, not as its error. Within a deployment run,
// a node found offline gets reconnect's window to come back, once per
// session.
type nodeSession struct {
	streams   server.NodeStreams
	name      string // the connection's name, which adoption gives the node's
	reconnect nodeReconnect

	mu sync.Mutex
	// failed is the first failure that holds for the rest of the session:
	// the node stayed offline through the wait, or it doesn't allow an
	// endpoint, which only its owner can change.
	failed error
	// refused is the last other refusal, such as a dial target that
	// didn't answer, until a stream opens. It explains a failed check but
	// holds nothing back: the next open asks the node again.
	refused error
}

func (s *nodeSession) Open(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	conn, err := s.streams.Open(ctx, orgID, nodeID, hdr)
	if err == nil {
		s.opened()
		return conn, nil
	}
	var off nodes.ErrNodeOffline
	// Only a deploy waits: an API read that reaches the node answers now.
	// A session whose wait already ran out doesn't wait again.
	if errors.As(err, &off) && security.InDeploymentRun(ctx) && s.final() == nil {
		conn, err = s.awaitNode(ctx, orgID, nodeID, hdr, err)
		if err == nil {
			s.opened()
			return conn, nil
		}
	}
	switch {
	case errors.Is(err, nodes.ErrEndpointNotAllowed):
		err = &endpointRefusedError{node: s.name, endpoint: hdr.Endpoint, notAllowed: true, err: err}
	case errors.Is(err, nodes.ErrEndpointRefused):
		err = &endpointRefusedError{node: s.name, endpoint: hdr.Endpoint, err: err}
		s.mu.Lock()
		s.refused = err
		s.mu.Unlock()
		return nil, err
	case errors.As(err, &off):
	default:
		return nil, err
	}
	s.mu.Lock()
	if s.failed == nil {
		s.failed = err
	}
	s.mu.Unlock()
	return nil, err
}

// opened forgets the last refusal: the node answered since.
func (s *nodeSession) opened() {
	s.mu.Lock()
	s.refused = nil
	s.mu.Unlock()
}

// final is the failure that holds for the rest of the session, or nil.
func (s *nodeSession) final() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// awaitNode opens the stream again every reconnect.every until the node
// is back, the window runs out or ctx ends. It returns the stream, or the
// last error, still the hub's offline error when the node never came back.
// An ended ctx comes back joined to the offline error, so the deploy still
// says the node is offline.
func (s *nodeSession) awaitNode(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader, err error) (net.Conn, error) {
	var off nodes.ErrNodeOffline
	for waited := time.Duration(0); waited < s.reconnect.window && errors.As(err, &off); waited += s.reconnect.every {
		if sleepErr := s.reconnect.sleep(ctx, s.reconnect.every); sleepErr != nil {
			return nil, errors.Join(err, sleepErr)
		}
		var conn net.Conn
		if conn, err = s.streams.Open(ctx, orgID, nodeID, hdr); err == nil {
			return conn, nil
		}
	}
	return nil, err
}

// unreachable is why the node couldn't be reached, or nil: the session's
// final failure, else the last refusal since a stream opened. A nil
// session is an SSH server's, which this never explains.
func (s *nodeSession) unreachable() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	return s.refused
}

// endpointRefusedError is the hub's refusal, with the node and endpoint.
// notAllowed is set when the node refused because it doesn't allow the
// endpoint.
type endpointRefusedError struct {
	node       string
	endpoint   nodeproto.Endpoint
	notAllowed bool
	err        error
}

func (e *endpointRefusedError) Error() string {
	return fmt.Sprintf("node %s refused the %s endpoint: %v", e.node, e.endpoint, e.err)
}

func (e *endpointRefusedError) Unwrap() error { return e.err }
