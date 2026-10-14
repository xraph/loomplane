package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/plugins"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/pkg/provider"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

type outputProvider struct {
	hold    chan struct{}
	applies atomic.Int32
}

func (*outputProvider) OperationLogs() bool { return true }
func (*outputProvider) Describe() provider.Provider {
	return provider.Provider{Name: "output", Title: "Output", Cloud: "output", Model: provider.Build, Runs: []provider.Runs{{Workload: provider.HTTPService, Execution: provider.Container, Creates: "service"}}}
}
func (*outputProvider) Check(context.Context, sdk.Credential) (*sdk.Account, error) {
	return &sdk.Account{}, nil
}
func (p *outputProvider) Apply(_ context.Context, job *sdk.Job) (*sdk.Outcome, error) {
	p.applies.Add(1)
	job.Log("stdout", "same line connection-token runtime-secret")
	job.Log("stdout", "same line connection-token runtime-secret")
	job.Progress("working connection-token")
	if p.hold != nil {
		<-p.hold
	}
	return &sdk.Outcome{ExternalID: "fixture"}, nil
}
func (*outputProvider) Remove(context.Context, *sdk.Job) error { return nil }
func (*outputProvider) Observe(context.Context, *sdk.Target) (*sdk.Observation, error) {
	return &sdk.Observation{}, nil
}

type outputSink struct {
	mu     sync.Mutex
	logs   []realtime.LogInput
	states []realtime.Collection
}

func (s *outputSink) AppendLog(_ context.Context, input realtime.LogInput, _ *realtime.Redactor) (*realtime.Log, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, input)
	return &realtime.Log{}, nil
}
func (s *outputSink) SaveCheckpoint(_ context.Context, input realtime.LogInput, cursor, state, message string, _ *realtime.Redactor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = append(s.states, realtime.Collection{OrgID: input.OrgID, ProjectID: input.ProjectID, DeploymentID: input.DeploymentID, ActionID: input.ActionID, OperationID: input.OperationID, Attempt: input.Attempt, Cursor: cursor, State: state, Message: message})
	return nil
}
func (s *outputSink) Checkpoint(_ context.Context, input realtime.LogInput) (*realtime.Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.states) - 1; index >= 0; index-- {
		state := s.states[index]
		if state.OperationID == input.OperationID && state.Attempt == input.Attempt {
			return &state, nil
		}
	}
	return nil, realtime.ErrNotFound
}
func (s *outputSink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.logs) }

func outputJob(sink LogSink) *Job {
	return &Job{Deployment: &Deployment{ID: "dep_fixture", OrgID: "org_fixture", ProjectID: "proj_fixture", EnvironmentID: "env_fixture"}, Action: planning.Action{ID: "service:api", Name: "api"}, Connection: &connections.Connection{ID: "conn_fixture"}, Credential: []byte("connection-token"), Env: map[string]string{"TOKEN": "runtime-secret"}, Secrets: map[string]bool{"TOKEN": true}, Logs: sink, Attempt: "1.1", Checkpoint: func([]byte) error { return nil }, Progress: func(string, string) {}}
}

func TestPluginLogs_retainsDistinctOutputBeforeOperationCompletes(t *testing.T) {
	host := &outputProvider{hold: make(chan struct{})}
	srv := httptest.NewServer(sdk.Handler(host, "token"))
	defer srv.Close()
	sink := &outputSink{}
	job := outputJob(sink)
	progress := []string{}
	job.Progress = func(_, message string) { progress = append(progress, message) }
	p := &Plugin{Client: plugins.New(srv.URL, "token"), Every: time.Millisecond, OperationLogs: true}
	done := make(chan error, 1)
	go func() {
		_, err := p.run(t.Context(), job, sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "deploy:api"}})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for sink.count() < 2 {
		if time.Now().After(deadline) {
			close(host.hold)
			t.Fatal("output was not retained while the provider was running")
		}
		time.Sleep(time.Millisecond)
	}
	close(host.hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitOutputState(t, sink, "complete")
	logs, states := sink.snapshot()
	if len(logs) != 2 || logs[0].ProviderID == logs[1].ProviderID {
		t.Fatalf("repeated output lost identity: %+v", logs)
	}
	for _, line := range logs {
		if strings.Contains(line.Message, "connection-token") || strings.Contains(line.Message, "runtime-secret") {
			t.Fatal("credentials entered retained output")
		}
		if line.OrgID != "org_fixture" || line.ProjectID != "proj_fixture" || line.DeploymentID != "dep_fixture" || !strings.HasPrefix(line.Attempt, "1.1.") {
			t.Fatalf("wrong scope: %+v", line)
		}
	}
	for _, message := range progress {
		if strings.Contains(message, "connection-token") {
			t.Fatal("credential entered progress")
		}
	}
	if states[len(states)-1].State != "complete" {
		t.Fatalf("collection not complete: %+v", states)
	}
}

