package statichost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzUnpack feeds the host tarballs with any entry names, and checks
// nothing is ever written outside the site's directory.
func FuzzUnpack(f *testing.F) {
	for _, name := range []string{"repo-1/site/index.html", "repo-1/site/../../escape", "repo-1/site/dist/a.js", "/etc/passwd", "repo-1/site//x", "repo-1/site/../site2/y"} {
		f.Add(name, "site")
	}
	f.Fuzz(func(t *testing.T, name, dir string) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}); err != nil {
			return
		}
		_, _ = tw.Write([]byte("hi"))
		_ = tw.Close()
		_ = zw.Close()

		root := t.TempDir()
		dest := filepath.Join(root, "site")
		_, _ = unpack(&buf, dir, dest)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && !strings.HasPrefix(p, dest+string(filepath.Separator)) {
				t.Fatalf("%q from entry %q landed outside the site, at %s", name, dir, p)
			}
			return nil
		})
	})
}
