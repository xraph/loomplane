package plane

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// SessionRoute is what a session needs from its route. Gate blocks while the
// route is paused. Removed is closed once the route is gone. ServerParams are
// the ParameterStatus values of a real backend connection, which the session
// reports to its client when it starts. Hub is the route's shared listener;
// without one, LISTEN pins the session.
type SessionRoute interface {
	Pool() *Pool
	Gate(ctx context.Context) error
	Removed() <-chan struct{}
	ServerParams() map[string]string
	Hub() *Hub
}

const (
	msgPoolBusy     = "the database gateway's connection pool is busy, try again"
	msgBackendLost  = "the database gateway lost its connection to the database"
	msgNoBackend    = "the database gateway could not connect to the database"
	msgRouteRemoved = "the database route was removed"
	msgEncoding     = "the database gateway only supports UTF8 client_encoding"
	msgAborted      = "current transaction is aborted, commands ignored until end of transaction block"
)

var (
	errBackendLost  = errors.New("lost the connection to the backend")
	errRouteRemoved = errors.New("the database route was removed")
)

// SendError sends a FATAL ErrorResponse. It is for errors that end the
// connection, so it ignores write errors.
func SendError(be *pgproto3.Backend, code, message string) {
	be.Send(fatalError(code, message))
	_ = be.Flush()
}

func fatalError(code, message string) *pgproto3.ErrorResponse {
	return &pgproto3.ErrorResponse{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: code, Message: message}
}

// softError is an ERROR the session survives.
func softError(code, message string) *pgproto3.ErrorResponse {
	return &pgproto3.ErrorResponse{Severity: "ERROR", SeverityUnlocalized: "ERROR", Code: code, Message: message}
}

// clientMsg is one message, or the read error, handed from the client reader
// to the session.
type clientMsg struct {
	msg pgproto3.FrontendMessage
	err error
}

// session is one client connection proxied onto pooled backend connections.
//
// Two goroutines share it. The session goroutine reads client messages,
// forwards them to the leased backend connection and acquires leases. A
// backend reader goroutine per lease reads the backend's replies, forwards
// them to the client and decides when the lease goes back to the pool. mu
// guards everything below it, and writeMu serialises writes to the client.
// When both are needed mu is taken first, and a goroutine takes writeMu
// before letting go of mu, so client writes happen in the order the
// decisions behind them were made.
type session struct {
	ctx   context.Context
	be    *pgproto3.Backend
	route SessionRoute
	pool  *Pool
	hub   *Hub

	in      chan clientMsg
	ack     chan struct{}
	quit    chan struct{}
	wake    chan struct{}
	readers sync.WaitGroup

	writeMu    sync.Mutex
	unflushed  bool // guarded by writeMu
	clientGone bool // guarded by writeMu

	mu   sync.Mutex
	cond *sync.Cond // on mu; signalled when writing drops

	lease       *Lease
	readerDone  chan struct{}
	stopping    bool // the session goroutine is taking the lease back from the reader
	readerCut   bool // the stop deadline interrupted the reader's Receive
	lost        bool // the backend failed and the client has been told
	writeFailed bool // a write to the leased connection failed
	writing     int  // writes to the leased connection in progress
	idleAtSync  bool // the backend has answered everything up to a ReadyForQuery
	connParams  map[string]string

	seq    sequencer
	seg    segment
	status byte // transaction status from the last ReadyForQuery
	pinned bool
	skip   bool // an extended-protocol error: drop client messages until Sync
	batch  bool // extended-protocol messages since the last Sync

	startup   map[string]string // overrides from the startup packet, the reset values
	overrides map[string]string // the overrides the client's session should have

	stmts          map[string]*prepared
	stmtSeq        uint64
	unnamed        *prepared
	unnamedOnLease bool
	portals        map[string]Classification // read and written by the session goroutine only

	// The client's subscriptions on the route's hub. sub is created at the
	// first LISTEN the hub answers. Hub notifications wait in notes until the
	// client is between transactions, where Postgres would send them, and
	// holdNotes keeps them back while the gateway answers a LISTEN itself.
	sub        *Subscriber
	channels   map[string]bool
	notes      []*pgproto3.NotificationResponse
	holdNotes  bool
	notesTaken chan struct{} // signalled when notes are taken
}

