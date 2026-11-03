package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// HostAccess is how a Client reaches its host: the Docker daemon, the
// host's network and the few read-only facts Loomplane probes. A Client
// touches the host only through it.
type HostAccess interface {
	// DialContext opens TCP from the host's network.
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	// DialDocker opens a raw connection to the configured daemon socket.
	DialDocker(ctx context.Context) (net.Conn, error)
	// FencedRequest runs one Docker request through the host's effect fence.
	FencedRequest(ctx context.Context, mode string, f EffectFence, daemon, socket, method, path string, body io.Reader, headers http.Header) (*http.Response, error)
	CgroupLimits(ctx context.Context) (cpuMillis, memoryBytes int64, err error)
	GPUProbe(ctx context.Context) ([]byte, error) // the JSON gpuProbeCommand prints
	Filesystem(ctx context.Context, root string) (FilesystemCapacity, error)
	IngressListeners(ctx context.Context) ([]int, error)
	Close() error
}

// NodeStreams opens streams to joined nodes. The nodes hub satisfies it;
// this package never imports it.
type NodeStreams interface {
	Open(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error)
}

// NodeTarget is the joined node a node-backed connection reaches.
type NodeTarget struct {
	OrgID, NodeID      string
	IdentityGeneration int64
}

// errNodeAccessPending refuses node connections until node access exists.
// It is temporary: nodeAccess replaces it.
var errNodeAccessPending = errors.New("reaching Docker through a node is not available yet")

// Connect reaches the host cfg describes: over pinned SSH, or through the
// node's streams when cfg names a node.
func Connect(ctx context.Context, cfg *Config, streams NodeStreams) (*Client, error) {
	return connect(ctx, cfg, streams, func(ctx context.Context, cfg *Config) (HostAccess, error) {
		access, err := dialSSH(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return access, nil
	})
}

// connect is Connect with the SSH dialer injected.
func connect(ctx context.Context, cfg *Config, streams NodeStreams, ssh func(context.Context, *Config) (HostAccess, error)) (*Client, error) {
	if cfg == nil {
		return nil, ErrInvalid
	}
	if cfg.Node != nil {
		if streams == nil {
			return nil, fmt.Errorf("%w: a node connection needs the node streams", ErrInvalid)
		}
		return nil, errNodeAccessPending
	}
	access, err := ssh(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newClient(cfg, access), nil
}

// newClient forwards the client's Docker HTTP over access.
func newClient(cfg *Config, access HostAccess) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return access.DialDocker(ctx)
	}, ResponseHeaderTimeout: 30 * time.Second, MaxIdleConnsPerHost: 4}
	return &Client{Config: cfg, access: access, transport: transport, HTTP: &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
