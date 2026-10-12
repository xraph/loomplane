package artifacts

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestStore_putOpenExists(t *testing.T) {
	for _, dsn := range []string{"mem://", "file://" + t.TempDir()} {
		t.Run(dsn[:4], func(t *testing.T) {
			ctx := t.Context()
			s, err := Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(ctx) }()
			if ok, xerr := s.Exists(ctx, "sources/org/abc.tar.gz"); xerr != nil || ok {
				t.Fatalf("exists before put: %v %v", ok, xerr)
			}
			if _, _, oerr := s.Open(ctx, "sources/org/abc.tar.gz"); !errors.Is(oerr, ErrNotFound) {
				t.Fatalf("open before put: %v", oerr)
			}
			n, err := s.Put(ctx, "sources/org/abc.tar.gz", strings.NewReader("tarball"), "application/gzip")
			if err != nil || n != 7 {
				t.Fatalf("put: %d %v", n, err)
			}
			r, size, err := s.Open(ctx, "sources/org/abc.tar.gz")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(r)
			_ = r.Close()
			if string(body) != "tarball" || size != 7 {
				t.Fatalf("read %q (%d)", body, size)
			}
			if ok, err := s.Exists(ctx, "sources/org/abc.tar.gz"); err != nil || !ok {
				t.Fatalf("exists after put: %v %v", ok, err)
			}
			// Opening the same storage again keeps what's there.
			if _, err := Open(ctx, dsn); err != nil {
				t.Fatalf("reopen: %v", err)
			}
		})
	}
}

func TestOpen_refusesUnknownSchemes(t *testing.T) {
	if _, err := Open(t.Context(), "ftp://example.com/x"); err == nil {
		t.Fatal("opened ftp://")
	}
}
