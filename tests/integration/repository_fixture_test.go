//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryFixtureTarball(t *testing.T) {
	checkout := t.TempDir()
	fixture := filepath.Join(checkout, "examples", "polyglot", "nested")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	const contents = "fixture bytes\n"
	if err := os.WriteFile(filepath.Join(fixture, "app.txt"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	packageCWD := filepath.Join(checkout, "tests", "integration")
	if err := os.MkdirAll(packageCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	unavailable := filepath.Join(t.TempDir(), "gone", "tests", "integration", "discovery_test.go")
	staleCheckout := t.TempDir()
	staleFixture := filepath.Join(staleCheckout, "examples", "polyglot")
	if err := os.MkdirAll(staleFixture, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleFixture, "wrong.txt"), []byte("stale source"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, cwd, source string }{
		{"relocated package", packageCWD, unavailable},
		{"relocated root", checkout, unavailable},
		{"working checkout preferred", packageCWD, filepath.Join(staleCheckout, "tests", "integration", "discovery_test.go")},
		{"trimpath package", packageCWD, "github.com/xraph/loomplane/tests/integration/discovery_test.go"},
		{"source fallback", t.TempDir(), filepath.Join(packageCWD, "discovery_test.go")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := repositoryExampleTarball(tc.cwd, tc.source)
			if err != nil {
				t.Fatal(err)
			}
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer zr.Close()
			tr := tar.NewReader(zr)
			header, err := tr.Next()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if header.Name != "acme-mono-fedcba9/nested/app.txt" || header.Mode != 0o644 || header.Typeflag != tar.TypeReg || string(data) != contents {
				t.Fatalf("archive changed: %+v %q", header, data)
			}
			if _, err := tr.Next(); err != io.EOF {
				t.Fatalf("unexpected archive entry: %v", err)
			}
		})
	}
}

func TestRepositoryFixtureMissing(t *testing.T) {
	cwd := t.TempDir()
	_, err := repositoryExampleTarball(cwd, filepath.Join(cwd, "missing", "discovery_test.go"))
	if err == nil || !strings.Contains(err.Error(), "repository fixture examples/polyglot unavailable") || !strings.Contains(err.Error(), cwd) {
		t.Fatalf("missing fixture diagnostic: %v", err)
	}
}
