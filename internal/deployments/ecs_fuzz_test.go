package deployments

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"path/filepath"
	"testing"
)

// FuzzTarToZip feeds the repacker tarballs with any entry names, and checks
// it never writes a zip entry that would land outside the directory it's
// unpacked into.
func FuzzTarToZip(f *testing.F) {
	for _, name := range []string{"acme-mono-1/main.go", "acme-mono-1/../../etc/passwd", "/abs/path", "acme/./x", "acme/a/../../b", "x"} {
		f.Add(name, []byte("body"))
	}
	f.Fuzz(func(t *testing.T, name string, body []byte) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			return
		}
		_, _ = tw.Write(body)
		_ = tw.Close()
		_ = zw.Close()
		job := &Job{Source: func(context.Context) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(buf.Bytes())), int64(buf.Len()), nil
		}}
		var out bytes.Buffer
		if err := tarToZip(context.Background(), job, &out); err != nil {
			return
		}
		zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
		if err != nil {
			t.Fatalf("the zip doesn't read back: %v", err)
		}
		for _, f := range zr.File {
			if !filepath.IsLocal(f.Name) {
				t.Fatalf("entry %q escapes the directory", f.Name)
			}
		}
	})
}
