//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

const catalogProcessConfig = "LOOMPLANE_CATALOG_PROCESS_CONFIG"

func TestCatalogProcessWaitCancelsBlockedHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := awaitCatalogProcess(ctx, func(check context.Context) bool {
		request, err := http.NewRequestWithContext(check, http.MethodGet, server.URL, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return true
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stage deadline did not cancel its blocked HTTP read: %v", err)
	}
}

// TestCatalogRetirementCrashProcess runs only as a task-owned child. It boots
// the real Forge API and durable workers over the parent's persisted stores.
func TestCatalogRetirementCrashProcess(t *testing.T) {
	path := os.Getenv(catalogProcessConfig)
	if path == "" {
		t.Skip("internal subprocess helper")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(t.Context(), os.Interrupt)
	defer cancel()
	app := start(t, cfg)
	defer app.stop()
	if err = os.WriteFile(path+".ready", []byte(app.srv.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
}

type catalogProcess struct {
	command *exec.Cmd
	done    chan error
	url     string
	stopped bool
}

func startCatalogProcess(t *testing.T, cfg config.Config, name string) *catalogProcess {
	t.Helper()
	address, err := url.Parse(cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := address.Query()
	query.Set("application_name", name)
	address.RawQuery = query.Encode()
	cfg.Database.URL = address.String()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(filepath.Join(directory, "process.log"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestCatalogRetirementCrashProcess$", "-test.timeout=10m", "-test.v")
	command.Env = append(os.Environ(), catalogProcessConfig+"="+path)
	command.Stdout, command.Stderr = output, output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	child := &catalogProcess{command: command, done: make(chan error, 1)}
	go func() { child.done <- command.Wait() }()
	t.Cleanup(func() { child.stop(t, false) })
	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err = <-child.done:
			child.stopped = true
			t.Fatalf("catalog child exited before readiness: %v; log %s", err, output.Name())
		case <-deadline.C:
			t.Fatalf("catalog child did not become ready; log %s", output.Name())
		case <-tick.C:
			endpoint, readErr := os.ReadFile(path + ".ready")
			if readErr == nil {
				child.url = string(endpoint)
				t.Logf("catalog child %s is ready with PID %d", name, command.Process.Pid)
				return child
			}
			if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal(readErr)
			}
		}
	}
}

func (p *catalogProcess) stop(t *testing.T, crash bool) {
	t.Helper()
	if p.stopped {
		return
	}
	var signalErr error
	if crash {
		signalErr = p.command.Process.Kill()
	} else {
		signalErr = p.command.Process.Signal(os.Interrupt)
	}
	if signalErr != nil && (crash || !errors.Is(signalErr, os.ErrProcessDone)) {
		t.Error(signalErr)
	}
	select {
	case err := <-p.done:
		p.stopped = true
		if crash && err == nil {
			t.Error("crashed catalog process returned success")
		}
		if !crash && err != nil {
			t.Errorf("catalog child shutdown: %v", err)
		}
	case <-time.After(startTimeout):
		_ = p.command.Process.Kill()
		select {
		case <-p.done:
			p.stopped = true
		case <-time.After(10 * time.Second):
		}
		t.Error("catalog child did not stop within the shutdown deadline")
	}
}

func awaitCatalogProcess(ctx context.Context, ready func(context.Context) bool) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ready(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
