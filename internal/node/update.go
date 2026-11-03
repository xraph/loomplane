package node

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xraph/forge/cli"
)

const (
	// releaseBase is where Loomplane's releases are published.
	releaseBase = "https://github.com/xraph/loomplane/releases/download"
	// serviceBinary is the file in the service's bin dir that it runs:
	// systemBinDir on Linux, <state>/bin on macOS.
	serviceBinary = "loomctl"
	// maxArchive bounds the release archive, and maxSmall the checksums and
	// signature, so a bad server cannot fill the disk or memory.
	maxArchive = 256 << 20
	maxSmall   = 1 << 20
	// downloadTimeout bounds one release download.
	downloadTimeout = 10 * time.Minute
)

// releaseVersion is a release tag, with or without its v.
var releaseVersion = regexp.MustCompile(`^v?\d[0-9A-Za-z.+_-]*$`)

// releaseTag is version as a tag. The flag is --release because Forge's CLI
// takes --version anywhere on the line for its own.
func releaseTag(version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", errors.New("say which release with --release, such as --release v1.9.0")
	}
	if !releaseVersion.MatchString(version) {
		return "", fmt.Errorf("--release %q is not a release version such as v1.9.0", version)
	}
	return "v" + strings.TrimPrefix(version, "v"), nil
}

// get downloads url into memory, at most limit bytes.
func (c *cmds) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	hc := c.opts.httpc
	if hc == nil {
		hc = &http.Client{Timeout: downloadTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("downloading %s: more than %d bytes", url, limit)
	}
	return body, nil
}

// verify checks a release with the seam, or VerifyRelease.
func (c *cmds) verify(ctx context.Context, version string, checksums, bundle []byte, artifact, sum string) error {
	if c.opts.verifyRelease != nil {
		return c.opts.verifyRelease(ctx, version, checksums, bundle, artifact, sum)
	}
	return VerifyRelease(ctx, version, checksums, bundle, artifact, sum)
}

// update replaces the binary the service runs with a verified release.
func (c *cmds) update(ctx cli.CommandContext) error {
	tag, err := releaseTag(ctx.String("release"))
	if err != nil {
		return err
	}
	if rerr := c.refuseMacRoot("node update"); rerr != nil {
		return rerr
	}
	if rerr := c.needRoot("node update"); rerr != nil {
		return rerr
	}
	dir, err := c.stateDir(ctx)
	if err != nil {
		return err
	}
	target := filepath.Join(c.binDir(dir), serviceBinary)
	old, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("this node does not run a copy of %s from %s: %w", serviceBinary, filepath.Dir(target), err)
	}
	if !old.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file, so node update leaves it alone", target)
	}

	goarch := c.opts.goarch
	artifact := fmt.Sprintf("loomctl_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), c.opts.goos, goarch)
	base := strings.TrimRight(c.opts.releaseBase, "/") + "/" + tag + "/"
	c.printf("Downloading %s\n", artifact)
	archive, err := c.get(ctx.Context(), base+artifact, maxArchive)
	if err != nil {
		return err
	}
	checksums, err := c.get(ctx.Context(), base+"binaries-checksums.txt", maxSmall)
	if err != nil {
		return err
	}
	bundle, err := c.get(ctx.Context(), base+"binaries-checksums.txt.sigstore.json", maxSmall)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(archive)
	if err = c.verify(ctx.Context(), tag, checksums, bundle, artifact, hex.EncodeToString(digest[:])); err != nil {
		return fmt.Errorf("not installing %s: %w", tag, err)
	}
	// Ask before changing anything, so a service that cannot be asked about
	// is reported while the old binary is still in place.
	running, rerr := c.opts.svc.Running(ctx.Context())
	if rerr != nil {
		return fmt.Errorf("not changing anything: cannot tell whether the node service runs: %w", rerr)
	}
	// The current binary stays beside the new one, as a hard link, so a
	// service that fails to start on the new one goes back to it.
	prev := target + ".old"
	if lerr := os.Remove(prev); lerr != nil && !errors.Is(lerr, fs.ErrNotExist) {
		return lerr
	}
	if lerr := os.Link(target, prev); lerr != nil {
		return fmt.Errorf("keeping the current binary as %s: %w", prev, lerr)
	}
	if werr := c.replaceBinary(archive, target, old); werr != nil {
		_ = os.Remove(prev)
		return werr
	}
	c.printf("Replaced %s with loomctl %s (the previous one is %s)\n", target, tag, prev)

	// A service that was not running stays stopped.
	if !running {
		c.printf("The node is not running, so the new version starts the next time it does.\n")
		return nil
	}
	if err = c.opts.svc.Stop(ctx.Context()); err != nil {
		return c.rollBack(ctx.Context(), target, prev, false, fmt.Errorf("stopping the service failed: %w", err))
	}
	if err = c.opts.svc.Start(ctx.Context()); err != nil {
		return c.rollBack(ctx.Context(), target, prev, true, fmt.Errorf("starting the service on %s failed: %w", tag, err))
	}
	c.printf("The node service restarted on %s.\n", tag)
	return nil
}

