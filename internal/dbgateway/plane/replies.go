package plane

import (
	"bytes"

	"github.com/jackc/pgx/v5/pgproto3"
)

// This file is the backend side of a session: the reader goroutine that owns
// a lease while it is held, what each backend reply does to the session, and
// when the lease goes back to the pool.

// segment is what the backend reported since the last ReadyForQuery. SET and
// RESET take effect for the session only if their transaction committed, which
// the session judges when the segment ends.
type segment struct {
	effects    []Classification
	failed     bool // an ErrorResponse arrived
	txnControl bool // a transaction began or ended inside the segment
}

// readBackend is the backend reader for one lease.
func (s *session) readBackend(l *Lease, done chan struct{}) {
	defer s.readers.Done()
	defer close(done)
	fe := l.Conn().Frontend
	for {
		msg, err := fe.Receive()
		if err != nil {
			s.mu.Lock()
			if s.stopping {
				// The stop deadline cut this read short, possibly part way
				// through a message.
				s.readerCut = true
				s.mu.Unlock()
				return
			}
			s.loseLocked(l)
			return
		}
		if s.fromBackend(l, msg) {
			return
		}
	}
}

// fromBackend handles one backend message and reports whether the reader is
// done with the lease.
func (s *session) fromBackend(l *Lease, msg pgproto3.BackendMessage) bool {
	s.mu.Lock()
	_, ready := msg.(*pgproto3.ReadyForQuery)
	// If this message can leave the backend with nothing to answer, the lease
	// may go back to the pool here, so the session goroutine must be done
	// writing to the connection first. Otherwise it can't, and waiting could
	// deadlock: the session goroutine may be blocked writing to a backend
	// that is itself blocked writing replies to this reader.
	for s.writing > 0 && !s.stopping && s.mayGoIdle(ready) {
		s.cond.Wait()
	}
	out, ok := s.dispatch(l, msg)
	if !ok {
		if s.stopping {
			s.mu.Unlock()
			return true
		}
		s.loseLocked(l)
		return true
	}
	if ready && s.seq.empty() {
		s.idleAtSync = true
	}
	// Hub notifications that waited for the transaction to end go out
	// right behind its ReadyForQuery.
	out = append(out, s.takeNotesLocked()...)
	// Bytes still buffered after ReadyForQuery are an asynchronous message,
	// such as a notice, that belongs to this client. The lease stays until
	// they have been read and forwarded, or the next holder's reader would
	// pass them to its own client.
	drained := l.Conn().Frontend.ReadBufferLen() == 0
	release := s.idleAtSync && drained && !s.stopping && !s.pinned && !s.writeFailed &&
		s.status == 'I' && s.seq.empty() && s.writing == 0

	// Encode the reply before the lease goes back: msg belongs to the
	// connection's Frontend, which the next holder reads into.
	s.writeMu.Lock()
	s.sendLocked(out...)
	if release {
		s.lease = nil
		l.Release(LeaseClean)
	}
	exit := release || s.stopping
	s.mu.Unlock()
	// Flush once the backend's buffer is drained, and always at
	// ReadyForQuery: bytes left behind it may be only part of a message, and
	// the next Receive would hold the reply back until the rest arrives.
	if drained || ready || exit {
		s.flushLocked()
	}
	s.writeMu.Unlock()
	if release {
		s.signal()
	}
	return exit
}

// mayGoIdle reports whether handling the message can leave the backend with
// nothing to answer: a ReadyForQuery with nothing real queued behind its
// entry, or an asynchronous message after the last ReadyForQuery.
func (s *session) mayGoIdle(ready bool) bool {
	if ready {
		return s.seq.onlyHead()
	}
	return s.idleAtSync && s.seq.empty()
}

// loseLocked handles a backend that failed or broke the protocol: the lease
// is released broken and the client gets FATAL 08006. Called with s.mu held;
// releases it.
func (s *session) loseLocked(l *Lease) {
	s.lease = nil
	s.lost = true
	s.seq = sequencer{}
	l.Conn().Broken = true
	l.Release(LeaseBroken)
	s.writeMu.Lock()
	s.mu.Unlock()
	s.sendLocked(fatalError("08006", msgBackendLost))
	s.flushLocked()
	s.writeMu.Unlock()
	s.signal()
}

