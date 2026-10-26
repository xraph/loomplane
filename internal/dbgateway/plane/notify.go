package plane

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// This file is the client's side of the route's LISTEN hub: when the hub
// answers a LISTEN or UNLISTEN instead of the client's own connection, and
// when hub notifications reach the client.

// listenWait is how long a LISTEN waits for the route's listener connection
// to listen on its channel. It matches the pool's wait limit.
const listenWait = 5 * time.Second

// idleLocked reports whether the client is between transactions, where
// Postgres would send it notifications: no lease, or a lease idle at
// ReadyForQuery 'I' with nothing outstanding, and no extended-protocol batch
// open.
func (s *session) idleLocked() bool {
	if s.batch || s.skip || s.lost {
		return false
	}
	return s.lease == nil || (s.status == 'I' && s.seq.empty() && s.idleAtSync)
}

// viaHubLocked reports whether the route's hub answers a simple Query: a
// lone LISTEN or UNLISTEN from an unpinned client between transactions.
// Anywhere else it pins and goes to the client's own connection. Inside a
// transaction that is what keeps it transactional; a pinned session already
// has a connection of its own.
func (s *session) viaHubLocked(c Classification) bool {
	if s.hub == nil || (c.Effect != EffectListen && c.Effect != EffectUnlisten) {
		return false
	}
	return !s.pinned && s.idleLocked()
}

// hubQuery answers a LISTEN or UNLISTEN through the route's hub, with
// CommandComplete and ReadyForQuery as Postgres would. It is called with
// holdNotes set, so no notification goes out ahead of the reply.
func (s *session) hubQuery(c Classification) (bool, error) {
	var reply []pgproto3.BackendMessage
	if c.Effect == EffectListen {
		if e := s.listen(c.Channel); e != nil {
			reply = append(reply, e)
		} else {
			reply = append(reply, &pgproto3.CommandComplete{CommandTag: []byte("LISTEN")})
		}
	} else {
		s.mu.Lock()
		s.unlistenLocked(c.Channel, c.All)
		s.mu.Unlock()
		reply = append(reply, &pgproto3.CommandComplete{CommandTag: []byte("UNLISTEN")})
	}
	s.mu.Lock()
	s.holdNotes = false
	reply = append(reply, &pgproto3.ReadyForQuery{TxStatus: s.status})
	reply = append(reply, s.takeNotesLocked()...)
	s.writeMu.Lock()
	s.mu.Unlock()
	s.sendLocked(reply...)
	s.flushLocked()
	s.writeMu.Unlock()
	return false, nil
}

// listen subscribes the client to channel and waits until the listener
// connection listens on it, so a NOTIFY sent after the client sees LISTEN
// complete reaches it. It returns the error to answer with when that fails.
func (s *session) listen(channel string) *pgproto3.ErrorResponse {
	channel = channelKey(channel)
	s.mu.Lock()
	if s.channels[channel] {
		s.mu.Unlock()
		return nil
	}
	sub := s.subscriberLocked()
	s.channels[channel] = true
	s.mu.Unlock()

	s.hub.Subscribe(channel, sub)
	ctx, cancel := context.WithTimeout(s.ctx, listenWait)
	err := s.hub.Listening(ctx, channel)
	cancel()
	if err == nil {
		return nil
	}
	s.mu.Lock()
	delete(s.channels, channel)
	s.mu.Unlock()
	s.hub.Unsubscribe(channel, sub)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return softError(pgErr.Code, pgErr.Message)
	}
	return softError("08006", msgNoBackend)
}

// unlistenLocked drops the client's hub subscription to channel, or to every
// channel when all is set (an unquoted UNLISTEN *, or DISCARD ALL). A quoted
// "*" is a channel like any other. Called with s.mu held.
func (s *session) unlistenLocked(channel string, all bool) {
	if s.sub == nil {
		return
	}
	if all {
		s.hub.UnsubscribeAll(s.sub)
		clear(s.channels)
		return
	}
	channel = channelKey(channel)
	s.hub.Unsubscribe(channel, s.sub)
	delete(s.channels, channel)
}

// subscriberLocked returns the client's hub subscriber, creating it and the
// goroutine that delivers its notifications on first use. Called with s.mu
// held.
func (s *session) subscriberLocked() *Subscriber {
	if s.sub == nil {
		s.sub = NewSubscriber()
		s.readers.Add(1)
		go s.pumpNotes(s.sub.C)
	}
	return s.sub
}

// pumpNotes moves hub notifications into notes and sends them while the
// client is between transactions. Otherwise they wait for whoever next
// sends the client a ReadyForQuery 'I'. At most a subscriber's buffer of
// them waits in the session; beyond that the subscriber's channel fills and
// the hub drops them.
func (s *session) pumpNotes(c <-chan *pgproto3.NotificationResponse) {
	defer s.readers.Done()
	for {
		s.mu.Lock()
		full := len(s.notes) >= subscriberBuffer
		s.mu.Unlock()
		if full {
			select {
			case <-s.notesTaken:
			case <-s.quit:
				return
			}
			continue
		}
		select {
		case n := <-c:
			s.mu.Lock()
			s.notes = append(s.notes, n)
			out := s.takeNotesLocked()
			if len(out) == 0 {
				s.mu.Unlock()
				continue
			}
			s.writeMu.Lock()
			s.mu.Unlock()
			s.sendLocked(out...)
			s.flushLocked()
			s.writeMu.Unlock()
		case <-s.quit:
			return
		}
	}
}

// takeNotesLocked returns the waiting notifications if the client is between
// transactions, for the caller to send after whatever it is sending. Called
// with s.mu held.
func (s *session) takeNotesLocked() []pgproto3.BackendMessage {
	if len(s.notes) == 0 || s.holdNotes || !s.idleLocked() {
		return nil
	}
	out := make([]pgproto3.BackendMessage, len(s.notes))
	for i, n := range s.notes {
		out[i] = n
	}
	clear(s.notes)
	s.notes = s.notes[:0]
	select {
	case s.notesTaken <- struct{}{}:
	default:
	}
	return out
}
