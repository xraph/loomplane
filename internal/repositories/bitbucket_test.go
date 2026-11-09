package repositories_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
)

const (
	trunkSHA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tagSHA     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	commitSHA  = "cccccccccccccccccccccccccccccccccccccccc"
	releaseSHA = "dddddddddddddddddddddddddddddddddddddddd"
)

// fakeBitbucket serves the corners of Bitbucket Cloud that Loomplane reads,
// and records what it was asked for.
type fakeBitbucket struct {
	mu     sync.Mutex
	auth   []string
	paths  []string
	status int // when set, every request gets it
	tgz    []byte
}

func (f *fakeBitbucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.paths = append(f.paths, r.URL.EscapedPath())
	f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch r.URL.EscapedPath() {
	case "/repositories/acme/api":
		_, _ = w.Write([]byte(`{"mainbranch":{"name":"trunk"}}`))
	case "/repositories/acme/api/refs/branches/trunk":
		_, _ = w.Write([]byte(`{"target":{"hash":"` + trunkSHA + `"}}`))
	case "/repositories/acme/api/refs/branches/release/1.2":
		_, _ = w.Write([]byte(`{"target":{"hash":"` + releaseSHA + `"}}`))
	case "/repositories/acme/api/refs/tags/v1.2":
		_, _ = w.Write([]byte(`{"target":{"hash":"` + tagSHA + `"}}`))
	case "/repositories/acme/api/commit/abc1234":
		_, _ = w.Write([]byte(`{"hash":"` + commitSHA + `"}`))
	case "/acme/api/get/" + trunkSHA + ".tar.gz":
		// Bitbucket redirects archives to its upload host.
		http.Redirect(w, r, "/uploads/api.tar.gz", http.StatusFound)
	case "/uploads/api.tar.gz":
		_, _ = w.Write(f.tgz)
	default:
		http.NotFound(w, r)
	}
}

// bitbucketTarball is a small checkout as Bitbucket serves it: one
// top-level "<workspace>-<repo>-<shortsha>/" directory.
func bitbucketTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name string, typ byte, body []byte) {
		t.Helper()
		hdr := &tar.Header{Name: name, Typeflag: typ, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	add("acme-api-abc1234/", tar.TypeDir, nil)
	add("acme-api-abc1234/package.json", tar.TypeReg, []byte(`{"name":"api"}`))
	add("acme-api-abc1234/src/main.go", tar.TypeReg, []byte("package main\n"))
	add("acme-api-abc1234/node_modules/x/index.js", tar.TypeReg, []byte("never written"))
	add("acme-api-abc1234/huge.bin", tar.TypeReg, make([]byte, 2048))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newFakeBitbucket(t *testing.T) (*fakeBitbucket, *repositories.Bitbucket) {
	t.Helper()
	f := &fakeBitbucket{tgz: bitbucketTarball(t)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	b := repositories.NewBitbucket(srv.URL, srv.URL) // the fake serves /repositories/... at its root
	b.Limits.File = 1024
	return f, b
}

var bitbucketAPI = repositories.Repository{Host: repositories.HostBitbucket, Owner: "acme", Name: "api"}

func TestBitbucketResolve(t *testing.T) {
	ctx := context.Background()
	f, b := newFakeBitbucket(t)
	tests := []struct {
		name, ref, want string
	}{
		{"default branch", "", trunkSHA},
		{"a branch", "trunk", trunkSHA},
		{"a tag, which is no branch", "v1.2", tagSHA},
		{"a commit, which is neither", "abc1234", commitSHA},
		{"a branch with a slash", "release/1.2", releaseSHA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := b.Resolve(ctx, bitbucketAPI, tt.ref, "")
			if err != nil || got != tt.want {
				t.Fatalf("Resolve(%q) = %q, %v; want %q", tt.ref, got, err, tt.want)
			}
		})
	}

	f.mu.Lock()
	f.paths = nil
	f.mu.Unlock()
	full := strings.Repeat("e", 40)
	if got, err := b.Resolve(ctx, bitbucketAPI, full, ""); err != nil || got != full {
		t.Fatalf("Resolve(full SHA) = %q, %v", got, err)
	}
	if len(f.paths) != 0 {
		t.Fatalf("a full SHA asked Bitbucket: %v", f.paths)
	}
	// Each segment of a ref is escaped on its own; the slash stays.
	if _, err := b.Resolve(ctx, bitbucketAPI, "release/1.2", ""); err != nil {
		t.Fatal(err)
	}
	if want := "/repositories/acme/api/refs/branches/release/1.2"; f.paths[len(f.paths)-1] != want {
		t.Fatalf("asked %q, want %q", f.paths[len(f.paths)-1], want)
	}
	if _, err := b.Resolve(ctx, bitbucketAPI, "nope", ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("a ref nothing has: %v", err)
	}
	if _, err := b.Resolve(ctx, bitbucketAPI, "../../etc", ""); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("a bad ref: %v", err)
	}
}

func TestBitbucketResolve_errors(t *testing.T) {
	ctx := context.Background()
	const token = "s3cret-t0ken"
	tests := []struct {
		status int
		is     error
	}{
		{http.StatusNotFound, repositories.ErrNotFound},
		{http.StatusUnauthorized, repositories.ErrNotFound},
		{http.StatusForbidden, repositories.ErrNotFound},
		{http.StatusTooManyRequests, repositories.ErrRateLimited},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			f, b := newFakeBitbucket(t)
			f.status = tt.status
			if _, err := b.Resolve(ctx, bitbucketAPI, "trunk", token); !errors.Is(err, tt.is) {
				t.Fatalf("status %d: %v, want %v", tt.status, err, tt.is)
			}
		})
	}
	t.Run("a server error names the status, not the token", func(t *testing.T) {
		f, b := newFakeBitbucket(t)
		f.status = http.StatusInternalServerError
		_, err := b.Resolve(ctx, bitbucketAPI, "trunk", token)
		if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), token) {
			t.Fatalf("500: %v", err)
		}
		if errors.Is(err, repositories.ErrNotFound) || errors.Is(err, repositories.ErrRateLimited) {
			t.Fatalf("500 read as a permanent answer: %v", err)
		}
	})
	t.Run("a refusal is not retried against the other kinds of ref", func(t *testing.T) {
		f, b := newFakeBitbucket(t)
		f.status = http.StatusForbidden
		_, _ = b.Resolve(ctx, bitbucketAPI, "trunk", "")
		if len(f.paths) != 1 {
			t.Fatalf("asked %d times: %v", len(f.paths), f.paths)
		}
	})
}

