package server

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// respTimeout bounds one RESP readiness probe.
const respTimeout = 5 * time.Second

// probeRESP sends PING to a Redis-protocol port published on the host's
// loopback, over the pinned SSH connection, as probeServer does for HTTP.
func probeRESP(ctx context.Context, c *Client, port int) error {
	return pingRESP(ctx, respTimeout, func(ctx context.Context) (net.Conn, error) {
		return c.ssh.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	})
}

// pingRESP is ready on +PONG, or on a -NOAUTH error: the server is up
// and wants a password the probe doesn't send. -LOADING, any other reply,
// a closed connection and silence past the timeout are not ready.
func pingRESP(ctx context.Context, timeout time.Duration, dial func(context.Context) (net.Conn, error)) error {
	return probeLine(ctx, timeout, dial, "PING", func(reply string) bool { return reply == "+PONG" || strings.HasPrefix(reply, "-NOAUTH") })
}

// probed reports whether a workload declares a readiness probe.
func probed(labels map[string]string) bool {
	return labels[owned.LabelHealth] != "" || labels[owned.LabelHealthProtocol] != ""
}

// healthProbe is the readiness probe a workload declares: a RESP PING when
// LabelHealthProtocol is "redis", otherwise an HTTP GET of LabelHealth.
func (r *Runtime) healthProbe(labels map[string]string) (func(context.Context, *Client, int) error, error) {
	switch protocol := labels[owned.LabelHealthProtocol]; protocol {
	case "":
		probe := r.Probe
		if probe == nil {
			probe = probeServer
		}
		return func(ctx context.Context, c *Client, port int) error {
			return probe(ctx, c, port, labels[owned.LabelHealth])
		}, nil
	case "memcached":
		return func(ctx context.Context, c *Client, port int) error {
			return pingMemcached(ctx, respTimeout, func(ctx context.Context) (net.Conn, error) {
				return c.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			})
		}, nil
	case "redis":
		if r.ProbeRESP != nil {
			return r.ProbeRESP, nil
		}
		return probeRESP, nil
	default:
		return nil, fmt.Errorf("unsupported readiness protocol %q", protocol)
	}
}
