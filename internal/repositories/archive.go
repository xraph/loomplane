package repositories

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xraph/loomplane/pkg/discovery"
)

// Limits on one checkout.
const (
	DefaultMaxArchiveBytes int64 = 1 << 30 // compressed tarball
	DefaultMaxTotalBytes   int64 = 2 << 30 // extracted files kept
	DefaultMaxFileBytes    int64 = 4 << 20 // one file; bigger ones are skipped
)

// Limits are the caps on one checkout. Every host reads its tarball through
// the same ones.
type Limits struct {
	Archive int64 // compressed tarball
	Total   int64 // extracted files kept
	File    int64 // one file; bigger ones are skipped
}

// Cap reads r, failing with ErrTooLarge once it passes the archive limit.
func (l Limits) Cap(r io.Reader) io.Reader { return &limitedReader{r: r, left: l.Archive} }

// Stats describe a checkout.
type Stats struct {
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
	Skipped int   `json:"skipped"`
}

// limitedReader fails, instead of returning EOF, past its limit, so an
// oversized archive can't pass for a short one.
type limitedReader struct {
	r    io.Reader
	left int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

func extractArchive(ctx context.Context, tr *tar.Reader, dest string, lim Limits) (Stats, error) {
	var st Stats
	root, err := filepath.Abs(dest)
	if err != nil {
		return st, fmt.Errorf("checkout directory: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		if err != nil {
			if errors.Is(err, ErrTooLarge) {
				return st, err
			}
			return st, fmt.Errorf("read tarball: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // directories are made as needed; links are never followed
		}
		name, ok := entryPath(hdr.Name)
		if !ok {
			st.Skipped++
			continue
		}
		if hdr.Size > lim.File {
			st.Skipped++
			continue
		}
		if st.Bytes+hdr.Size > lim.Total {
			return st, ErrTooLarge
		}
		if st.Files >= maxFiles {
			return st, ErrTooLarge
		}
		if err := writeFile(root, name, tr, hdr.Size); err != nil {
			return st, err
		}
		st.Files++
		st.Bytes += hdr.Size
	}
}

// maxFiles caps the files one checkout writes.
const maxFiles = 200_000

// entryPath turns a tarball entry name into a clean path under the
// checkout, dropping GitHub's top-level "owner-repo-sha/" directory. It
// refuses absolute paths, "..", and anything under a directory a detector
// never reads.
func entryPath(name string) (string, bool) {
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", false
	}
	_, rest, ok := strings.Cut(name, "/")
	if !ok || rest == "" {
		return "", false
	}
	clean := path.Clean(rest)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	segs := strings.Split(clean, "/")
	for _, seg := range segs[:len(segs)-1] {
		if seg == ".." || discovery.SkipDir(seg) {
			return "", false
		}
	}
	return clean, true
}

func writeFile(root, name string, r io.Reader, size int64) error {
	target := filepath.Join(root, filepath.FromSlash(name))
	if !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return fmt.Errorf("tarball entry %q escapes the checkout", name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", name, err)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	_, err = io.CopyN(f, r, size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return err
		}
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}
