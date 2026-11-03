package repositories_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/repositories"
)

// tarball is the examples/polyglot checkout as GitHub would serve it, with
// entries no checkout may write mixed in.
func tarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	write := func(hdr *tar.Header, body []byte) {
		t.Helper()
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	write(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": testSHA}}, nil)
	write(&tar.Header{Name: "acme-mono-0123456/", Typeflag: tar.TypeDir, Mode: 0o755}, nil)
	root := "../../examples/polyglot"
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		write(&tar.Header{Name: "acme-mono-0123456/" + filepath.ToSlash(rel), Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}, body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	evil := []byte("never written")
	for _, name := range []string{
		"acme-mono-0123456/../../escape.txt",
		"/etc/cron.d/x",
		"acme-mono-0123456/node_modules/left-pad/package.json",
		"acme-mono-0123456/apps/web/.next/server.js",
	} {
		write(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(evil))}, evil)
	}
	write(&tar.Header{Name: "acme-mono-0123456/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, nil)
	big := make([]byte, 2048)
	write(&tar.Header{Name: "acme-mono-0123456/huge.bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(big))}, big)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGitHub_resolveAndFetchExtractsSafely(t *testing.T) {
	ctx := context.Background()
	f, g := newFake(t)
	repo := repositories.Repository{Owner: "acme", Name: "mono"}

	sha, err := g.Resolve(ctx, repo, "", "ghs_token")
	if err != nil || sha != testSHA {
		t.Fatalf("Resolve default branch = %q, %v", sha, err)
	}
	if sha, err = g.Resolve(ctx, repo, "release/1.2", ""); err != nil || sha != testSHA {
		t.Fatalf("Resolve release/1.2 = %q, %v", sha, err)
	}

	dest := t.TempDir()
	st, err := g.Fetch(ctx, repo, sha, "ghs_token", dest)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if st.Skipped != 5 { // escape, absolute, node_modules, .next, huge
		t.Fatalf("stats %+v", st)
	}
	for _, never := range []string{"link", "huge.bin", "node_modules", "apps/web/.next"} {
		if _, err = os.Lstat(filepath.Join(dest, never)); !os.IsNotExist(err) {
			t.Fatalf("%s was written", never)
		}
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dest)), "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("an entry escaped the checkout")
	}
	if f.auth[0] != "Bearer ghs_token" {
		t.Fatalf("authorization %q", f.auth[0])
	}

	report, err := discovery.Analyze(ctx, os.DirFS(dest))
	if err != nil {
		t.Fatal(err)
	}
	if sum := graph.Build(report).Summarize(); sum.Services != 7 || sum.Packages != 3 || sum.Resources != 2 {
		t.Fatalf("analysis of the fetched checkout: %+v", sum)
	}
}

func TestGitHub_limits(t *testing.T) {
	ctx := context.Background()
	repo := repositories.Repository{Owner: "acme", Name: "mono"}
	tests := map[string]func(g *repositories.GitHub){
		"archive": func(g *repositories.GitHub) { g.MaxArchiveBytes = 512 },
		"total":   func(g *repositories.GitHub) { g.MaxTotalBytes = 1024 },
	}
	for name, limit := range tests {
		t.Run(name, func(t *testing.T) {
			_, g := newFake(t)
			limit(g)
			if _, err := g.Fetch(ctx, repo, testSHA, "", t.TempDir()); !errors.Is(err, repositories.ErrTooLarge) {
				t.Fatalf("Fetch over the %s limit: %v", name, err)
			}
		})
	}
}
