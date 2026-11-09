package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Handler serves one stream. Serve closes the stream afterwards. Serve sends
// the accept verdict just before the handler's first read or write of the
// stream, so a handler may first check what it was asked for and refuse the
// stream instead, by returning a refusal before it has used it. A handler
// with slow work to do before it uses the stream calls accept first.
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

// refusedError, returned by a Handler before it has used the stream, refuses
// the stream with its reason instead of accepting it. Once the stream has
// been used, Serve only logs it.
type refusedError struct{ reason string }

func (e *refusedError) Error() string { return e.reason }

// refusal returns a refusedError with reason.
func refusal(reason string) error { return &refusedError{reason: reason} }

// headerTimeout is how long a stream may take to send its header.
var headerTimeout = 10 * time.Second

// logger is the registry's log, or the default one.
func (r *Registry) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Serve reads the stream header, writes the verdict and runs the handler.
// The wire contract matches the hub: one verdict byte, 0 for accepted, and for
// anything else a reason line ending in a newline.
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
		r.refuseWith(stream, nodeproto.VerdictNotAllowed, fmt.Sprintf("endpoint %s is not allowed on this node", hdr.Endpoint))
		return
	}
	// The rules every handler shares, such as a fenced docker stream
	// carrying a whole fence, are checked once here.
	if verr := hdr.Validate(); verr != nil {
		r.refuse(stream, verr.Error())
		return
	}
	r.mu.RLock()
	h := r.handlers[hdr.Endpoint]
	r.mu.RUnlock()
	if h == nil {
		r.refuse(stream, fmt.Sprintf("endpoint %s is not available in this node version", hdr.Endpoint))
		return
	}
	s := &acceptOnUse{Conn: stream}
	err = h(ctx, hdr, s)
	var refused *refusedError
	if errors.As(err, &refused) && s.claim() {
		r.refuse(stream, refused.reason)
		return
	}
	// A handler that never used the stream still accepted it.
	if aerr := s.accept(); aerr != nil {
		return
	}
	if err != nil {
		r.logger().Warn("loomplane-node: serving a stream", "endpoint", sanitize(string(hdr.Endpoint)), "error", err)
	}
}

// errStreamRefused is what a refused stream's reads and writes return.
var errStreamRefused = errors.New("the stream was refused")

// acceptOnUse writes the accept verdict before the first read or write.
type acceptOnUse struct {
	net.Conn
	once sync.Once
	err  error
}

// accept writes the accept verdict unless it, or a refusal, went first.
func (a *acceptOnUse) accept() error {
	a.once.Do(func() { _, a.err = a.Conn.Write([]byte{nodeproto.VerdictAccepted}) })
	return a.err
}

// claim reports whether the stream is still unanswered, and if so keeps it
// from being accepted, so Serve can refuse it.
func (a *acceptOnUse) claim() (won bool) {
	a.once.Do(func() { won, a.err = true, errStreamRefused })
	return won
}

func (a *acceptOnUse) Read(b []byte) (int, error) {
	if err := a.accept(); err != nil {
		return 0, err
	}
	return a.Conn.Read(b)
}

func (a *acceptOnUse) Write(b []byte) (int, error) {
	if err := a.accept(); err != nil {
		return 0, err
	}
	return a.Conn.Write(b)
}

// accept sends stream's accept verdict now, if Serve has not sent it yet.
func accept(stream net.Conn) error {
	if a, ok := stream.(*acceptOnUse); ok {
		return a.accept()
	}
	return nil
}

// refuse sanitizes the reason, logs it and writes the refusal verdict and a
// single-line reason. The hub reads at most 512 bytes of reason, so the line
// is capped below that.
func (r *Registry) refuse(stream net.Conn, reason string) {
	r.refuseWith(stream, nodeproto.VerdictRefused, reason)
}

// refuseWith is refuse with verdict code in place of VerdictRefused.
func (r *Registry) refuseWith(stream net.Conn, code byte, reason string) {
	reason = sanitize(reason)
	if len(reason) > 500 {
		reason = strings.ToValidUTF8(reason[:500], "")
	}
	r.logger().Warn("loomplane-node: refused a stream", "reason", reason)
	if _, err := stream.Write(append([]byte{code}, reason+"\n"...)); err != nil {
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
