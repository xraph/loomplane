package nodes

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xraph/loomplane/internal/node/release"
)

const (
	// sumsEvery is how often the verified sums are checked again, and
	// sumsRetry how soon after a failure.
	sumsEvery = time.Hour
	sumsRetry = 5 * time.Minute
	// maxReleaseFile bounds a checksums file or bundle from GitHub.
	maxReleaseFile = 1 << 20
	sumsTimeout    = 2 * time.Minute
)

// releaseBase is where Loomplane's releases are published.
var releaseBase = "https://github.com/xraph/loomplane/releases/download"

// fetchSums downloads this version's checksums and signature bundle from the
// GitHub release, verifies the signature against Sigstore's public-good
// trust, and returns the signed sha256 of each loomctl archive.
func fetchSums(ctx context.Context, version string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, sumsTimeout)
	defer cancel()
	tag := release.Tag(version)
	checksums, err := download(ctx, releaseBase+"/"+tag+"/binaries-checksums.txt")
	if err != nil {
		return nil, err
	}
	bundle, err := download(ctx, releaseBase+"/"+tag+"/binaries-checksums.txt.sigstore.json")
	if err != nil {
		return nil, err
	}
	trust, err := release.FetchTrust(ctx, filepath.Join(os.TempDir(), "loomplane-sigstore-tuf"))
	if err != nil {
		return nil, err
	}
	return release.Sums(ctx, trust, tag, checksums, bundle)
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxReleaseFile))
}

// refreshSums verifies this release once more. A failure keeps the sums
// already verified (a release's checksums do not change), and a dev build or
// a machine with no route to GitHub simply never has any.
func (e *Extension) refreshSums(ctx context.Context) bool {
	if e.svc == nil {
		return false
	}
	fetch := e.fetchSums
	if fetch == nil {
		fetch = fetchSums
	}
	s, err := fetch(ctx, e.svc.Version)
	if err != nil {
		e.Logger().Debug("node.sh has no verified loomctl checksums yet: " + err.Error())
		return false
	}
	e.sums.Store(&s)
	return true
}

// keepSums verifies in the background, so serving node.sh never waits on
// GitHub, and again every hour.
func (e *Extension) keepSums(ctx context.Context) {
	for {
		wait := sumsRetry
		if e.refreshSums(ctx) {
			wait = sumsEvery
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// verifiedSums is what node.sh may carry: nil until a verification succeeds.
func (e *Extension) verifiedSums() map[string]string {
	if p := e.sums.Load(); p != nil {
		return *p
	}
	return nil
}