// dispatch applies one backend message to the session and returns what the
// client should see. ok is false when the message makes no sense for what
// is pending.
func (s *session) dispatch(l *Lease, msg pgproto3.BackendMessage) (out []pgproto3.BackendMessage, ok bool) {
	head := s.seq.head()
	switch judge(head, msg) {
	case verdictAsync:
		if ps, isStatus := msg.(*pgproto3.ParameterStatus); isStatus {
			c := l.Conn()
			if c.Params == nil {
				c.Params = map[string]string{}
			}
			c.Params[ps.Name] = ps.Value
		}
		return append(out, msg), true
	case verdictError:
		s.seg.failed = true
		if head != nil && head.kind.extended() {
			dropped, synced := s.seq.fail()
			for _, e := range dropped {
				s.undo(l, e)
			}
			if !synced {
				s.skip = true
			}
		}
		out = append(out, msg)
	case verdictPass:
		if cc, isCC := msg.(*pgproto3.CommandComplete); isCC {
			s.completed(l, head, cc)
		}
		out = append(out, msg)
	case verdictDone:
		e := s.seq.pop()
		switch m := msg.(type) {
		case *pgproto3.CommandComplete:
			s.completed(l, &e, m)
		case *pgproto3.ReadyForQuery:
			s.ready(l, m.TxStatus)
		}
		if !e.injected {
			out = append(out, msg)
		}
	default:
		return nil, false
	}
	return s.answerHead(out), true
}

// completed records what a finished statement did to the session.
func (s *session) completed(l *Lease, e *entry, cc *pgproto3.CommandComplete) {
	if isTxnControl(cc.CommandTag) {
		s.seg.txnControl = true
	}
	switch e.effect.Effect {
	case EffectSet, EffectReset:
		s.seg.effects = append(s.seg.effects, e.effect)
	case EffectDiscardAll:
		// DISCARD ALL can't run inside a transaction, so it has taken effect.
		// Parameters return to their startup values, which for the backend
		// connection means no overrides.
		s.seg.effects = nil
		s.overrides = copyParams(s.startup)
		s.connParams = map[string]string{}
		l.SetOverrides(s.connParams)
		s.dropStatements(e.upTo)
		s.resetBackendStatements(l.Conn())
		// DISCARD ALL includes UNLISTEN *.
		s.unlistenLocked("*")
	case EffectUnlisten:
		// An UNLISTEN that went to the client's own connection also ends what
		// the client had on the hub. Inside a transaction that rolls back,
		// Postgres would keep the subscription; the gateway drops it anyway.
		s.unlistenLocked(e.effect.Channel)
	case EffectDeallocate:
		// Prepared statements are not transactional either.
		if e.effect.Param == "*" {
			s.dropStatements(e.upTo)
			s.resetBackendStatements(l.Conn())
		} else if !s.parsePending(e.effect.Param) {
			delete(l.Conn().Statements, e.effect.Param)
		}
	}
}

// ready ends a segment at ReadyForQuery. A SET or RESET outside a transaction
// that committed with no error becomes part of the client's overrides. One
// that a rollback could still undo, or whose fate the gateway can't tell,
// pins the session instead.
func (s *session) ready(l *Lease, status byte) {
	before := s.status
	s.status = status
	if len(s.seg.effects) > 0 && !s.pinned {
		if before == 'I' && status == 'I' && !s.seg.failed && !s.seg.txnControl {
			for _, c := range s.seg.effects {
				s.applyEffect(c)
			}
			l.SetOverrides(s.connParams)
		} else {
			s.pinned = true
		}
	}
	s.seg = segment{}
}

// applyEffect applies a committed SET or RESET to the client's overrides and
// to what the backend connection now carries. A RESET returns a parameter to
// its startup value, as in Postgres. On the backend connection that value is
// an override too, so the connection loses it until the next replay.
func (s *session) applyEffect(c Classification) {
	switch {
	case c.Effect == EffectSet:
		s.overrides[c.Param] = c.Value
		s.connParams[c.Param] = c.Value
	case c.Param == "*":
		s.overrides = copyParams(s.startup)
		s.connParams = map[string]string{}
	default:
		if v, ok := s.startup[c.Param]; ok {
			s.overrides[c.Param] = v
		} else {
			delete(s.overrides, c.Param)
		}
		delete(s.connParams, c.Param)
	}
}

// answerHead answers the synthetic entries at the head of the queue.
func (s *session) answerHead(out []pgproto3.BackendMessage) []pgproto3.BackendMessage {
	for h := s.seq.head(); h != nil && h.kind == entrySynthetic; h = s.seq.head() {
		out = append(out, s.reply(s.seq.pop())...)
	}
	return out
}

var txnControlTags = [][]byte{
	[]byte("BEGIN"), []byte("START TRANSACTION"), []byte("COMMIT"), []byte("ROLLBACK"),
	[]byte("SAVEPOINT"), []byte("RELEASE"), []byte("PREPARE TRANSACTION"),
}

// isTxnControl reports whether a command tag says a transaction or savepoint
// began or ended.
func isTxnControl(tag []byte) bool {
	for _, t := range txnControlTags {
		if bytes.HasPrefix(tag, t) {
			return true
		}
	}
	return false
}