// ServeSession proxies one authenticated client in transaction-pooling mode.
// The router calls it after sending AuthenticationOk. ServeSession reports
// the server's parameters (with the client's own startup values on top), a
// BackendKeyData and ReadyForQuery, then serves until the client terminates,
// the route is removed or ctx ends.
//
// A backend connection is leased for each transaction and goes back to the
// pool at the ReadyForQuery that ends it. Session state the gateway can't
// replay on another connection, such as an advisory lock, pins the session to
// its connection until it ends. Known parameters set with SET are replayed
// on whichever connection serves the next transaction, and named prepared
// statements are renamed and prepared again wherever they are needed.
//
// It returns nil when the client ended the session. By the time it returns,
// nothing it started writes to be any more, but a goroutine reading from be
// may still be blocked there: the caller closes the client connection, which
// ends it. A write to a client that has stopped reading blocks until the
// caller closes the connection or sets a deadline on it.
func ServeSession(ctx context.Context, be *pgproto3.Backend, r SessionRoute, startup map[string]string) error {
	overrides, shown, err := startupParams(startup)
	if err != nil {
		SendError(be, "08P01", err.Error())
		return err
	}
	s := &session{
		ctx:       ctx,
		be:        be,
		route:     r,
		pool:      r.Pool(),
		hub:       r.Hub(),
		in:        make(chan clientMsg),
		ack:       make(chan struct{}),
		quit:      make(chan struct{}),
		wake:      make(chan struct{}, 1),
		status:    'I',
		startup:   overrides,
		overrides: copyParams(overrides),
		stmts:     map[string]*prepared{},
		portals:   map[string]Classification{},
		channels:  map[string]bool{},

		notesTaken: make(chan struct{}, 1),
	}
	s.cond = sync.NewCond(&s.mu)
	if err := s.greet(shown); err != nil {
		return err
	}
	go s.readClient()
	defer func() {
		if s.sub != nil {
			s.hub.UnsubscribeAll(s.sub)
		}
		close(s.quit)
		s.readers.Wait()
		s.mu.Lock()
		pinned := s.pinned
		s.mu.Unlock()
		if pc, ok := s.route.(pinCounter); ok && pinned {
			pc.PinChanged(false)
		}
	}()
	return s.serve()
}

// greet sends the rest of the startup sequence. Cancel requests are not
// supported in Phase 1, so the BackendKeyData is random and nothing honours
// it.
func (s *session) greet(shown map[string]string) error {
	params := s.route.ServerParams()
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := params[name]
		if v, ok := shown[strings.ToLower(name)]; ok {
			value = v
		}
		s.be.Send(&pgproto3.ParameterStatus{Name: name, Value: value})
	}
	var key [8]byte
	_, _ = rand.Read(key[:])
	s.be.Send(&pgproto3.BackendKeyData{ProcessID: binary.BigEndian.Uint32(key[:4]), SecretKey: key[4:]})
	s.be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	return s.be.Flush()
}

// readClient reads client messages and hands each to the session goroutine,
// waiting until it is handled, since pgproto3 reuses the message.
func (s *session) readClient() {
	for {
		msg, err := s.be.Receive()
		select {
		case s.in <- clientMsg{msg, err}:
		case <-s.quit:
			return
		}
		if err != nil {
			return
		}
		select {
		case <-s.ack:
		case <-s.quit:
			return
		}
	}
}

// serve is the session goroutine's loop.
func (s *session) serve() error {
	defer s.endLease()
	for {
		s.mu.Lock()
		lost, idle, atRest := s.lost, s.lease == nil, s.pinnedAtRestLocked()
		s.mu.Unlock()
		if lost {
			return errBackendLost
		}
		// Only the session goroutine acquires or forwards, so a session idle
		// here, or pinned with nothing outstanding, stays that way until the
		// select returns. A pinned session ends there too; its lease goes
		// back through DISCARD ALL.
		var removed <-chan struct{}
		if idle || atRest {
			removed = s.route.Removed()
		}
		select {
		case in := <-s.in:
			if in.err != nil {
				return s.clientFailed(in.err)
			}
			if end, err := s.handle(in.msg); end {
				return err
			}
			s.ack <- struct{}{}
		case <-removed:
			if !idle {
				s.mu.Lock()
				atRest = s.pinnedAtRestLocked()
				s.mu.Unlock()
				if !atRest {
					continue
				}
			}
			s.fatal("57P01", msgRouteRemoved)
			return errRouteRemoved
		case <-s.wake:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
}

// clientFailed ends the session after a client read error. A closed
// connection is the client leaving; anything else it sent that pgproto3
// can't read is a protocol violation.
func (s *session) clientFailed(err error) error {
	var opErr *net.OpError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed), errors.Is(err, io.ErrClosedPipe):
		return nil
	case errors.As(err, &opErr):
		return err
	}
	s.fatal("08P01", "the database gateway could not read a message from the client")
	return err
}

