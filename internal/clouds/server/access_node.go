package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// maxProbeReply bounds a probe's JSON reply.
const maxProbeReply = 1 << 20

// errProbeReply is a probe reply that couldn't be read. Each probe turns it
// into the error its SSH command gives when it fails.
var errProbeReply = errors.New("the node's probe reply is unreadable")

// nodeAccess reaches a joined node's host through the hub's streams, one
// stream per call. The session belongs to the hub, so there is nothing of
// its own to close. Errors from Open come back unwrapped, so a caller can
// match the hub's offline and refusal errors.
type nodeAccess struct {
	streams NodeStreams
	target  NodeTarget
	// socket is the daemon socket the connection names. The node serves
	// the socket it found itself and no stream header names one, so a
	// fenced request for any other socket is refused rather than sent.
	socket string
}

func newNodeAccess(streams NodeStreams, t NodeTarget, socket string) HostAccess {
	return &nodeAccess{streams: streams, target: t, socket: socket}
}

func (a *nodeAccess) open(ctx context.Context, hdr nodeproto.StreamHeader) (net.Conn, error) {
	return a.streams.Open(ctx, a.target.OrgID, a.target.NodeID, hdr)
}

// DialContext dials from the node. The node takes only an IP literal or
// localhost, with a numeric port, and reaches only loopback and Loomplane's
// containers.
func (a *nodeAccess) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("%w: the server dials only TCP, not %s", ErrInvalid, network)
	}
	return a.open(ctx, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDial, Target: address, Arg: network})
}

func (a *nodeAccess) DialDocker(ctx context.Context) (net.Conn, error) {
	return a.open(ctx, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetSocket})
}

func (a *nodeAccess) Close() error { return nil }

// FencedRequest sends one request on a fenced docker stream and reads the
// response back. The stream stays open until the response has been read to
// its end: the node takes anything after the request, a half-close or
// closing early included, as the caller going away, and then leaves its
// journal dirty. So the request is never followed by a CloseWrite, and
// closing the body first reads the rest of the response.
func (a *nodeAccess) FencedRequest(ctx context.Context, mode string, f EffectFence, daemon, socket, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	if socket != a.socket {
		return nil, fmt.Errorf("%w: the node fences only its own Docker socket", ErrInvalid)
	}
	hdr := nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetFenced,
		Fence: &nodeproto.Fence{Resource: f.Operation, Generation: f.Generation, Mode: mode, Daemon: daemon}}
	if err := hdr.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	// The SSH helper forwards only the caller's headers. An empty
	// User-Agent keeps req.Write from adding Go's.
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header["User-Agent"] = []string{""}
	}
	conn, err := a.open(ctx, hdr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	// A failed write leaves the node a partial request, which it refuses
	// without running anything.
	if err = req.Write(conn); err != nil {
		stop()
		_ = conn.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		stop()
		_ = conn.Close()
		return nil, errFenceOutcomeUnknown
	}
	// Only a length or chunked framing proves the whole response arrived;
	// the node sends one or the other.
	chunked := len(response.TransferEncoding) > 0 && response.TransferEncoding[0] == "chunked"
	if !chunked && response.ContentLength < 0 {
		stop()
		_ = conn.Close()
		_ = response.Body.Close()
		return nil, errFenceOutcomeUnknown
	}
	response.Body = &nodeFenceBody{body: response.Body, conn: conn, stop: stop}
	return response, nil
}

// nodeFenceBody is a fenced response's body. A read that fails before the
// body's end, such as a chunked body without its last chunk, leaves the
// outcome unknown. Close reads what is left before it closes the stream.
type nodeFenceBody struct {
	body io.ReadCloser
	conn net.Conn
	stop func() bool
	err  error
}

func (b *nodeFenceBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	n, err := b.body.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = errFenceOutcomeUnknown
	}
	if err != nil {
		b.err = err
	}
	return n, err
}

func (b *nodeFenceBody) Close() error {
	_, err := io.Copy(io.Discard, b)
	b.stop()
	closeErr := errors.Join(b.body.Close(), b.conn.Close())
	if err != nil {
		// The unknown outcome is the news; the closes failing after it isn't.
		return err
	}
	return closeErr
}

// probe opens a probe stream for target and decodes its one JSON reply into
// v. A probe that fails on the node refuses the stream, and that error comes
// back from Open as it is. A reply it can't read is errProbeReply.
func (a *nodeAccess) probe(ctx context.Context, target, arg string, v any) error {
	conn, err := a.open(ctx, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe, Target: target, Arg: arg})
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err = json.NewDecoder(io.LimitReader(conn, maxProbeReply)).Decode(v); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s: %w", errProbeReply, target, err)
	}
	return nil
}

func (a *nodeAccess) CgroupLimits(ctx context.Context) (cpuMillis, memoryBytes int64, err error) {
	var limits nodeproto.CgroupLimits
	if err = a.probe(ctx, nodeproto.ProbeCgroup, "", &limits); err != nil {
		if errors.Is(err, errProbeReply) {
			return 0, 0, errors.New("effective Docker cgroup limits are unavailable")
		}
		return 0, 0, err
	}
	return limits.CPU, limits.Memory, nil
}

// GPUProbe returns the reply as raw JSON, the same shape the SSH command
// prints, so parseGPUProbe reads both.
func (a *nodeAccess) GPUProbe(ctx context.Context) ([]byte, error) {
	var raw json.RawMessage
	if err := a.probe(ctx, nodeproto.ProbeGPU, "", &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (a *nodeAccess) Filesystem(ctx context.Context, root string) (FilesystemCapacity, error) {
	// The SSH probe's check on the root, so both refuse the same roots.
	if _, err := storageCapacityCommand(root); err != nil {
		return FilesystemCapacity{}, err
	}
	var raw json.RawMessage
	if err := a.probe(ctx, nodeproto.ProbeFilesystem, root, &raw); err != nil {
		if errors.Is(err, errProbeReply) {
			return FilesystemCapacity{}, errors.New("docker data filesystem capacity is unavailable")
		}
		return FilesystemCapacity{}, err
	}
	return parseFilesystemCapacity(raw)
}

func (a *nodeAccess) IngressListeners(ctx context.Context) ([]int, error) {
	var listeners nodeproto.Listeners
	if err := a.probe(ctx, nodeproto.ProbeListeners, "", &listeners); err != nil {
		if errors.Is(err, errProbeReply) {
			return nil, errors.New("cannot verify ingress listener availability")
		}
		return nil, err
	}
	// As parseListeners: any port it can't vouch for fails the probe.
	ports := []int{}
	for _, port := range listeners.Ports {
		if port < 1 || port > 65535 {
			return nil, errors.New("cannot verify ingress listener availability")
		}
		if !slices.Contains(ports, port) {
			ports = append(ports, port)
		}
	}
	slices.Sort(ports)
	return ports, nil
}
