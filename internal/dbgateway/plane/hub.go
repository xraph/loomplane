package plane

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// A route's clients share one listener connection for LISTEN. A client's
// LISTEN subscribes it to the route's Hub instead of holding a backend
// connection for the rest of its session. The hub listens on a channel while
// anyone subscribes to it and fans each notification out to the channel's
// subscribers.
//
// The listener connection is dialled on the first Subscribe and lives as
// long as the hub. When it fails the hub dials again, backing off from 100ms
// to 5s, and listens again on every channel that has subscribers. Whatever
// was notified while it was down is lost, as it would be for a client whose
// own connection dropped.

const (
	// subscriberBuffer is how many notifications a subscriber can fall behind
	// before the hub drops them for it.
	subscriberBuffer = 64
	hubBackoffMin    = 100 * time.Millisecond
	hubBackoffMax    = 5 * time.Second
	// maxIdentLen is the longest identifier Postgres keeps (NAMEDATALEN-1).
	maxIdentLen = 63
)

// ErrHubClosed is what Listening returns once the hub is closed.
var ErrHubClosed = errors.New("the route's LISTEN hub is closed")

// Subscriber receives the notifications of the channels it subscribes to on
// C. Every subscriber of a channel gets the same NotificationResponse, which
// none of them may change. When C is full the hub drops the notification for
// this subscriber and counts it, so one slow client never holds up the
// others.
type Subscriber struct {
	C     chan *pgproto3.NotificationResponse
	drops atomic.Uint64
}

// NewSubscriber returns a Subscriber with room for 64 notifications.
func NewSubscriber() *Subscriber {
	return &Subscriber{C: make(chan *pgproto3.NotificationResponse, subscriberBuffer)}
}

// Drops is how many notifications the hub dropped because C was full.
func (s *Subscriber) Drops() uint64 { return s.drops.Load() }

// Hub is one route's shared listener connection and its subscribers. No
// method waits on backend I/O while holding the hub's lock: one goroutine
// owns the listener connection and works out what to LISTEN and UNLISTEN
// from the subscriptions, and another reads it and delivers notifications.
type Hub struct {
	dial   func(ctx context.Context) (*BackendConn, error)
	ctx    context.Context
	cancel context.CancelFunc
	kick   chan struct{} // the subscriptions changed
	done   chan struct{} // closed when the listener goroutine exits

	mu       sync.Mutex
	subs     map[string]map[*Subscriber]struct{}
	owned    map[*Subscriber]map[string]struct{}
	listened map[string]bool  // channels the current connection listens on
	failed   map[string]error // channels the current connection refused to listen on
	changed  chan struct{}    // closed and replaced when listened or failed change
	conn     *BackendConn
	started  bool
	closed   bool
}

// NewHub returns a hub that opens its listener connection with dial when
// the first client subscribes.
func NewHub(dial func(ctx context.Context) (*BackendConn, error)) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		dial:     dial,
		ctx:      ctx,
		cancel:   cancel,
		kick:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		subs:     map[string]map[*Subscriber]struct{}{},
		owned:    map[*Subscriber]map[string]struct{}{},
		listened: map[string]bool{},
		failed:   map[string]error{},
		changed:  make(chan struct{}),
	}
}

// Subscribe adds c to channel's subscribers. The hub starts listening on a
// channel when it gets its first subscriber, but Subscribe doesn't wait for
// that: Listening does. Subscribing twice is the same as once. After Close
// it does nothing.
func (h *Hub) Subscribe(channel string, c *Subscriber) {
	channel = channelKey(channel)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	set := h.subs[channel]
	if set == nil {
		set = map[*Subscriber]struct{}{}
		h.subs[channel] = set
	}
	if _, ok := set[c]; ok {
		return
	}
	set[c] = struct{}{}
	mine := h.owned[c]
	if mine == nil {
		mine = map[string]struct{}{}
		h.owned[c] = mine
	}
	mine[channel] = struct{}{}
	if len(set) == 1 {
		h.kickLocked()
	}
	if !h.started {
		h.started = true
		go h.run()
	}
}

// Unsubscribe removes c from channel's subscribers. The hub stops listening
// on a channel when its last subscriber leaves.
func (h *Hub) Unsubscribe(channel string, c *Subscriber) {
	channel = channelKey(channel)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(channel, c)
}

// UnsubscribeAll removes c from every channel.
func (h *Hub) UnsubscribeAll(c *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for channel := range h.owned[c] {
		h.removeLocked(channel, c)
	}
}

func (h *Hub) removeLocked(channel string, c *Subscriber) {
	set := h.subs[channel]
	if _, ok := set[c]; !ok {
		return
	}
	delete(set, c)
	if mine := h.owned[c]; mine != nil {
		delete(mine, channel)
		if len(mine) == 0 {
			delete(h.owned, c)
		}
	}
	if len(set) == 0 {
		delete(h.subs, channel)
		delete(h.failed, channel)
		h.kickLocked()
		// Anyone waiting in Listening for this channel can stop.
		h.broadcastLocked()
	}
}

