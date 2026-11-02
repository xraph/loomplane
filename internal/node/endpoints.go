package node

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Handler serves one accepted stream. Serve closes the stream afterwards.
type Handler func(ctx context.Context, hdr nodeproto.StreamHeader, stream net.Conn) error

// Registry maps endpoints to handlers. Later phases call Register from their
// own files.
type Registry struct {
	// Log receives refusals and handler errors. It defaults to slog.Default.
	Log *slog.Logger

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

// headerTimeout is how long a stream may take to send its header.
var headerTimeout = 10 * time.Second

// Serve reads the stream header, writes the verdict and runs the handler.
// The wire contract matches the hub: one verdict byte, 0 for accepted, and for
// anything else a reason line ending in a newline.
func (r *Registry) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Registry) Serve(ctx context.Context, cfg *Live, stream net.Conn) {
	defer func() { _ = stream.Close() }()
	// A stream that never sends its header must not hold a goroutine forever.
	dl, canDeadline := stream.(interface{ SetReadDeadline(time.Time) error })
	if canDeadline {
		_ = dl.SetReadDeadline(time.Now().Add(headerTimeout)) //nolint:errcheck // a stream that cannot take a deadline just waits
	}
	hdr, err := nodeproto.ReadHeader(stream)
	if canDeadline {
		_ = dl.SetReadDeadline(time.Time{}) //nolint:errcheck // as above
	}
	if err != nil {
		r.logger().Warn("loomplane-node: reading a stream header", "error", err)
		return
	}
	if !cfg.Load().Allows(hdr.Endpoint) {
		r.refuse(stream, fmt.Sprintf("endpoint %s is not allowed on this node", hdr.Endpoint))
		return
	}
	r.mu.RLock()
	h := r.handlers[hdr.Endpoint]
	r.mu.RUnlock()
	if h == nil {
		r.refuse(stream, fmt.Sprintf("endpoint %s is not available in this node version", hdr.Endpoint))
		return
	}
	if _, err := stream.Write([]byte{0}); err != nil {
		return
	}
	if err := h(ctx, hdr, stream); err != nil {
		r.logger().Warn("loomplane-node: serving a stream", "endpoint", sanitize(string(hdr.Endpoint)), "error", err)
	}
}

// refuse sanitizes the reason, logs it and writes the refusal verdict and a
// single-line reason. The hub reads at most 512 bytes of reason, so the line
// is capped below that.
func (r *Registry) refuse(stream net.Conn, reason string) {
	reason = sanitize(reason)
	if len(reason) > 500 {
		reason = strings.ToValidUTF8(reason[:500], "")
	}
	r.logger().Warn("loomplane-node: refused a stream", "reason", reason)
	if _, err := stream.Write(append([]byte{verdictRefused}, reason+"\n"...)); err != nil {
		r.logger().Warn("loomplane-node: writing a refusal", "error", err)
	}
}

// sanitize drops control and non-printable runes and invalid UTF-8.
func sanitize(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}
