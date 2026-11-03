// Package release checks that a Loomplane release was signed by the Release
// workflow of github.com/xraph/loomplane before anything from it runs.
package release

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	// ReleaseIdentity is the start of the identity of a release signed from
	// a tag push: the Release workflow, run on the tag. The tag follows it.
	ReleaseIdentity = "https://github.com/xraph/loomplane/.github/workflows/release.yml@refs/tags/"
	// MainIdentity is the identity of a release cut with workflow_dispatch,
	// which runs the Release workflow from main.
	MainIdentity = "https://github.com/xraph/loomplane/.github/workflows/release.yml@refs/heads/main"
	// ReleaseIssuer is the OIDC issuer of GitHub Actions, which vouches for
	// those identities.
	ReleaseIssuer = "https://token.actions.githubusercontent.com"
	// SourceRepository and Runner are the Fulcio extensions the certificate
	// must carry: built from this repository, on a GitHub-hosted runner.
	SourceRepository = "https://github.com/xraph/loomplane"
	Runner           = "github-hosted"
)

// Platforms are the os_arch pairs loomctl is released for on nodes.
var Platforms = []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"}

// Tag is version as a release tag, with the v.
func Tag(version string) string {
	return "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
}

// Archive is the name of the loomctl archive for a tag and platform.
func Archive(tag, platform string) string {
	return "loomctl_" + strings.TrimPrefix(tag, "v") + "_" + platform + ".tar.gz"
}

// Policy is how a signature is checked. Production is the one that ships;
// tests pass their own, since a virtual Sigstore has no certificate
// transparency log and its certificates carry no GitHub extensions.
type Policy struct {
	Options    []verify.VerifierOption
	Extensions certificate.Extensions
}

// Production requires the log entry, a trusted time, and a signed certificate
// timestamp, from the pinned repository on a GitHub-hosted runner.
func Production() Policy {
	return Policy{
		Options: []verify.VerifierOption{
			verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1),
		},
		Extensions: certificate.Extensions{SourceRepositoryURI: SourceRepository, RunnerEnvironment: Runner},
	}
}