func TestPluginLogs_permissionFailureKeepsSuccessfulDeployment(t *testing.T) {
	handler := sdk.Handler(&outputProvider{}, "token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			w.WriteHeader(403)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	sink := &outputSink{}
	p := &Plugin{Client: plugins.New(srv.URL, "token"), Every: time.Millisecond, OperationLogs: true}
	op, err := p.run(t.Context(), outputJob(sink), sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "denied:api"}})
	if err != nil || op.Status != sdk.StatusSucceeded {
		t.Fatalf("log permission failed deployment: %+v %v", op, err)
	}
	waitOutputState(t, sink, "unavailable")
	_, states := sink.snapshot()
	if states[len(states)-1].State != "unavailable" {
		t.Fatalf("log permission hidden: %+v", states)
	}
}

func TestPluginLogs_restartKeepsEarlierOutputAndMarksIncomplete(t *testing.T) {
	first := &outputProvider{hold: make(chan struct{})}
	var mu sync.Mutex
	active := sdk.Handler(first, "token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		handler := active
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	sink := &outputSink{}
	job := outputJob(sink)
	p := &Plugin{Client: plugins.New(srv.URL, "token"), Every: time.Millisecond, OperationLogs: true}
	done := make(chan error, 1)
	go func() {
		_, err := p.run(t.Context(), job, sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "restart:api"}})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for sink.count() < 2 {
		if time.Now().After(deadline) {
			close(first.hold)
			t.Fatal("no pre-restart output")
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	active = sdk.Handler(&outputProvider{}, "token")
	mu.Unlock()
	close(first.hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitOutputCount(t, sink, 4)
	waitOutputState(t, sink, "incomplete")
	logs, states := sink.snapshot()
	if len(logs) != 4 || logs[0].ProviderID == logs[2].ProviderID {
		t.Fatalf("restart output identity: %+v", logs)
	}
	if states[len(states)-1].State != "incomplete" {
		t.Fatalf("restart loss was hidden: %+v", states)
	}
}

func waitOutputState(t *testing.T, sink *outputSink, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		sink.mu.Lock()
		found := len(sink.states) > 0 && sink.states[len(sink.states)-1].State == state
		sink.mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("collection never reached " + state)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *outputSink) snapshot() ([]realtime.LogInput, []realtime.Collection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]realtime.LogInput(nil), s.logs...), append([]realtime.Collection(nil), s.states...)
}
func waitOutputCount(t *testing.T, sink *outputSink, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for sink.count() < count {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d output records", count)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPluginLogs_hangingReaderCannotBlockSuccessfulOperation(t *testing.T) {
	handler := sdk.Handler(&outputProvider{}, "transport-bearer-token")
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs") {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	sink := &outputSink{}
	p := &Plugin{Client: plugins.New(srv.URL, "transport-bearer-token"), Every: time.Millisecond, OperationLogs: true}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	op, err := p.run(ctx, outputJob(sink), sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "slow-reader"}})
	if err != nil || op.Status != sdk.StatusSucceeded || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("log read delayed authoritative operation: %+v %v", op, err)
	}
	waitOutputState(t, sink, "incomplete")
}