// rollBack puts the previous binary back and, when the service was stopped,
// starts it on that.
func (c *cmds) rollBack(ctx context.Context, target, prev string, stopped bool, cause error) error {
	if err := os.Rename(prev, target); err != nil {
		return fmt.Errorf("%w; putting %s back also failed: %w", cause, prev, err)
	}
	_ = syncDir(filepath.Dir(target)) //nolint:errcheck // the rename happened; a failed sync only weakens durability
	if stopped {
		if err := c.opts.svc.Start(ctx); err != nil {
			return fmt.Errorf("%w; the previous binary is back but the service would not start on it either: %w", cause, err)
		}
	}
	return fmt.Errorf("%w; the previous binary is back in place", cause)
}

// replaceBinary writes loomctl from the archive next to target and renames it
// over target, so a running copy is never cut short. The new file belongs to
// whoever owned the old one.
func (c *cmds) replaceBinary(archive []byte, target string, old os.FileInfo) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("reading the release archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			return errors.New("the release archive does not hold loomctl")
		}
		if nerr != nil {
			return fmt.Errorf("reading the release archive: %w", nerr)
		}
		if h.Typeflag != tar.TypeReg || path.Clean(strings.TrimPrefix(h.Name, "./")) != serviceBinary {
			continue
		}
		return c.writeBinary(tr, target, old)
	}
}

func (c *cmds) writeBinary(src io.Reader, target string, old os.FileInfo) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(src, maxArchive+1))
	if err != nil {
		return fail(fmt.Errorf("unpacking loomctl: %w", err))
	}
	if n > maxArchive {
		return fail(errors.New("loomctl in the release archive is too large"))
	}
	if err = tmp.Chmod(0o755); err != nil {
		return fail(err)
	}
	if err = tmp.Sync(); err != nil {
		return fail(err)
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if owner, ok := fileOwner(old); ok {
		if err = c.opts.chown(tmp.Name(), int(owner), -1); err != nil {
			_ = os.Remove(tmp.Name())
			return err
		}
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err = syncDir(filepath.Dir(target)); err != nil {
		return fmt.Errorf("syncing %s after the rename: %w", filepath.Dir(target), err)
	}
	return nil
}

// verifyRelease checks a downloaded release by hand. node.sh runs it when
// cosign is not installed.
func (c *cmds) verifyRelease(ctx cli.CommandContext) error {
	tag, err := releaseTag(ctx.String("release"))
	if err != nil {
		return err
	}
	archive := ctx.String("archive")
	if archive == "" || ctx.String("checksums") == "" || ctx.String("bundle") == "" {
		return errors.New("give --archive, --checksums and --bundle")
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return fmt.Errorf("reading %s: %w", archive, err)
	}
	checksums, err := os.ReadFile(ctx.String("checksums"))
	if err != nil {
		return err
	}
	bundle, err := os.ReadFile(ctx.String("bundle"))
	if err != nil {
		return err
	}
	name := filepath.Base(archive)
	if verr := c.verify(ctx.Context(), tag, checksums, bundle, name, hex.EncodeToString(h.Sum(nil))); verr != nil {
		return verr
	}
	c.printf("%s is the %s release, signed by Loomplane's release workflow.\n", name, tag)
	return nil
}