// identity is an exact certificate identity under this policy's extensions.
func (p Policy) identity(san, issuer string) (verify.CertificateIdentity, error) {
	iss, err := verify.NewIssuerMatcher(issuer, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	m, err := verify.NewSANMatcher(san, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	return verify.NewCertificateIdentity(m, iss, p.Extensions)
}

// identities are the two the release workflow signs as: from the tag, or from
// main. Each is exact, with the issuer and extensions.
func (p Policy) identities(tag string) ([]verify.CertificateIdentity, error) {
	var ids []verify.CertificateIdentity
	for _, san := range []string{ReleaseIdentity + tag, MainIdentity} {
		id, err := p.identity(san, ReleaseIssuer)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// VerifyRelease checks a release before anything from it runs, with
// Sigstore's public-good trust. checksumsFile is binaries-checksums.txt and
// bundleJSON its cosign bundle. See VerifyReleaseWith.
func VerifyRelease(ctx context.Context, version string, checksumsFile, bundleJSON []byte, artifact, sum string) error {
	trust, err := FetchTrust(ctx, "")
	if err != nil {
		return err
	}
	return VerifyReleaseWith(ctx, trust, version, checksumsFile, bundleJSON, artifact, sum)
}

// VerifyReleaseWith is VerifyRelease against the given trusted material.
func VerifyReleaseWith(ctx context.Context, trust root.TrustedMaterial, version string, checksumsFile, bundleJSON []byte, artifact, sum string) error {
	return verifyRelease(ctx, trust, Production(), version, checksumsFile, bundleJSON, artifact, sum)
}

// FetchTrust is Sigstore's public-good trusted root, fetched through TUF. The
// TUF cache goes in cacheDir; empty means a directory made for this call and
// removed after it, so nothing lands in the home directory of whoever ran
// sudo.
func FetchTrust(ctx context.Context, cacheDir string) (root.TrustedMaterial, error) {
	if cacheDir == "" {
		d, err := os.MkdirTemp("", "loomplane-tuf-")
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.RemoveAll(d) }()
		cacheDir = d
	} else if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, err
	}
	type result struct {
		tr  *root.TrustedRoot
		err error
	}
	ch := make(chan result, 1)
	go func() {
		tr, err := root.FetchTrustedRootWithOptions(tuf.DefaultOptions().WithCachePath(cacheDir).WithContext(ctx))
		ch <- result{tr, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("fetching Sigstore's trusted root: %w", r.err)
		}
		return r.tr, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// artifactName is a loomctl archive of the requested release.
func artifactName(tag string) *regexp.Regexp {
	return regexp.MustCompile(`^loomctl_` + regexp.QuoteMeta(strings.TrimPrefix(tag, "v")) + `_(linux|darwin)_(amd64|arm64)\.tar\.gz$`)
}

// checkSigned verifies that bundleJSON signs checksumsFile as the Release workflow.
func checkSigned(trust root.TrustedMaterial, pol Policy, tag string, checksumsFile, bundleJSON []byte) error {
	ids, err := pol.identities(tag)
	if err != nil {
		return err
	}
	if err = verifyBundle(trust, pol, ids, verify.WithArtifact(bytes.NewReader(checksumsFile)), bundleJSON); err != nil {
		return fmt.Errorf("the checksums for %s are not signed by Loomplane's release workflow: %w", tag, err)
	}
	return nil
}

// verifyBundle checks bundleJSON over an artifact against any of ids.
func verifyBundle(trust root.TrustedMaterial, pol Policy, ids []verify.CertificateIdentity, artifact verify.ArtifactPolicyOption, bundleJSON []byte) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("reading the release's signature bundle: %w", err)
	}
	v, err := verify.NewVerifier(trust, pol.Options...)
	if err != nil {
		return fmt.Errorf("setting up the signature check: %w", err)
	}
	opts := make([]verify.PolicyOption, 0, len(ids))
	for _, id := range ids {
		opts = append(opts, verify.WithCertificateIdentity(id))
	}
	_, err = v.Verify(&b, verify.NewPolicy(artifact, opts...))
	return err
}

// verifyRelease checks the bundle, that artifact is a loomctl archive of this
// very release, and that sum is the signed checksum for it.
func verifyRelease(_ context.Context, trust root.TrustedMaterial, pol Policy, version string, checksumsFile, bundleJSON []byte, artifact, sum string) error {
	tag := Tag(version)
	if tag == "v" {
		return errors.New("verifying a release needs its version")
	}
	// Without this, the signed checksums of an old release (signed from main)
	// could vouch for an archive that only claims to be this one.
	if !artifactName(tag).MatchString(artifact) {
		return fmt.Errorf("%s is not a loomctl archive of release %s", artifact, tag)
	}
	if err := checkSigned(trust, pol, tag, checksumsFile, bundleJSON); err != nil {
		return err
	}
	want, err := checksumFor(checksumsFile, artifact)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want, strings.TrimSpace(sum)) {
		return fmt.Errorf("%s does not match the signed checksum: it is %s, the release says %s", artifact, strings.ToLower(strings.TrimSpace(sum)), want)
	}
	return nil
}

// Sums verifies the release's signature, then returns the signed sha256 of
// each loomctl archive, keyed by platform (linux_amd64 and so on). A release
// that lacks one of them is an error.
func Sums(ctx context.Context, trust root.TrustedMaterial, version string, checksumsFile, bundleJSON []byte) (map[string]string, error) {
	return sums(ctx, trust, Production(), version, checksumsFile, bundleJSON)
}

func sums(_ context.Context, trust root.TrustedMaterial, pol Policy, version string, checksumsFile, bundleJSON []byte) (map[string]string, error) {
	tag := Tag(version)
	if err := checkSigned(trust, pol, tag, checksumsFile, bundleJSON); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range Platforms {
		s, err := checksumFor(checksumsFile, Archive(tag, p))
		if err != nil {
			return nil, err
		}
		out[p] = strings.ToLower(s)
	}
	return out, nil
}

// checksumFor finds artifact's line in a checksums file: "<hex>  <name>".
func checksumFor(checksumsFile []byte, artifact string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(checksumsFile))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		if strings.TrimPrefix(f[1], "*") == artifact {
			return f[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s is not listed in the signed checksums", artifact)
}
