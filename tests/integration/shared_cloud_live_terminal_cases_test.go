//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/provider"
)

type liveTerminalStore struct {
	cloudinfra.Store
	alive       *bool
	unavailable bool
}

func (s liveTerminalStore) GetOperation(ctx context.Context, _, _, _ string) (*cloudinfra.Operation, error) {
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		return nil, errors.New("unbounded snapshot")
	}
	if s.unavailable || (s.alive != nil && !*s.alive) {
		return nil, errors.New("secret-provider-error")
	}
	return &cloudinfra.Operation{Status: "succeeded", Enrolled: true, Attempt: 1, Attempted: true, Message: "secret-provider-error", HostKey: "secret-key", ServerID: "known-server", Actions: []provider.InfrastructureAction{{Status: "completed", ID: "secret-action"}, {Status: "secret-status"}}}, nil
}
func (s liveTerminalStore) GetServer(context.Context, string, string, string) (*cloudinfra.Server, error) {
	return &cloudinfra.Server{Status: "ready", RuntimeConnectionID: "secret-connection", AccountID: "secret-account", Health: cloudinfra.RuntimeHealth{DockerID: "secret-daemon", CPUs: 1, MemoryBytes: 2 << 30, EffectiveCPUMillis: 0, EffectiveMemoryBytes: 0}}, nil
}

func TestSharedCloudLiveTerminalBeforeCleanup(t *testing.T) {
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestSharedCloudLiveTerminalChild$")
	child.Env = append(os.Environ(), "LOOMPLANE_TERMINAL_TEST="+directory)
	if child.Run() == nil {
		t.Fatal("pool failure fixture passed")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "run-terminal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("terminal evidence leaked raw fields")
	}
	var snapshot liveTerminalSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Stage != "host_pool" || snapshot.HTTPStatus != http.StatusConflict || snapshot.Code != "captured" || len(snapshot.Hosts) != 1 {
		t.Fatalf("incorrect terminal codes: %+v", snapshot)
	}
	host := snapshot.Hosts[0]
	if host.OperationStatus != "succeeded" || !host.Enrolled || host.ServerStatus != "ready" || host.EffectiveCPUMillis != 0 || host.MemoryBytes != 2<<30 || host.ProviderActions[1] != "unknown" || host.OperationCode != "unknown" {
		t.Fatalf("incorrect terminal projection: %+v", host)
	}
	progressRaw, err := os.ReadFile(filepath.Join(directory, "run-progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	var progress liveProgress
	if err = json.Unmarshal(progressRaw, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Code != "http_request_failed" || progress.HTTPStatus != http.StatusConflict || progress.Status != "failed" {
		t.Fatalf("HTTP failure not retained: %+v", progress)
	}
	if _, err = os.Stat(filepath.Join(directory, "database-dropped")); err != nil {
		t.Fatal("fixture cleanup did not run")
	}
	progress.path = filepath.Join(directory, "run-progress.json")
	if err = liveCaptureTerminal(filepath.Join(directory, "state.json"), &progress, liveTerminalStore{}, "org", "cloud", nil); err == nil {
		t.Fatal("existing terminal snapshot overwritten")
	}
	retained, err := os.ReadFile(filepath.Join(directory, "run-terminal.json"))
	if err != nil || string(retained) != string(raw) {
		t.Fatal("terminal evidence changed")
	}
}

func TestSharedCloudLiveTerminalChild(t *testing.T) {
	directory := os.Getenv("LOOMPLANE_TERMINAL_TEST")
	if directory == "" {
		t.Skip("subprocess fixture only")
	}
	alive := true
	defer func() {
		alive = false
		_ = os.WriteFile(filepath.Join(directory, "database-dropped"), []byte("done"), 0o600)
	}()
	progress := newLiveProgress(t, directory)
	progress.step(t, "host_pool")
	defer func() {
		if err := liveCaptureTerminal(filepath.Join(directory, "state.json"), progress, liveTerminalStore{alive: &alive}, "org", "cloud", []liveHostIntent{{Operation: "known"}}); err != nil {
			t.Error(err)
		}
	}()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("secret-response"))
	}))
	defer srv.Close()
	c := &client{t: t, base: srv.URL, observeStatus: func(status int) { progress.observeHTTP(t, status) }}
	c.must(http.StatusOK, http.MethodPost, "/pool", nil, nil)
}

func TestSharedCloudLiveTerminalUnavailable(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	progress := &liveProgress{Stage: "host_wait"}
	if err := liveCaptureTerminal(path, progress, liveTerminalStore{unavailable: true}, "org", "cloud", []liveHostIntent{{Operation: "known"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "run-terminal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot liveTerminalSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Code != "read_incomplete" || snapshot.Hosts[0].ReadCode != "operation_unavailable" || strings.Contains(string(raw), "secret-") {
		t.Fatal("unsafe unavailable snapshot")
	}
	for _, message := range []string{"Organization management access was revoked.", "secret-provider-error"} {
		code := liveOperationCode(message)
		if code != "manager_revoked" && code != "unknown" {
			t.Fatal("unsafe operation failure code")
		}
	}
}
