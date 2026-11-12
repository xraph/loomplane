package catalogconnect

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"

	"github.com/xraph/loomplane/internal/recipes"
)

// FalkorDBProber checks supplied authentication, the supported module
// declaration and access to one fixed configuration read.
type FalkorDBProber struct{}

var _ Prober = FalkorDBProber{}

// Probe uses one connection from the deployment target.
func (FalkorDBProber) Probe(ctx context.Context, dial DialFunc, raw string, req recipes.Requirement) (*Report, error) {
	a, err := ParseEngineAccess(raw)
	if err != nil {
		return nil, err
	}
	if a.Engine != EngineFalkorDB || req.Engine != "" && req.Engine != a.Engine || len(req.Extensions) > 0 || len(req.Preload) > 0 || req.CreateRole {
		return nil, invalidEngineAccess()
	}
	if len(req.TLS) > 0 && !slices.Contains(req.TLS, a.TLS) {
		return &Report{Problems: []string{"the FalkorDB endpoint does not satisfy the required TLS policy"}}, nil
	}
	if req.MinVersion != "" {
		if _, versionErr := versionParts(req.MinVersion); versionErr != nil {
			return nil, invalidEngineAccess()
		}
	}
	if dial == nil {
		return nil, errors.New("the FalkorDB check requires a guarded dialer")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return nil, falkorTransportFailure(ctx, err, "the FalkorDB connection could not be opened")
	}
	conn, dialErr := dial(ctx, "tcp", net.JoinHostPort(a.Host, strconv.Itoa(a.Port)))
	if conn == nil {
		return nil, falkorTransportFailure(ctx, dialErr, "the FalkorDB connection could not be opened")
	}

	// Forwarded SSH connections cannot set deadlines, and closing the channel
	// can wait on its transport. Return on cancellation so the target caller
	// can close that transport, even while the channel is still shutting down.
	wire := falkorProbeConn{Conn: conn, closeOnce: sync.OnceValue(conn.Close)}
	stop := context.AfterFunc(ctx, func() { _ = wire.Close() })
	type probeResult struct {
		report *Report
		err    error
	}
	result := make(chan probeResult, 1)
	go func() {
		defer stop()
		var completed probeResult
		if dialErr != nil {
			completed.err = falkorTransportFailure(ctx, dialErr, "the FalkorDB connection could not be opened")
		} else {
			completed.report, completed.err = probeFalkorConnection(ctx, wire, a, req)
		}
		_ = wire.Close()
		result <- completed
	}()
	select {
	case <-ctx.Done():
	case completed := <-result:
		if ctx.Err() == nil {
			return completed.report, completed.err
		}
	}
	return nil, falkorTransportFailure(ctx, ctx.Err(), "the FalkorDB connection check did not finish")
}

// The probe, cancellation callback and TLS handshake share one raw close.
type falkorProbeConn struct {
	net.Conn
	closeOnce func() error
}

func (c falkorProbeConn) Close() error { return c.closeOnce() }

func probeFalkorConnection(ctx context.Context, conn net.Conn, a EngineAccess, req recipes.Requirement) (*Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, falkorTransportFailure(ctx, err, "the FalkorDB connection check did not finish")
	}
	wire := conn
	if a.TLS == TLSVerifyFull {
		roots, caErr := engineRootCAs(a.CACert)
		if caErr != nil {
			return nil, caErr
		}
		client := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: a.Host})
		if handshakeErr := client.HandshakeContext(ctx); handshakeErr != nil {
			return nil, falkorTransportFailure(ctx, handshakeErr, "the FalkorDB TLS handshake could not be verified")
		}
		wire = client
	}

	reply := newFalkorRESP(wire)
	hello, err := falkorExchange(ctx, wire, reply, "HELLO", "2", "AUTH", a.Username, a.Password)
	if err != nil {
		return nil, err
	}
	if err = validateFalkorHello(hello); err != nil {
		return nil, refused(err.Error())
	}
	config, err := falkorExchange(ctx, wire, reply, "GRAPH.CONFIG", "GET", "RESULTSET_SIZE")
	if err != nil {
		return nil, err
	}
	if !validFalkorConfig(config) {
		return nil, refused(errFalkorResponse.Error())
	}
	report := &Report{Version: "4.2.2"}
	checkVersion(report, a.Engine, req)
	return report, nil
}

func falkorExchange(ctx context.Context, writer io.Writer, reader *falkorRESP, args ...string) (falkorValue, error) {
	const writeFailure = "the FalkorDB request could not be sent"
	if err := ctx.Err(); err != nil {
		return falkorValue{}, falkorTransportFailure(ctx, err, writeFailure)
	}
	if err := writeFalkorCommand(writer, args...); err != nil {
		return falkorValue{}, falkorTransportFailure(ctx, err, writeFailure)
	}
	value, err := reader.reply()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return falkorValue{}, falkorTransportFailure(ctx, ctxErr, "the FalkorDB response could not be read")
	}
	switch {
	case err == nil:
		return value, nil
	case errors.Is(err, errFalkorAuth):
		return falkorValue{}, refused("the FalkorDB credentials were refused")
	case errors.Is(err, errFalkorPerm):
		if args[0] == "HELLO" {
			return falkorValue{}, refused("the FalkorDB authentication request was denied")
		}
		return falkorValue{}, refused("the FalkorDB configuration read was denied")
	case errors.Is(err, errFalkorCommand):
		if args[0] == "HELLO" {
			return falkorValue{}, refused("the FalkorDB authentication request was refused")
		}
		return falkorValue{}, refused("the FalkorDB configuration read was refused")
	case errors.Is(err, errFalkorResponse):
		return falkorValue{}, refused(errFalkorResponse.Error())
	case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
		return falkorValue{}, falkorTransportFailure(ctx, err, "the FalkorDB connection closed before replying")
	default:
		return falkorValue{}, falkorTransportFailure(ctx, err, "the FalkorDB response could not be read")
	}
}

func falkorTransportFailure(ctx context.Context, err error, message string) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return &probeError{msg: "the FalkorDB check was canceled"}
	}
	var timeout net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return errTimedOut
	}
	return &probeError{msg: message}
}