// Listening waits until the listener connection listens on channel. It
// returns at once when nobody subscribes to the channel, the backend's error
// when it refused the LISTEN, ctx's error when ctx ends first, and
// ErrHubClosed after Close.
func (h *Hub) Listening(ctx context.Context, channel string) error {
	channel = channelKey(channel)
	for {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return ErrHubClosed
		}
		_, wanted := h.subs[channel]
		if h.listened[channel] || !wanted {
			h.mu.Unlock()
			return nil
		}
		if err := h.failed[channel]; err != nil {
			h.mu.Unlock()
			return err
		}
		changed := h.changed
		h.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close stops the listener, closes its connection and waits for the hub's
// goroutines to exit. The router calls it when it retires the route.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	h.broadcastLocked()
	c, started := h.conn, h.started
	h.mu.Unlock()
	h.cancel()
	if c != nil {
		_ = c.Close()
	}
	if started {
		<-h.done
	}
}

func (h *Hub) kickLocked() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *Hub) broadcastLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

// run keeps a listener connection open until the hub closes.
func (h *Hub) run() {
	defer close(h.done)
	backoff := hubBackoffMin
	for {
		c, err := h.dial(h.ctx)
		if err == nil {
			began := time.Now()
			h.serve(c)
			_ = c.Close()
			h.mu.Lock()
			h.conn = nil
			h.listened = map[string]bool{}
			h.failed = map[string]error{}
			h.broadcastLocked()
			h.mu.Unlock()
			// A connection that lasted resets the backoff; one that failed
			// straight away doesn't, or a backend that accepts and then drops
			// the gateway would be dialled ten times a second.
			if time.Since(began) >= hubBackoffMax {
				backoff = hubBackoffMin
			}
		}
		if h.ctx.Err() != nil {
			return
		}
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-h.ctx.Done():
			t.Stop()
			return
		}
		backoff = min(2*backoff, hubBackoffMax)
	}
}

// hubReply is the part of a command's reply the listener goroutine needs.
type hubReply struct {
	err   error
	ready bool
}

// serve runs one listener connection until it fails or the hub closes. It
// issues one LISTEN or UNLISTEN at a time, whichever brings the connection
// closer to the subscriptions, and waits for its ReadyForQuery.
func (h *Hub) serve(c *BackendConn) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.conn = c
	h.mu.Unlock()

	replies := make(chan hubReply)
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		h.read(c, replies, stop)
	}()
	defer func() {
		close(stop)
		_ = c.Close()
		<-readerDone
	}()

	for {
		verb, channel, ok := h.next()
		if !ok {
			select {
			case <-h.kick:
				continue
			case <-readerDone:
				return
			case <-h.ctx.Done():
				return
			}
		}
		c.Frontend.Send(&pgproto3.Query{String: verb + " " + quoteIdent(channel)})
		if c.Frontend.Flush() != nil {
			return
		}
		var cmdErr error
		for ready := false; !ready; {
			select {
			case r := <-replies:
				if r.err != nil {
					cmdErr = r.err
				}
				ready = r.ready
			case <-readerDone:
				return
			case <-h.ctx.Done():
				return
			}
		}
		h.finish(verb, channel, cmdErr)
	}
}

// read reads the listener connection, delivering notifications and handing
// command replies to serve, until the connection fails or stop is closed.
func (h *Hub) read(c *BackendConn, replies chan<- hubReply, stop <-chan struct{}) {
	for {
		msg, err := c.Frontend.Receive()
		if err != nil {
			return
		}
		var r hubReply
		switch m := msg.(type) {
		case *pgproto3.NotificationResponse:
			h.deliver(m, c.PID)
			continue
		case *pgproto3.ErrorResponse:
			if m.SeverityUnlocalized == "FATAL" || m.SeverityUnlocalized == "PANIC" || m.Severity == "FATAL" || m.Severity == "PANIC" {
				// The backend is closing the connection, and nothing is
				// waiting for this.
				return
			}
			r.err = pgconn.ErrorResponseToPgError(m)
		case *pgproto3.ReadyForQuery:
			r.ready = true
		default:
			continue
		}
		select {
		case replies <- r:
		case <-stop:
			return
		}
	}
}

// next is the command that brings the listener closer to the
// subscriptions: LISTEN on a channel with subscribers, or UNLISTEN on one
// without. A channel the backend refused isn't tried again on this
// connection.
func (h *Hub) next() (verb, channel string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		if !h.listened[ch] && h.failed[ch] == nil {
			return "LISTEN", ch, true
		}
	}
	for ch := range h.listened {
		if _, wanted := h.subs[ch]; !wanted {
			return "UNLISTEN", ch, true
		}
	}
	return "", "", false
}

// finish records the outcome of a LISTEN or UNLISTEN.
func (h *Hub) finish(verb, channel string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case verb == "UNLISTEN":
		delete(h.listened, channel)
	case err != nil:
		if _, wanted := h.subs[channel]; wanted {
			h.failed[channel] = err
		}
	default:
		h.listened[channel] = true
	}
	h.broadcastLocked()
}

// deliver hands a notification to the channel's subscribers without
// waiting for any of them. The copy carries the listener connection's PID,
// since pgproto3 reuses m on the next Receive.
func (h *Hub) deliver(m *pgproto3.NotificationResponse, pid uint32) {
	n := &pgproto3.NotificationResponse{PID: pid, Channel: m.Channel, Payload: m.Payload}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[n.Channel] {
		select {
		case s.C <- n:
		default:
			s.drops.Add(1)
		}
	}
}

// channelKey is the channel name Postgres keeps: identifiers longer than 63
// bytes are cut at a character boundary.
func channelKey(name string) string {
	if len(name) <= maxIdentLen {
		return name
	}
	n := maxIdentLen
	for n > 0 && !utf8.RuneStart(name[n]) {
		n--
	}
	return name[:n]
}

// quoteIdent quotes a channel name so its case and characters survive.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
