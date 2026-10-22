package catalogproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stream is an event stream the component keeps open: what's written to
// it reaches the browser.
type stream struct {
	w    *io.PipeWriter
	done chan *httptest.ResponseRecorder
}

// openStream starts a GET of an event stream through the proxy, as
// another tab of the session would.
func openStream(t *testing.T, h *harness, base string) *stream {
	t.Helper()
	r, w := io.Pipe()
	h.up.transport = fixedTransport{func(req *http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: r, Request: req}
	}}
	s := &stream{w: w, done: make(chan *httptest.ResponseRecorder, 1)}
	go func() { s.done <- h.do(http.MethodGet, base+"/admin/api/events", nil) }()
	t.Cleanup(func() { _ = w.Close() })
	// Once the first event is through, the request has been served and
	// its watcher runs.
	s.send(t)
	return s
}

// send writes an event and waits until the proxy has passed it on: a pipe
// write returns once it's read, and the proxy reads again, here an empty
// write, only after writing what it read.
func (s *stream) send(t *testing.T) {
	t.Helper()
	for _, event := range []string{"data: x\n\n", ""} {
		if _, err := io.WriteString(s.w, event); err != nil {
			t.Fatalf("the stream ended: %v", err)
		}
	}
}

func (s *stream) ended(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-s.done:
		return w
	case <-time.After(5 * time.Second):
		t.Fatal("the stream is still open")
		return nil
	}
}

func (s *stream) open() bool {
	select {
	case w := <-s.done:
		s.done <- w
		return false
	default:
		return true
	}
}

// tickOnce has the stream's watcher check its session, and waits until it
// has.
func (h *harness) tickOnce(t *testing.T) {
	t.Helper()
	select {
	case h.tick <- time.Time{}:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing is watching the stream")
	}
	select {
	case <-h.checked:
	case <-time.After(5 * time.Second):
		t.Fatal("the check didn't finish")
	}
}

// usedAt checks that the session was last used at at.
func (h *harness) usedAt(t *testing.T, at time.Time) {
	t.Helper()
	if rows := h.store.all(); len(rows) != 1 || !rows[0].LastUsedAt.Equal(at) {
		t.Fatalf("the session wasn't used at %v: %+v", at, rows)
	}
}

func TestProxyEndsStreamsWhenPermissionIsRevoked(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	s := openStream(t, h, base)
	h.clock.Add(RecheckEvery)
	h.tickOnce(t)
	h.usedAt(t, h.clock.Now())
	if !s.open() {
		t.Fatal("a stream whose creator may still modify infrastructure ended")
	}
	// Revoked a second after the last check: the next tick still checks,
	// so the stream ends within one RecheckEvery.
	h.authz.deny("admin")
	h.clock.Add(time.Second)
	h.tickOnce(t)
	w := s.ended(t)
	if !strings.HasPrefix(w.Body.String(), "data: x\n\n") {
		t.Fatalf("body %q", w.Body.String())
	}
	if n := len(h.store.all()); n != 0 {
		t.Fatalf("the revoked session is still kept: %d", n)
	}
}

func TestProxyEndsStreamsWhenTheResourceIsRevoked(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	s := openStream(t, h, base)
	if err := h.svc.RevokeResource(context.Background(), "org", "env", "app"); err != nil {
		t.Fatal(err)
	}
	h.tickOnce(t)
	s.ended(t)
}

func TestProxyKeepsBusyStreamsAndEndsQuietOnes(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		h := newHarness(t, sampleTarget())
		base := h.open(t)
		s := openStream(t, h, base)
		// Events flowing count as use: the stream outlives the 30 minutes
		// its session had when it opened.
		for range 2 {
			h.clock.Add(25 * time.Minute)
			s.send(t)
			h.tickOnce(t)
			h.usedAt(t, h.clock.Now())
		}
		last := h.clock.Now()
		// A tick with no events in between isn't use.
		h.clock.Add(time.Minute)
		h.tickOnce(t)
		h.usedAt(t, last)
		if !s.open() {
			t.Fatal("a quiet minute ended the stream")
		}
		h.clock.Add(IdleTTL)
		h.tickOnce(t)
		s.ended(t)
	})
	t.Run("absolute", func(t *testing.T) {
		h := newHarness(t, sampleTarget())
		base := h.open(t)
		s := openStream(t, h, base)
		for elapsed := 20 * time.Minute; elapsed < MaxTTL; elapsed += 20 * time.Minute {
			h.clock.Add(20 * time.Minute)
			s.send(t)
			h.tickOnce(t)
			h.usedAt(t, h.clock.Now())
		}
		h.clock.Add(21 * time.Minute) // 8h01m
		s.send(t)
		h.tickOnce(t)
		s.ended(t)
	})
}

func TestProxyEndsWebSocketsWhenTheResourceIsRevoked(t *testing.T) {
	h := newHarness(t, sampleTarget())
	component, proxySide := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, component) }()
	t.Cleanup(func() { _ = component.Close() })
	h.up.transport = fixedTransport{func(r *http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: http.Header{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}}, Body: pipeBody{proxySide}, Request: r}
	}}
	base := h.open(t)
	srv := httptest.NewServer(h.handler)
	defer srv.Close()
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET "+base+"/admin/ws HTTP/1.1\r\nHost: loomplane.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("switch: %d", res.StatusCode)
	}
	if err = h.svc.RevokeResource(context.Background(), "org", "env", "app"); err != nil {
		t.Fatal(err)
	}
	h.tickOnce(t)
	if _, err = br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("the socket stayed open: %v", err)
	}
}
