package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// An SSH channel has no deadlines, so the context closes it instead.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err = io.WriteString(conn, "PING\r\n"); err != nil {
		return fmt.Errorf("send PING: %w", err)
	}
	reply, err := bufio.NewReader(io.LimitReader(conn, 512)).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read the PING reply: %w", err)
	}
	reply = strings.TrimRight(reply, "\r\n")
	if reply == "+PONG" || strings.HasPrefix(reply, "-NOAUTH") {
		return nil
	}
	return fmt.Errorf("the port answered PING with %.64q", reply)
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
	case "redis":
		if r.ProbeRESP != nil {
			return r.ProbeRESP, nil
		}
		return probeRESP, nil
	default:
		return nil, fmt.Errorf("unsupported readiness protocol %q", protocol)
	}
}
