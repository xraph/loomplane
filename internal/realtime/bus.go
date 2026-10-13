package realtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge/extensions/conduit"
	conduitpg "github.com/xraph/forge/extensions/conduit/providers/postgres"
)

// Wakeup carries a committed cursor, never credentials or entity data.
type Wakeup struct {
	OrgID  string `json:"org_id"`
	Cursor string `json:"cursor"`
}

var wakeupEvent = conduit.Event[Wakeup]("loomplane.wakeup.v1")

type watcher struct {
	org  string
	wake chan struct{}
}

// Bus broadcasts Postgres commit notifications through Conduit. A bounded
// catch-up tick makes missed notifications harmless to durable readers.
type Bus struct {
	CatchupInterval time.Duration
	Warn            func(error)
	dsn             string
	runtime         *conduit.Runtime
	mu              sync.Mutex
	listeners       map[uint64]watcher
	next            uint64
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

func NewBus(dsn, instance string) (*Bus, error) {
	cfg := conduit.Config{Identity: conduit.Identity{Namespace: "loomplane", ServiceID: "realtime", InstanceID: instance},
		Streams:       map[string]conduit.StreamConfig{"events": {Name: "events", Provider: "postgres", Subjects: []string{wakeupEvent.Name}, MaxAge: 5 * time.Minute, MaxMessages: 100000}},
		Subscriptions: map[string]conduit.SubscriptionConfig{"realtime": {Stream: "events", Mode: conduit.Broadcast, Durable: false, MaxInFlight: 16, Concurrency: 1, MaxAttempts: 1}},
	}
	r, err := conduit.New(cfg, conduit.WithProvider("postgres", conduitpg.New(conduitpg.Options{URL: dsn, Schema: "loomplane_realtime_broker", PollInterval: 200 * time.Millisecond})))
	if err != nil {
		return nil, err
	}
	b := &Bus{CatchupInterval: 2 * time.Second, dsn: dsn, runtime: r, listeners: map[uint64]watcher{}}
	if subscribeErr := conduit.Subscribe(r, wakeupEvent, func(_ context.Context, message conduit.Message[Wakeup]) error {
		if _, positionErr := position(message.Data.OrgID, message.Data.Cursor); positionErr != nil {
			return conduit.Permanent(positionErr)
		}
		b.wake(message.Data.OrgID)
		return nil
	}, conduit.Consumer("realtime")); subscribeErr != nil {
		return nil, subscribeErr
	}
	return b, nil
}

func (b *Bus) Watch(org string) (wake <-chan struct{}, unsubscribe func()) {
	b.mu.Lock()
	b.next++
	id := b.next
	ch := make(chan struct{}, 1)
	b.listeners[id] = watcher{org: org, wake: ch}
	b.mu.Unlock()
	return ch, func() { b.mu.Lock(); delete(b.listeners, id); b.mu.Unlock() }
}

func (b *Bus) wake(org string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, listener := range b.listeners {
		if org != "" && org != listener.org {
			continue
		}
		select {
		case listener.wake <- struct{}{}:
		default:
		}
	}
}

func (b *Bus) Start(ctx context.Context) error {
	if b.cancel != nil {
		return errors.New("realtime bus already started")
	}
	ctx, cancel := context.WithCancel(ctx)
	conn, err := pgx.Connect(ctx, b.dsn)
	if err != nil {
		cancel()
		return err
	}
	if _, err = conn.Exec(ctx, "LISTEN loomplane_events"); err != nil {
		closeListener(ctx, conn)
		cancel()
		return err
	}
	if err = b.runtime.Start(ctx); err != nil {
		closeListener(ctx, conn)
		cancel()
		return err
	}
	b.cancel = cancel
	b.wg.Add(2)
	go b.listen(ctx, conn)
	go func() {
		defer b.wg.Done()
		interval := b.CatchupInterval
		if interval <= 0 {
			interval = 2 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.wake("")
			}
		}
	}()
	return nil
}

func (b *Bus) listen(ctx context.Context, conn *pgx.Conn) {
	defer b.wg.Done()
	defer func() {
		if conn != nil {
			closeListener(ctx, conn)
		}
	}()
	for ctx.Err() == nil {
		if conn == nil {
			var err error
			conn, err = pgx.Connect(ctx, b.dsn)
			if err == nil {
				_, err = conn.Exec(ctx, "LISTEN loomplane_events")
			}
			if err != nil {
				if conn != nil {
					closeListener(ctx, conn)
					conn = nil
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}
			b.wake("")
		}
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			closeListener(ctx, conn)
			conn = nil
			continue
		}
		var wake Wakeup
		if json.Unmarshal([]byte(notification.Payload), &wake) != nil {
			continue
		}
		if _, err = position(wake.OrgID, wake.Cursor); err != nil {
			continue
		}
		// If publication fails, the catch-up ticker still reads durable rows.
		publicationID := fmt.Sprintf("%x", sha256.Sum256([]byte(wake.Cursor)))
		if _, publishErr := conduit.Publish(ctx, b.runtime, wakeupEvent, wake, conduit.MessageID(publicationID)); publishErr != nil && b.Warn != nil {
			b.Warn(publishErr)
		}
	}
}

func closeListener(ctx context.Context, conn *pgx.Conn) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_ = conn.Close(cleanup)
}

func (b *Bus) Stop(ctx context.Context) error {
	if b.cancel == nil {
		return nil
	}
	b.cancel()
	b.wg.Wait()
	return b.runtime.Stop(ctx)
}
