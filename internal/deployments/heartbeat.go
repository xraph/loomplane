package deployments

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// keepHeartbeat keeps a slow build or readiness wait alive without changing
// its durable checkpoint. Sends serialize so an older tick cannot replace a
// newer executor checkpoint.
func keepHeartbeat(parent context.Context, resume []byte, send func([]byte) error, every time.Duration) (activityContext context.Context, checkpoint func([]byte) error, stop func()) {
	if send == nil {
		return parent, func([]byte) error { return nil }, func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	var mu sync.Mutex
	latest := bytes.Clone(resume)
	save := func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if err := send(data); err != nil {
			cancel()
			return err
		}
		latest = bytes.Clone(data)
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			mu.Lock()
			err := send(bytes.Clone(latest))
			mu.Unlock()
			if err != nil {
				cancel()
				return
			}
		}
	}()
	return ctx, save, func() { cancel(); <-done }
}
