package guest

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

func extract(dir string, input io.Reader) error {
	archive := tar.NewReader(io.LimitReader(input, fc.MaxSourceBytes+1))
	var total int64
	count := 0
	for {
		h, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if name == "." && h.Typeflag == tar.TypeDir {
			continue
		}
		if name == "." || name == ".." || strings.HasPrefix(name, "../") || path.IsAbs(name) || strings.ContainsAny(name, "\\\x00") || len(name) > 4096 {
			return fc.ErrInvalid
		}
		count++
		total += h.Size
		if count > 100000 || h.Size < 0 || total > fc.MaxSourceBytes {
			return fc.ErrInvalid
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil { //nolint:gosec // Approved source is readable by the rootless builder inside this VM.
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // Approved source is readable by the rootless builder inside this VM.
				return err
			}
			f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode&0o777))
			if e != nil {
				return e
			}
			_, e = io.CopyN(f, archive, h.Size)
			e = errors.Join(e, f.Close())
			if e != nil {
				return e
			}
		default:
			return fc.ErrInvalid
		}
	}
}
