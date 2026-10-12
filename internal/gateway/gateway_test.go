package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
