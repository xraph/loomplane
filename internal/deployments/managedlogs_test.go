package deployments

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
	"github.com/xraph/loomplane/internal/realtime"
)

type uniqueOutputSink struct {
	outputSink
	ids map[string]bool
}

func (s *uniqueOutputSink) AppendLog(ctx context.Context, line realtime.LogInput, redactor *realtime.Redactor) (*realtime.Log, error) {
	s.mu.Lock()
	if s.ids == nil {
		s.ids = map[string]bool{}
	}
	key := line.OperationID + "/" + line.Attempt + "/" + line.ProviderID
	if s.ids[key] {
		s.mu.Unlock()
		return &realtime.Log{}, nil
	}
	s.ids[key] = true
	s.mu.Unlock()
	return s.outputSink.AppendLog(ctx, line, redactor)
}

func TestManagedLogsDelayedTerminalOutputAndResume(t *testing.T) {
	sink := &uniqueOutputSink{}
	job := outputJob(sink)
	var mu sync.Mutex
	started := time.Now()
	seenCursor := ""
	read := func(_ context.Context, cursor string, _ bool, emit buildlogs.Emit) (string, bool, error) {
		mu.Lock()
		seenCursor = cursor
		mu.Unlock()
		if err := emit(buildlogs.Line{ID: "first", Stream: "stdout", Message: "connection-token runtime-secret"}); err != nil {
			return cursor, false, err
		}
		if time.Since(started) > 800*time.Millisecond {
			if err := emit(buildlogs.Line{ID: "last", Stream: "stderr", Message: "late"}); err != nil {
				return cursor, false, err
			}
		}
		return "saved", false, nil
	}
	out := startManagedOutput(context.Background(), job, "operation", "stable-attempt", read)
	for sink.count() == 0 && time.Since(started) < time.Second {
		time.Sleep(time.Millisecond)
	}
	out.finish(context.Background(), true)
	if sink.count() != 2 {
		t.Fatalf("lost delayed output: %+v", sink.logs)
	}
	if sink.logs[0].Message == "connection-token runtime-secret" {
		t.Fatal("secrets persisted")
	}
	job.Attempt = "changed-worker-attempt"
	resumed := startManagedOutput(context.Background(), job, "operation", "stable-attempt", read)
	resumed.finish(context.Background(), true)
	mu.Lock()
	cursor := seenCursor
	mu.Unlock()
	if cursor != "saved" || sink.count() != 2 {
		t.Fatalf("resume %q: %+v", cursor, sink.logs)
	}
}

func TestManagedLogsFailureDoesNotBlockLifecycle(t *testing.T) {
	for _, status := range []int{403, 429} {
		sink := &uniqueOutputSink{}
		out := startManagedOutput(context.Background(), outputJob(sink), "operation", "1", func(context.Context, string, bool, buildlogs.Emit) (string, bool, error) {
			return "", false, &buildlogs.Error{Status: status, RetryAfter: time.Millisecond, Message: "denied"}
		})
		out.finish(context.Background(), true)
		if len(sink.states) == 0 || sink.states[len(sink.states)-1].State == "collecting" {
			t.Fatalf("unfinished error: %+v", sink.states)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	sink := &uniqueOutputSink{}
	out := startManagedOutput(ctx, outputJob(sink), "hanging", "1", func(ctx context.Context, _ string, _ bool, _ buildlogs.Emit) (string, bool, error) {
		<-ctx.Done()
		return "", false, ctx.Err()
	})
	started := time.Now()
	out.finish(ctx, true)
	if time.Since(started) > time.Second || len(sink.states) == 0 || sink.states[len(sink.states)-1].State != "incomplete" {
		t.Fatalf("unbounded finalization: %+v", sink.states)
	}
}

func TestManagedLogsWaitsForInitiallyEmptyDelayedOutput(t *testing.T) {
	sink := &uniqueOutputSink{}
	started := time.Now()
	out := startManagedOutput(context.Background(), outputJob(sink), "empty-then-late", "1", func(_ context.Context, cursor string, _ bool, emit buildlogs.Emit) (string, bool, error) {
		if time.Since(started) > 800*time.Millisecond {
			return "end", false, emit(buildlogs.Line{ID: "late", Message: "last line"})
		}
		return cursor, false, nil
	})
	out.finish(context.Background(), true)
	if sink.count() != 1 {
		t.Fatalf("empty range ended before final output: %+v", sink.states)
	}
}

func TestManagedLogsRetentionFailureDoesNotAdvanceCursor(t *testing.T) {
	read := func(_ context.Context, _ string, _ bool, emit buildlogs.Emit) (string, bool, error) {
		err := emit(buildlogs.Line{ID: "a", Message: "line"})
		return "advanced", false, err
	}
	sink := &failedLogSink{outputSink: outputSink{}}
	out := startManagedOutput(context.Background(), outputJob(sink), "failed", "1", read)
	out.finish(context.Background(), true)
	for _, state := range sink.states {
		if state.Cursor == "advanced" {
			t.Fatal("cursor advanced past lost line")
		}
	}
}

type failedLogSink struct{ outputSink }

func (*failedLogSink) AppendLog(context.Context, realtime.LogInput, *realtime.Redactor) (*realtime.Log, error) {
	return nil, errors.New("store failed")
}