// handle serves one client message. end says the session is over.
func (s *session) handle(msg pgproto3.FrontendMessage) (end bool, err error) {
	switch msg.(type) {
	case *pgproto3.Terminate:
		return true, nil
	case *pgproto3.Sync:
		return s.sync()
	}
	switch msg.(type) {
	case *pgproto3.Parse, *pgproto3.Bind, *pgproto3.Describe, *pgproto3.Execute, *pgproto3.Close:
		s.mu.Lock()
		s.batch = true
		s.mu.Unlock()
	}
	if s.skipping() {
		return false, nil
	}
	switch m := msg.(type) {
	case *pgproto3.Query:
		return s.query(m)
	case *pgproto3.Parse:
		return s.parse(m)
	case *pgproto3.Bind:
		return s.bind(m)
	case *pgproto3.Describe:
		return s.describe(m)
	case *pgproto3.Execute:
		return s.execute(m)
	case *pgproto3.Close:
		return s.closeObject(m)
	case *pgproto3.Flush:
		return s.flushBackend()
	case *pgproto3.FunctionCall:
		return s.functionCall(m)
	case *pgproto3.CopyData, *pgproto3.CopyDone, *pgproto3.CopyFail:
		return s.copyMessage(m)
	}
	s.fatal("08P01", "the database gateway got an unexpected message from the client")
	return true, errors.New("unexpected client message")
}

// pinnedAtRestLocked reports whether the session is pinned and its backend
// owes it nothing: idle at ReadyForQuery 'I' with no transaction, batch or
// write in progress. Called with s.mu held.
func (s *session) pinnedAtRestLocked() bool {
	return s.pinned && s.lease != nil && !s.lost && !s.batch && !s.skip &&
		s.status == 'I' && s.seq.empty() && s.idleAtSync && s.writing == 0
}

// pinLocked pins the session to its backend connection and tells a route
// that counts pinned sessions. Called with s.mu held.
func (s *session) pinLocked() {
	if s.pinned {
		return
	}
	s.pinned = true
	if pc, ok := s.route.(pinCounter); ok {
		pc.PinChanged(true)
	}
}

// pinCounter is implemented by a route that counts its pinned sessions. The
// session reports when it becomes pinned, and when a pinned session ends.
type pinCounter interface{ PinChanged(pinned bool) }

func (s *session) skipping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skip
}

// query forwards a simple Query, unless it is a DEALLOCATE, LISTEN or
// UNLISTEN the gateway answers itself.
func (s *session) query(m *pgproto3.Query) (bool, error) {
	class := classifyStatement(m.String)
	s.mu.Lock()
	// handle checked skip without the lock; a backend error may have set it
	// since, and then Postgres ignores this Query.
	if s.skip {
		s.mu.Unlock()
		return false, nil
	}
	// A simple Query destroys the unnamed statement, and its ReadyForQuery
	// ends any extended-protocol batch the client left without a Sync.
	s.unnamed = nil
	s.batch = false
	if class.Effect == EffectDeallocate && s.deallocateLocked(class) {
		return false, nil
	}
	if s.viaHubLocked(class) {
		s.holdNotes = true
		s.mu.Unlock()
		return s.hubQuery(class)
	}
	s.mu.Unlock()

	l, end, err := s.lockLease(false)
	if l == nil {
		return end, err
	}
	if s.skip {
		s.mu.Unlock()
		return false, nil
	}
	if pins(class) {
		s.pinLocked()
	}
	s.unnamedOnLease = false
	return s.forwardLocked(l, true, &entry{kind: entryQuery, effect: class, upTo: s.stmtSeq}, m)
}

// sync forwards a Sync, or answers it when no lease is held: without one no
// transaction can be open.
func (s *session) sync() (bool, error) {
	s.mu.Lock()
	s.skip = false
	s.batch = false
	if l := s.lease; l != nil {
		return s.forwardLocked(l, true, &entry{kind: entrySync}, &pgproto3.Sync{})
	}
	out := append([]pgproto3.BackendMessage{&pgproto3.ReadyForQuery{TxStatus: 'I'}}, s.takeNotesLocked()...)
	s.writeMu.Lock()
	s.mu.Unlock()
	s.sendLocked(out...)
	s.flushLocked()
	s.writeMu.Unlock()
	return false, nil
}

// flushBackend forwards a Flush. Without a lease nothing is waiting.
func (s *session) flushBackend() (bool, error) {
	s.mu.Lock()
	l := s.lease
	if l == nil || s.skip {
		s.mu.Unlock()
		return false, nil
	}
	return s.forwardLocked(l, true, nil, &pgproto3.Flush{})
}

