//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// infrastructureBrowser exposes the real app to a separately scoped Vite
// process. Files control test-only transport failures without adding a product
// endpoint, changing the user's running API or reading browser credentials.
type infrastructureBrowser struct {
	proxy     *httptest.Server
	target    atomic.Pointer[url.URL]
	directory string
	stop      sync.Once
	shutdown  func()
}

func startInfrastructureBrowser(t *testing.T, appURL, statePath string) *infrastructureBrowser {
	t.Helper()
	b := &infrastructureBrowser{directory: filepath.Dir(statePath)}
	b.setTarget(t, appURL)
	b.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/v1/realtime/ws" {
			if _, err := os.Stat(filepath.Join(b.directory, "disable-ws")); err == nil {
				http.Error(w, "acceptance websocket outage", http.StatusServiceUnavailable)
				return
			}
		}
		proxy := httputil.NewSingleHostReverseProxy(b.target.Load())
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(b.close)
	if os.Getenv("LOOMPLANE_ACCEPTANCE_BROWSER") != "1" {
		return b
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	command := exec.CommandContext(ctx, "node", "node_modules/vite/bin/vite.js", "--host", "127.0.0.1", "--port", "5190", "--strictPort")
	command.Dir = "../../apps/dashboard"
	command.Env = append(os.Environ(), "LOOMPLANE_API_URL="+b.proxy.URL)
	output, err := os.Create(filepath.Join(b.directory, "dashboard.log"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = output, output
	if err = command.Start(); err != nil {
		cancel()
		_ = output.Close()
		t.Fatal(err)
	}
	b.shutdown = func() { cancel(); _ = command.Wait(); _ = output.Close() }
	return b
}

func (b *infrastructureBrowser) close() {
	b.stop.Do(func() {
		if b.shutdown != nil {
			b.shutdown()
		}
		b.proxy.CloseClientConnections()
		b.proxy.Close()
	})
}

func (b *infrastructureBrowser) setTarget(t *testing.T, appURL string) {
	t.Helper()
	target, err := url.Parse(appURL)
	if err != nil {
		t.Fatal(err)
	}
	b.target.Store(target)
}

func (b *infrastructureBrowser) checkpoint(t *testing.T, stage string, metadata map[string]string) {
	t.Helper()
	if os.Getenv("LOOMPLANE_ACCEPTANCE_BROWSER") != "1" {
		return
	}
	metadata["stage"] = stage
	metadata["url"] = "http://127.0.0.1:5190"
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(b.directory, "browser.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("browser acceptance checkpoint %s: localhost:5190; create %s to continue", stage, filepath.Join(b.directory, "continue-"+stage))
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(filepath.Join(b.directory, "continue-"+stage)); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("browser acceptance checkpoint %s timed out", stage)
}
