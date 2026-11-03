package oauth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGateLetsOneCallerPerKeyThrough(t *testing.T) {
	g := NewGate(8)
	var inside, most atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leave, err := g.Enter(context.Background(), "org/conn")
			if err != nil {
				t.Errorf("enter: %v", err)
				return
			}
			n := inside.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inside.Add(-1)
			leave()
		}()
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Fatalf("%d callers of one key were inside at once", most.Load())
	}
	if len(g.queue) != 0 {
		t.Fatalf("%d queues left behind", len(g.queue))
	}
}

func TestGateCapsCallersAcrossKeys(t *testing.T) {
	const slots = 2
	g := NewGate(slots)
	var inside, most atomic.Int32
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b", "c", "d", "e", "f"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leave, err := g.Enter(context.Background(), key)
			if err != nil {
				t.Errorf("enter: %v", err)
				return
			}
			n := inside.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inside.Add(-1)
			leave()
		}()
	}
	wg.Wait()
	if most.Load() > slots {
		t.Fatalf("%d callers were inside at once; the cap is %d", most.Load(), slots)
	}
}

func TestGateCallerGivingUpLeavesPromptly(t *testing.T) {
	g := NewGate(1)
	leave, err := g.Enter(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Enter(ctx, "k"); err == nil {
		t.Fatal("a waiting caller got through while the turn was held")
	}
	if _, err := g.Enter(ctx, "other"); err == nil {
		t.Fatal("a caller got a slot while none was free")
	}
	leave()
	again, err := g.Enter(context.Background(), "k")
	if err != nil {
		t.Fatalf("the gate stayed blocked after callers gave up: %v", err)
	}
	again()
	if len(g.queue) != 0 {
		t.Fatalf("%d queues left behind", len(g.queue))
	}
}
