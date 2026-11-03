package node

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/xraph/loomplane/internal/node/install"
)

// What a release must be signed by. They live in the install package, which
// node.sh is rendered from, so the script and the check cannot drift apart.
const (
	ReleaseIdentity = install.ReleaseIdentity
	ReleaseIssuer   = install.ReleaseIssuer
)

// VerifyRelease checks a release before anything from it runs. bundle is the
// cosign bundle for checksums; it must be signed by the Release workflow for
// version, as logged in Sigstore's public-good transparency log. Then the
// line for artifact in checksums must hold sum, the archive's SHA-256.
func VerifyRelease(ctx context.Context, version string, checksums, bundleJSON []byte, artifact, sum string) error {
	trust, err := publicGoodTrust()
	if err != nil {
		return err
	}
	return verifyRelease(ctx, trust, version, checksums, bundleJSON, artifact, sum)
}

// publicGoodTrust is Sigstore's public-good trusted root, fetched through TUF
// and cached under the home directory.
func publicGoodTrust() (root.TrustedMaterial, error) {
	tr, err := root.FetchTrustedRoot()
	if err != nil {
		return nil, fmt.Errorf("fetching Sigstore's trusted root: %w", err)
	}
	return tr, nil
}

// verifyRelease is VerifyRelease against the given trusted material, which
// tests replace with a virtual Sigstore.
func verifyRelease(_ context.Context, trust root.TrustedMaterial, version string, checksums, bundleJSON []byte, artifact, sum string) error {
	tag := "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
	if tag == "v" {
		return errors.New("verifying a release needs its version")
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("reading the release's signature bundle: %w", err)
	}
	// The tag is part of the identity, so a bundle for another release, which
	// is validly signed, still fails here.
	id, err := verify.NewShortCertificateIdentity(ReleaseIssuer, "", ReleaseIdentity+tag, "")
	if err != nil {
		return err
	}
	v, err := verify.NewVerifier(trust, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return fmt.Errorf("setting up the signature check: %w", err)
	}
	policy := verify.NewPolicy(verify.WithArtifact(bytes.NewReader(checksums)), verify.WithCertificateIdentity(id))
	if _, verr := v.Verify(&b, policy); verr != nil {
		return fmt.Errorf("the checksums for %s are not signed by Loomplane's release workflow: %w", tag, verr)
	}
	want, err := checksumFor(checksums, artifact)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want, strings.TrimSpace(sum)) {
		return fmt.Errorf("%s does not match the signed checksum: it is %s, the release says %s", artifact, strings.ToLower(strings.TrimSpace(sum)), want)
	}
	return nil
}

// checksumFor finds artifact's line in a checksums file: "<hex>  <name>".
func checksumFor(checksums []byte, artifact string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(checksums))
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
