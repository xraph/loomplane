package deployments

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// serverConnect is server.Connect's shape, which tests replace.
type serverConnect func(context.Context, *server.Config, server.NodeStreams) (*server.Client, error)

// ServerClientFor reaches the Docker daemon a server connection names: over
// its pinned SSH server, or through streams when it is a node's. raw is the
// connection's credential, the node's marker for a node connection.
func ServerClientFor(ctx context.Context, conn *connections.Connection, raw []byte, streams server.NodeStreams) (*server.Client, error) {
	client, _, err := serverClientFor(ctx, conn, raw, streams, nil)
	return client, err
}

// serverClientFor is ServerClientFor with the connect injected. For a node
// connection it also returns the streams it reached the node through, which
// remember why the node couldn't be reached.
func serverClientFor(ctx context.Context, conn *connections.Connection, raw []byte, streams server.NodeStreams, connect serverConnect) (*server.Client, *nodeSession, error) {
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
		session = &nodeSession{streams: streams, name: conn.Name}
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
// the first error that says the node can't be reached: Client.Check reads
// an unreachable daemon as a missing docker:access, not as its error.
type nodeSession struct {
	streams server.NodeStreams
	name    string // the connection's name, which adoption gives the node's

	mu     sync.Mutex
	failed error
}

func (s *nodeSession) Open(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	conn, err := s.streams.Open(ctx, orgID, nodeID, hdr)
	if err == nil {
		return conn, nil
	}
	var off nodes.ErrNodeOffline
	switch {
	case errors.Is(err, nodes.ErrEndpointRefused):
		err = &endpointRefusedError{node: s.name, endpoint: hdr.Endpoint, err: err}
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

// unreachable is why the node couldn't be reached, or nil. A nil session
// is an SSH server's, which this never explains.
func (s *nodeSession) unreachable() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// endpointRefusedError is the hub's refusal, with the node and endpoint.
type endpointRefusedError struct {
	node     string
	endpoint nodeproto.Endpoint
	err      error
}

func (e *endpointRefusedError) Error() string {
	return fmt.Sprintf("node %s refused the %s endpoint: %v", e.node, e.endpoint, e.err)
}

func (e *endpointRefusedError) Unwrap() error { return e.err }
