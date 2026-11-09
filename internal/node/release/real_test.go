package release

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// A real keyless bundle from Sigstore's public-good instance, for the
// rekor-server-linux-amd64 binary of sigstore/rekor v1.5.5, checked against a
// pinned copy of the public-good trusted root. It was signed by a Google
// service account, not GitHub Actions, so it cannot stand in for a Loomplane
// release; what it proves is that the production options (certificate
// timestamp, transparency log, observer timestamp) accept a real bundle and
// that the extension pin is enforced on a real certificate.
const (
	realDigest = "ddb4bcd95871771aea94c2e7c59ba24ccecc3cce340e071f9a11aba5e38eba56"
	realSAN    = "keyless@projectsigstore.iam.gserviceaccount.com"
	realIssuer = "https://accounts.google.com"
)

func realFixtures(t *testing.T) (root.TrustedMaterial, []byte, verify.ArtifactPolicyOption) {
	t.Helper()
	tr, err := root.NewTrustedRootFromPath("testdata/trusted_root.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/rekor-server-linux-amd64.sigstore.json")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := hex.DecodeString(realDigest)
	return tr, b, verify.WithArtifactDigest("sha256", d)
}

func TestRealBundleVerifiesUnderTheProductionOptions(t *testing.T) {
	tr, b, art := realFixtures(t)
	p := Production()
	p.Extensions = certificate.Extensions{}
	id, err := p.identity(realSAN, realIssuer)
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyBundle(tr, p, []verify.CertificateIdentity{id}, art, b); err != nil {
		t.Fatalf("a real public-good bundle: %v", err)
	}
}

func TestRealBundleIsRefusedWhenWhatItSignedChanged(t *testing.T) {
	tr, b, _ := realFixtures(t)
	p := Production()
	p.Extensions = certificate.Extensions{}
	id, _ := p.identity(realSAN, realIssuer)
	other := make([]byte, 32)
	if err := verifyBundle(tr, p, []verify.CertificateIdentity{id}, verify.WithArtifactDigest("sha256", other), b); err == nil {
		t.Fatal("a bundle verified for a digest it did not sign")
	}
}

func TestRealBundleIsRefusedByTheProductionPins(t *testing.T) {
	tr, b, art := realFixtures(t)
	// The shipped pins: GitHub's issuer and Loomplane's workflow, repository
	// and runner. A real certificate from somewhere else must not pass.
	ids, err := Production().identities("v1.5.5")
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyBundle(tr, Production(), ids, art, b); err == nil {
		t.Fatal("a real bundle from another signer passed the production pins")
	}
	// The same SAN and issuer, but with the repository extension pinned.
	p := Production()
	id, _ := p.identity(realSAN, realIssuer)
	err = verifyBundle(tr, p, []verify.CertificateIdentity{id}, art, b)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "repository") && !strings.Contains(strings.ToLower(err.Error()), "extension") && !strings.Contains(strings.ToLower(err.Error()), "runner") {
		t.Fatalf("the extension pin was not enforced: %v", err)
	}
}
