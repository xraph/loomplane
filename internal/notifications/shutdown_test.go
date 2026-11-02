package notifications

import (
	"context"
	"testing"
	"time"
)

func TestWithdrawAfterShutdownBeganDoesNothing(t *testing.T) {
	svc := &Service{closed: true}
	svc.withdraw(withdrawal{userID: "user-a", orgID: "org-a", collapse: "c", devices: []string{"d"}})
	done := make(chan struct{})
	go func() { svc.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a withdraw started after shutdown")
	}
}

func TestStartBackgroundContextIsCancelledByShutdownState(t *testing.T) {
	svc := &Service{}
	ctx, ok := svc.startBackground()
	if !ok || ctx.Err() != nil {
		t.Fatalf("ok=%v err=%v", ok, ctx.Err())
	}
	svc.wg.Done()
	svc.stopCancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("context not cancelled")
	}
}