// functionCall forwards a FunctionCall. The gateway can't see which function
// it calls, so it pins.
func (s *session) functionCall(m *pgproto3.FunctionCall) (bool, error) {
	l, end, err := s.lockLease(false)
	if l == nil {
		return end, err
	}
	if s.skip {
		s.mu.Unlock()
		return false, nil
	}
	s.pinLocked()
	return s.forwardLocked(l, true, &entry{kind: entryFunctionCall}, m)
}

// copyMessage forwards COPY data. It only means something while a COPY is
// running, which keeps the statement's entry pending.
func (s *session) copyMessage(m pgproto3.FrontendMessage) (bool, error) {
	s.mu.Lock()
	l := s.lease
	if l == nil || s.skip || s.seq.empty() {
		s.mu.Unlock()
		return false, nil
	}
	return s.forwardLocked(l, true, nil, m)
}

// lockLease returns the session's lease with s.mu held, acquiring one first
// if there is none. A nil lease means the message has been answered, or, with
// end set, that the session is over.
func (s *session) lockLease(extended bool) (l *Lease, end bool, err error) {
	s.mu.Lock()
	if s.lost {
		s.mu.Unlock()
		return nil, true, errBackendLost
	}
	if s.lease != nil {
		return s.lease, false, nil
	}
	want := copyParams(s.overrides)
	s.mu.Unlock()

	select {
	case <-s.route.Removed():
		s.fatal("57P01", msgRouteRemoved)
		return nil, true, errRouteRemoved
	default:
	}
	if gateErr := s.route.Gate(s.ctx); gateErr != nil {
		if s.ctx.Err() != nil {
			return nil, true, s.ctx.Err()
		}
		s.fatal("57P01", gateErr.Error())
		return nil, true, gateErr
	}
	l, err = s.pool.Acquire(s.ctx, want)
	if err != nil {
		end, err = s.acquireFailed(err, extended)
		return nil, end, err
	}

	s.mu.Lock()
	s.lease = l
	s.status = 'I'
	s.idleAtSync = true
	s.writeFailed = false
	s.unnamedOnLease = false
	s.connParams = want
	done := make(chan struct{})
	s.readerDone = done
	s.readers.Add(1)
	go s.readBackend(l, done)
	return l, false, nil
}

// acquireFailed answers a failed Acquire.
func (s *session) acquireFailed(err error, extended bool) (bool, error) {
	var pgErr *pgconn.PgError
	switch {
	case s.ctx.Err() != nil:
		return true, s.ctx.Err()
	case errors.Is(err, ErrPoolTimeout):
		s.refuse(extended, softError("53300", msgPoolBusy))
	case errors.As(err, &pgErr):
		// A failed parameter replay can echo the value in the message. The
		// value is the client's own, so it may see it, but it is never logged.
		s.refuse(extended, softError(pgErr.Code, pgErr.Message))
	case errors.Is(err, ErrPoolClosed):
		s.fatal("57P01", msgRouteRemoved)
		return true, errRouteRemoved
	default:
		// The dial error names the backend's address, which the client must
		// not learn.
		s.refuse(extended, softError("08006", msgNoBackend))
	}
	return false, nil
}

// refuse answers a message that couldn't get a lease with a non-fatal error.
// After a simple Query the client gets ReadyForQuery at once; in the extended
// protocol everything up to Sync is skipped, as Postgres does after an error.
func (s *session) refuse(extended bool, e *pgproto3.ErrorResponse) {
	s.mu.Lock()
	out := []pgproto3.BackendMessage{e}
	if extended {
		s.skip = true
	} else {
		out = append(out, &pgproto3.ReadyForQuery{TxStatus: 'I'})
		out = append(out, s.takeNotesLocked()...)
	}
	s.writeMu.Lock()
	s.mu.Unlock()
	s.sendLocked(out...)
	s.flushLocked()
	s.writeMu.Unlock()
}

// forwardLocked pushes e, if any, and writes msgs to the leased connection.
// It is called with s.mu held and releases it.
func (s *session) forwardLocked(l *Lease, flush bool, e *entry, msgs ...pgproto3.FrontendMessage) (bool, error) {
	if e != nil {
		s.seq.push(*e)
		s.idleAtSync = false
	}
	s.writing++
	s.mu.Unlock()

	fe := l.Conn().Frontend
	for _, m := range msgs {
		fe.Send(m)
	}
	var err error
	if flush {
		err = fe.Flush()
	}

	s.mu.Lock()
	if err != nil {
		s.writeFailed = true
	}
	s.writing--
	s.cond.Broadcast()
	s.mu.Unlock()
	if err == nil {
		return false, nil
	}

	s.endLease()
	s.mu.Lock()
	told := s.lost
	s.lost = true
	s.mu.Unlock()
	if !told {
		s.fatal("08006", msgBackendLost)
	}
	return true, errBackendLost
}