func TestBitbucketFetch(t *testing.T) {
	ctx := context.Background()
	f, b := newFakeBitbucket(t)
	dest := t.TempDir()
	st, err := b.Fetch(ctx, bitbucketAPI, trunkSHA, "t0k", dest)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if st.Files != 2 || st.Skipped != 2 { // package.json and src/main.go; node_modules and huge.bin
		t.Fatalf("stats %+v", st)
	}
	for _, rel := range []string{"package.json", "src/main.go"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	for _, never := range []string{"acme-api-abc1234", "node_modules", "huge.bin"} {
		if _, err := os.Lstat(filepath.Join(dest, never)); !os.IsNotExist(err) {
			t.Fatalf("%s was written", never)
		}
	}
	if f.auth[0] != "Bearer t0k" {
		t.Fatalf("authorization %q", f.auth[0])
	}

	if _, err := b.Fetch(ctx, bitbucketAPI, trunkSHA, "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := f.auth[len(f.auth)-2]; got != "" {
		t.Fatalf("an anonymous read sent authorization %q", got)
	}

	if _, err := b.Fetch(ctx, bitbucketAPI, "trunk", "", t.TempDir()); err == nil || !strings.Contains(err.Error(), "full commit SHA") {
		t.Fatalf("fetch by branch: %v", err)
	}
}

func TestBitbucketFetch_limits(t *testing.T) {
	ctx := context.Background()
	tests := map[string]func(b *repositories.Bitbucket){
		"archive": func(b *repositories.Bitbucket) { b.Limits.Archive = 64 },
		"total":   func(b *repositories.Bitbucket) { b.Limits.Total = 8 },
	}
	for name, limit := range tests {
		t.Run(name, func(t *testing.T) {
			_, b := newFakeBitbucket(t)
			limit(b)
			if _, err := b.Fetch(ctx, bitbucketAPI, trunkSHA, "", t.TempDir()); !errors.Is(err, repositories.ErrTooLarge) {
				t.Fatalf("Fetch over the %s limit: %v", name, err)
			}
		})
	}
}

func TestBitbucketDownload(t *testing.T) {
	ctx := context.Background()
	f, b := newFakeBitbucket(t)
	var buf bytes.Buffer
	n, err := b.Download(ctx, bitbucketAPI, trunkSHA, "t0k", &buf)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if n != int64(len(f.tgz)) || !bytes.Equal(buf.Bytes(), f.tgz) {
		t.Fatalf("got %d bytes, want the %d the server sent", n, len(f.tgz))
	}
	if _, err := b.Download(ctx, bitbucketAPI, "trunk", "", &buf); err == nil || !strings.Contains(err.Error(), "full commit SHA") {
		t.Fatalf("download by branch: %v", err)
	}
	b.Limits.Archive = 16
	if _, err := b.Download(ctx, bitbucketAPI, trunkSHA, "", &buf); !errors.Is(err, repositories.ErrTooLarge) {
		t.Fatalf("download over the limit: %v", err)
	}
}

func TestBitbucketDefaultBranch(t *testing.T) {
	_, b := newFakeBitbucket(t)
	got, err := b.DefaultBranch(context.Background(), bitbucketAPI, "")
	if err != nil || got != "trunk" {
		t.Fatalf("DefaultBranch = %q, %v", got, err)
	}
}
