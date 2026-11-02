package node

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Handler serves one accepted stream. Serve closes the stream afterwards.
type Handler func(ctx context.Context, hdr nodeproto.StreamHeader, stream net.Conn) error

// Registry maps endpoints to handlers. Later phases call Register from their
// own files.
type Registry struct {
	mu       sync.RWMutex
	handlers map[nodeproto.Endpoint]Handler
}

// Register sets the handler for e.
func (r *Registry) Register(e nodeproto.Endpoint, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handlers == nil {
		r.handlers = map[nodeproto.Endpoint]Handler{}
	}
	r.handlers[e] = h
}

const verdictRefused = 1

// Serve reads the stream header, writes the verdict and runs the handler.
// The wire contract matches the hub: one verdict byte, 0 for accepted, and for
// anything else a reason line ending in a newline.
func (r *Registry) Serve(ctx context.Context, cfg *Config, stream net.Conn) {
	defer func() { _ = stream.Close() }()
	hdr, err := nodeproto.ReadHeader(stream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loomplane-node: reading a stream header: %v\n", err)
		return
	}
	if !cfg.Allows(hdr.Endpoint) {
		refuse(stream, fmt.Sprintf("endpoint %s is not allowed on this node", hdr.Endpoint))
		return
	}
	r.mu.RLock()
	h := r.handlers[hdr.Endpoint]
	r.mu.RUnlock()
	if h == nil {
		refuse(stream, fmt.Sprintf("endpoint %s is not available in this node version", hdr.Endpoint))
		return
	}
	if _, err := stream.Write([]byte{0}); err != nil {
		return
	}
	if err := h(ctx, hdr, stream); err != nil {
		fmt.Fprintf(os.Stderr, "loomplane-node: serving %s: %v\n", hdr.Endpoint, err)
	}
}

// refuse writes the refusal verdict and a single-line reason. The hub reads at
// most 512 bytes of reason, so the line is capped below that.
func refuse(stream net.Conn, reason string) {
	fmt.Fprintf(os.Stderr, "loomplane-node: refused a stream: %s\n", reason)
	reason = strings.NewReplacer("\n", " ", "\r", " ").Replace(reason)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if _, err := stream.Write(append([]byte{verdictRefused}, reason+"\n"...)); err != nil {
		fmt.Fprintf(os.Stderr, "loomplane-node: writing a refusal: %v\n", err)
	}
}