// endLease takes the lease back from the backend reader and releases it in
// the state the session leaves it in. The reader is stopped first with a
// read deadline in the past, so nothing reads the connection once the pool
// has it.
func (s *session) endLease() {
	s.mu.Lock()
	l := s.lease
	if l == nil {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	s.cond.Broadcast()
	done := s.readerDone
	s.mu.Unlock()

	c := l.Conn()
	_ = c.Conn.SetReadDeadline(time.Unix(1, 0)) //nolint:errcheck // on a dead connection the reader has already stopped
	<-done
	_ = c.Conn.SetReadDeadline(time.Time{}) //nolint:errcheck // on a dead connection the lease is dropped anyway

	s.mu.Lock()
	var state LeaseState
	switch {
	case s.writeFailed || !s.seq.empty() || !s.idleAtSync:
		// Replies are still owed, or the backend is part way through an
		// extended-protocol exchange.
		state = LeaseBroken
	case s.pinned && s.status != 'I':
		// Session state inside an open transaction. ROLLBACK would hand the
		// state to the next client, and DISCARD ALL can't run inside a
		// transaction, so the connection goes.
		state = LeaseBroken
	case s.status != 'I':
		state = LeaseDirtyTxn
	case s.pinned:
		state = LeaseSession
	case s.readerCut || c.Frontend.ReadBufferLen() > 0:
		// A clean connection goes straight to its next holder, whose reader
		// would finish a half-read message and hand it to another client.
		// The reader normally releases an idle lease itself, so this only
		// closes a connection the client left in a narrow window.
		state = LeaseBroken
	default:
		state = LeaseClean
	}
	s.lease = nil
	s.stopping = false
	s.readerCut = false
	s.seq = sequencer{}
	s.skip = false
	s.mu.Unlock()
	if state == LeaseBroken {
		c.Broken = true
	}
	l.Release(state)
}

// answerLocked answers e now if nothing is pending, or queues it behind what
// is. Called with s.mu held; releases it.
func (s *session) answerLocked(e entry) {
	if !s.seq.empty() {
		s.seq.push(e)
		s.mu.Unlock()
		return
	}
	out := append(s.reply(e), s.takeNotesLocked()...)
	s.writeMu.Lock()
	s.mu.Unlock()
	s.sendLocked(out...)
	s.flushLocked()
	s.writeMu.Unlock()
}

// fatal sends a FATAL error to the client.
func (s *session) fatal(code, message string) {
	s.writeMu.Lock()
	s.sendLocked(fatalError(code, message))
	s.flushLocked()
	s.writeMu.Unlock()
}

// sendLocked encodes msgs for the client. Called with writeMu held.
func (s *session) sendLocked(msgs ...pgproto3.BackendMessage) {
	if s.clientGone {
		return
	}
	for _, m := range msgs {
		s.be.Send(m)
		s.unflushed = true
	}
}

// flushLocked writes what sendLocked encoded. Called with writeMu held. An
// empty flush is skipped: on some connections, net.Pipe among them, a
// zero-length write waits for the reader.
func (s *session) flushLocked() {
	if s.clientGone || !s.unflushed {
		return
	}
	s.unflushed = false
	if err := s.be.Flush(); err != nil {
		s.clientGone = true
	}
}

// signal wakes the session goroutine.
func (s *session) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// classifyStatement is ClassifySQL with one correction: DISCARD PLANS drops
// cached plans, not prepared statements, so it needs no tracking.
func classifyStatement(sql string) Classification {
	c := ClassifySQL(sql)
	if c.Effect == EffectDeallocate && c.Param == "*" && leadingWord(sql) == "discard" {
		return Classification{}
	}
	return c
}

// leadingWord is the first keyword of sql, lowercased, past comments.
func leadingWord(sql string) string {
	buf := make([]byte, len(sql))
	maskSQL(buf, sql, false)
	r := scanWords(buf)
	if r.hs < 0 {
		return ""
	}
	return string(buf[r.hs:r.he])
}

// pins reports whether a statement pins the session. LISTEN and UNLISTEN pin
// when the route's hub can't answer them: see viaHubLocked.
func pins(c Classification) bool {
	switch c.Effect {
	case EffectPinSession, EffectListen, EffectUnlisten:
		return true
	}
	return false
}