func TestPluginLogs_workerRetryResumesSameExecutionAndCollectionAttempt(t *testing.T) {
	provider := &outputProvider{hold: make(chan struct{})}
	srv := httptest.NewServer(sdk.Handler(provider, "transport-bearer-token"))
	defer srv.Close()
	sink := &outputSink{}
	p := &Plugin{Client: plugins.New(srv.URL, "transport-bearer-token"), Every: time.Millisecond, OperationLogs: true}
	job := outputJob(sink)
	var checkpointMu sync.Mutex
	var checkpoint []byte
	job.Checkpoint = func(raw []byte) error {
		checkpointMu.Lock()
		checkpoint = append([]byte(nil), raw...)
		checkpointMu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := p.run(ctx, job, sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "worker-resume"}})
		done <- err
	}()
	waitOutputCount(t, sink, 2)
	// Ensure the durable log cursor is retained before simulating worker loss.
	deadline := time.Now().Add(time.Second)
	for {
		_, states := sink.snapshot()
		if len(states) > 0 && states[len(states)-1].Cursor != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cursor not retained")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("worker cancellation was not reported", err)
	}
	waitOutputState(t, sink, "error")
	checkpointMu.Lock()
	resume := append([]byte(nil), checkpoint...)
	checkpointMu.Unlock()
	var saved pluginCheckpoint
	if err := json.Unmarshal(resume, &saved); err != nil || saved.Execution == "" || saved.Operation == "" {
		t.Fatalf("operation identity not durable: %+v %v", saved, err)
	}
	next := outputJob(sink)
	next.Attempt = "1.2"
	next.Resume = resume
	var released sync.Once
	next.Checkpoint = func([]byte) error { released.Do(func() { close(provider.hold) }); return nil }
	op, err := p.run(t.Context(), next, sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "worker-resume"}})
	if err != nil || op.Status != sdk.StatusSucceeded {
		t.Fatalf("worker resume: %+v %v", op, err)
	}
	waitOutputState(t, sink, "complete")
	logs, _ := sink.snapshot()
	if len(logs) != 2 {
		t.Fatalf("worker replay duplicated provider output: %+v", logs)
	}
	for _, line := range logs {
		if line.Attempt != saved.Attempt {
			t.Fatal("ongoing execution changed collection attempt")
		}
	}
}

func TestPluginLogs_redactsTransportTokenBeforeStartFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_ = json.NewEncoder(w).Encode(sdk.Failure{Error: "provider failed with " + r.Header.Get("Authorization")})
	}))
	defer srv.Close()
	p := &Plugin{Client: plugins.New(srv.URL, "transport-bearer-token"), OperationLogs: true}
	_, err := p.run(t.Context(), outputJob(&outputSink{}), sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "start-failure"}})
	if err == nil || strings.Contains(err.Error(), "transport-bearer-token") {
		t.Fatalf("unsafe start error: %v", err)
	}
}

func TestPluginLogs_recoversTerminalOutputBeforeActivityAcknowledgement(t *testing.T) {
	host := &outputProvider{}
	srv := httptest.NewServer(sdk.Handler(host, "transport-bearer-token"))
	defer srv.Close()
	client := plugins.New(srv.URL, "transport-bearer-token")
	req := sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "terminal-resume"}}
	op, err := client.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for op.Status == sdk.StatusRunning {
		if time.Now().After(deadline) {
			t.Fatal("fixture did not finish")
		}
		op, err = client.Operation(t.Context(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	// This is the durable state retained if a process exits before final catch-up.
	attempt := pluginCollectionAttempt("1.1", op.ExecutionID)
	resume, err := json.Marshal(pluginCheckpoint{Version: "1", Key: req.Job.Key, Operation: op.ID, Execution: op.ExecutionID, Attempt: attempt, Terminal: true})
	if err != nil {
		t.Fatal(err)
	}
	sink := &outputSink{}
	job := outputJob(sink)
	job.Attempt = "1.2"
	job.Resume = resume
	p := &Plugin{Client: client, Every: time.Millisecond, OperationLogs: true}
	result, err := p.run(t.Context(), job, req)
	if err != nil || result.Status != sdk.StatusSucceeded {
		t.Fatalf("resume: %+v %v", result, err)
	}
	logs, states := sink.snapshot()
	if host.applies.Load() != 1 || len(logs) != 2 || states[len(states)-1].State != "complete" {
		t.Fatalf("terminal recovery re-executed or lost output: %d %+v %+v", host.applies.Load(), logs, states)
	}
}

func TestPluginLogs_terminalRecoveryAfterProviderRestartMarksGap(t *testing.T) {
	host := &outputProvider{}
	srv := httptest.NewServer(sdk.Handler(host, "transport-bearer-token"))
	defer srv.Close()
	state := pluginCheckpoint{Version: "1", Key: "terminal-restart", Operation: "apply-previous", Execution: "old-provider-epoch", Attempt: "1.1.previous", Terminal: true}
	resume, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	sink := &outputSink{}
	job := outputJob(sink)
	job.Attempt = "1.2"
	job.Resume = resume
	p := &Plugin{Client: plugins.New(srv.URL, "transport-bearer-token"), Every: time.Millisecond, OperationLogs: true}
	op, err := p.run(t.Context(), job, sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: state.Key}})
	if err != nil || op.Status != sdk.StatusSucceeded {
		t.Fatalf("restarted terminal recovery: %+v %v", op, err)
	}
	logs, states := sink.snapshot()
	if len(logs) != 2 || states[len(states)-1].State != "incomplete" {
		t.Fatalf("provider restart gap missing: %+v %+v", logs, states)
	}
}
