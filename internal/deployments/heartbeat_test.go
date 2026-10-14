package deployments

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestActivityHeartbeatKeepsLatestCheckpointAndStops(t *testing.T) {
	var mu sync.Mutex
	var received [][]byte
	_, save, stop := keepHeartbeat(t.Context(), []byte("initial"), func(data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, bytes.Clone(data))
		return nil
	}, time.Millisecond)
	if err := save([]byte("latest")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	stop()
	mu.Lock()
	count := len(received)
	for _, data := range received {
		if !bytes.Equal(data, []byte("latest")) {
			t.Fatalf("stale checkpoint: %q", data)
		}
	}
	mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(received) != count || count < 2 {
		t.Fatalf("heartbeat count: %d then %d", count, len(received))
	}
}

func TestActivityHeartbeatCancelsOnLeaseFailure(t *testing.T) {
	ctx, _, stop := keepHeartbeat(t.Context(), nil, func([]byte) error { return errors.New("lease lost") }, time.Millisecond)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("lost activity lease did not cancel the executor")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal(ctx.Err())
	}
}
