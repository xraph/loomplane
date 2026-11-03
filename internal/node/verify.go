package node

import (
	"context"

	"github.com/xraph/loomplane/internal/node/release"
)

// What a release must be signed by. They live in the release package, which
// node.sh is rendered with, so the script and the check cannot drift apart.
const (
	ReleaseIdentity = release.ReleaseIdentity
	ReleaseIssuer   = release.ReleaseIssuer
)

// VerifyRelease checks a release before anything from it runs; see
// release.VerifyRelease.
func VerifyRelease(ctx context.Context, version string, checksums, bundle []byte, artifact, sum string) error {
	return release.VerifyRelease(ctx, version, checksums, bundle, artifact, sum)
}
