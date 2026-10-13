package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var routes = []Route{
	{Path: "/api", To: "api", Env: "API_URL", StripPrefix: true},
	{Path: "/", To: "web", Env: "WEB_URL"},
}

func unpack(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = b
	}
}

func TestSource(t *testing.T) {
	data, id, err := Source(routes)
	if err != nil {
		t.Fatal(err)
	}
	got := unpack(t, data)
	for _, name := range []string{"gateway/main.go", "gateway/go.mod", "gateway/go.sum", "gateway/Dockerfile", "gateway/routes.json"} {
		if len(got[name]) == 0 {
			t.Errorf("%s is missing or empty", name)
		}
	}
	var table []Route
	if err := json.Unmarshal(got["gateway/routes.json"], &table); err != nil || len(table) != 2 || table[0].Path != "/" {
		t.Fatalf("routes.json %s (%v)", got["gateway/routes.json"], err)
	}
	if len(id) != 12 || !strings.HasPrefix(id, "gw") {
		t.Errorf("id %q", id)
	}

	// The same routes, in any order, are the same source.
	again, id2, _ := Source([]Route{routes[1], routes[0]})
	if id2 != id || !bytes.Equal(again, data) {
		t.Error("the same routes made a different source")
	}
	_, other, _ := Source(routes[:1])
	if other == id {
		t.Error("different routes made the same ID")
	}
}

// TestSource_builds compiles the generated program, which fetches its
// modules the first time.
func TestSource_builds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the gateway, fetching its modules")
	}
	data, _, err := Source(routes)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, b := range unpack(t, data) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "go", "vet", ".")
	cmd.Dir = filepath.Join(dir, "gateway")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the gateway doesn't build: %v\n%s", err, out)
	}
}

// TestSource_routes builds the generated gateway and runs it in front of
// two upstreams: each request reaches the service its path routes to,
// with /api stripped and a trailing slash kept as the client sent it.
func TestSource_routes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the gateway, fetching its modules")
	}
	data, _, err := Source(routes)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, b := range unpack(t, data) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "gw")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	build.Dir = filepath.Join(dir, "gateway")
	build.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the gateway doesn't build: %v\n%s", err, out)
	}

	seen := make(chan string, 8)
	upstream := func(name string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- name + " " + r.URL.RequestURI()
			_, _ = io.WriteString(w, name)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	api, web := upstream("api"), upstream("web")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	run := exec.CommandContext(t.Context(), bin)
	// Run it where it has no config file, as in its container. Forge looks
	// for one upward from the working directory, and this repository's
	// would otherwise be found.
	run.Dir = filepath.Join(dir, "gateway")
	run.Env = append(os.Environ(), "PORT="+port, "API_URL="+api.URL, "WEB_URL="+web.URL,
		"LOOMPLANE_ROUTES="+filepath.Join(dir, "gateway", "routes.json"))
	var logs bytes.Buffer
	run.Stdout, run.Stderr = &logs, &logs
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Process.Kill(); _ = run.Wait() })

	base := "http://127.0.0.1:" + port
	deadline := time.Now().Add(30 * time.Second)
	// The gateway listens a moment before its routes are in place, so
	// it's ready once / reaches web.
	last := ""
	for ready := false; !ready; {
		res, err := http.Get(base + "/") //nolint:noctx // a local test server
		if err == nil {
			body, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			last = res.Status + " " + string(body)
			ready = res.StatusCode == http.StatusOK
			if ready {
				<-seen
			}
		}
		if !ready && time.Now().After(deadline) {
			t.Fatalf("the gateway never routed /: %v, last %q\n%s", err, last, logs.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	for path, want := range map[string]string{
		"/api/users?page=2": "api /users?page=2",
		"/docs/":            "web /docs/",
		"/docs":             "web /docs",
	} {
		res, err := http.Get(base + path) //nolint:noctx // a local test server
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = res.Body.Close()
		select {
		case got := <-seen:
			if got != want {
				t.Errorf("%s reached %q, want %q", path, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s reached nothing (%d)\n%s", path, res.StatusCode, logs.String())
		}
	}
}
