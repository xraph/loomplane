package oauth

import (
	"context"
	"sync"
)

// Gate queues renewals in the process before they reach the database. A
// caller waiting on the database lock holds a pooled connection, so
// without it many callers of one connection could take the whole pool and
// leave none for the caller holding the lock.
type Gate struct {
	mu    sync.Mutex
	queue map[string]*gateQueue
	slots chan struct{}
}

// gateQueue lets one caller of a connection through at a time.
type gateQueue struct {
	turn  chan struct{}
	users int
}

// NewGate returns a gate that lets at most slots callers through at once.
func NewGate(slots int) *Gate {
	return &Gate{queue: map[string]*gateQueue{}, slots: make(chan struct{}, slots)}
}

// Enter waits for key's turn and then for a free slot, or for ctx to end.
// The caller calls leave when done.
func (g *Gate) Enter(ctx context.Context, key string) (leave func(), err error) {
	g.mu.Lock()
	q := g.queue[key]
	if q == nil {
		q = &gateQueue{turn: make(chan struct{}, 1)}
		g.queue[key] = q
	}
	q.users++
	g.mu.Unlock()
	forget := func() {
		g.mu.Lock()
		q.users--
		if q.users == 0 {
			delete(g.queue, key)
		}
		g.mu.Unlock()
	}
	select {
	case q.turn <- struct{}{}:
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		<-q.turn
		forget()
		return nil, ctx.Err()
	}
	return func() {
		<-g.slots
		<-q.turn
		forget()
	}, nil
}
