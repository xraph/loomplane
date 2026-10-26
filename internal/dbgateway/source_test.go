package dbgateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

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

func TestSourceDeterministic(t *testing.T) {
	a, idA, err := Source()
	if err != nil {
		t.Fatal(err)
	}
	b, idB, err := Source()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || idA != idB {
		t.Error("two calls made different sources")
	}
	if len(idA) != 12 || !strings.HasPrefix(idA, "dg") {
		t.Errorf("id %q, want dg and ten hex characters", idA)
	}
}

func TestProgramIDIsSourcesID(t *testing.T) {
	_, id, err := Source()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got, err := ProgramID(); err != nil || got != id {
			t.Fatalf("ProgramID = %q %v, want %q", got, err, id)
		}
	}
}

func TestSourceLayout(t *testing.T) {
	data, _, err := Source()
	if err != nil {
		t.Fatal(err)
	}
	got := unpack(t, data)
	for _, name := range []string{"dbgateway/go.mod", "dbgateway/go.sum", "dbgateway/main.go", "dbgateway/Dockerfile", "dbgateway/plane/run.go"} {
		if len(got[name]) == 0 {
			t.Errorf("%s is missing or empty", name)
		}
	}
	for name := range got {
		if !strings.HasPrefix(name, "dbgateway/") {
			t.Errorf("%s is outside the top directory", name)
		}
	}
	// Every shipped plane file is in the tar.
	entries, err := os.ReadDir("plane")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		if len(got["dbgateway/plane/"+n]) == 0 {
			t.Errorf("plane/%s isn't in the source", n)
		}
	}
}

func TestSourceExcludesTests(t *testing.T) {
	data, _, err := Source()
	if err != nil {
		t.Fatal(err)
	}
	for name := range unpack(t, data) {
		if strings.HasSuffix(name, "_test.go") || strings.Contains(name, "testdata") {
			t.Errorf("%s is in the source", name)
		}
	}
}

// TestProgramModuleMatchesRoot keeps the generated module on the versions
// this repository builds and tests the data plane with.
func TestProgramModuleMatchesRoot(t *testing.T) {
	versions := func(path string) map[string]string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := modfile.Parse(path, b, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range f.Require {
			out[r.Mod.Path] = r.Mod.Version
		}
		return out
	}
	root := versions(filepath.Join("..", "..", "go.mod"))
	prog := versions(filepath.Join("program", "go.mod.txt"))
	for _, mod := range []string{"github.com/jackc/pgx/v5", "github.com/gorilla/websocket"} {
		if prog[mod] == "" {
			t.Errorf("%s isn't required by the program's go.mod", mod)
		} else if prog[mod] != root[mod] {
			t.Errorf("%s is %s in the program, %s in the root go.mod", mod, prog[mod], root[mod])
		}
	}
	// The program builds against at least the golang.org/x/ versions the
	// root module does.
	for mod, v := range prog {
		if !strings.HasPrefix(mod, "golang.org/x/") {
			continue
		}
		want, ok := root[mod]
		if !ok {
			t.Errorf("%s is required by the program but not by the root go.mod", mod)
		} else if semver.Compare(v, want) < 0 {
			t.Errorf("%s is %s in the program, older than %s in the root go.mod", mod, v, want)
		}
	}
}

// TestSourceBuilds compiles the generated program, which fetches its
// modules the first time.
func TestSourceBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the gateway, fetching its modules")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go isn't on PATH")
	}
	data, _, err := Source()
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
	src := filepath.Join(dir, "dbgateway")
	for _, args := range [][]string{{"vet", "./..."}, {"build", "-o", "bin", "."}} {
		cmd := exec.CommandContext(t.Context(), "go", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
