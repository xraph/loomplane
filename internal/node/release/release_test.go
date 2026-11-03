package node

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	testTag      = "v1.8.0"
	testArtifact = "loomctl_1.8.0_linux_amd64.tar.gz"
)

// release is a signed checksums file from a virtual Sigstore: no network,
// and a trust root that is the virtual one, not the public-good one.
type release struct {
	vs        *ca.VirtualSigstore
	checksums []byte
	bundle    []byte
	sum       string
}

func archiveSum(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// signRelease signs a checksums file that lists the test archive, as the
// given workflow identity and issuer.
func signRelease(t *testing.T, vs *ca.VirtualSigstore, identity, issuer string) release {
	t.Helper()
	sum := archiveSum("the loomctl archive")
	checksums := []byte("0000000000000000000000000000000000000000000000000000000000000000  loomplane_1.8.0_linux_amd64.tar.gz\n" +
		sum + "  " + testArtifact + "\n")
	return release{vs: vs, checksums: checksums, bundle: signBlob(t, vs, identity, issuer, checksums), sum: sum}
}

// signBlob is what cosign sign-blob --bundle writes: the certificate, the
// signature over blob, the log entry and the timestamp, as bundle JSON.
func signBlob(t *testing.T, vs *ca.VirtualSigstore, identity, issuer string, blob []byte) []byte {
	t.Helper()
	ent, err := vs.Sign(identity, issuer, blob)
	if err != nil {
		t.Fatal(err)
	}
	vc, err := ent.VerificationContent()
	if err != nil {
		t.Fatal(err)
	}
	sc, err := ent.SignatureContent()
	if err != nil {
		t.Fatal(err)
	}
	ms := sc.MessageSignatureContent()
	if ms == nil {
		t.Fatal("no message signature")
	}
	tlogs, err := ent.TlogEntries()
	if err != nil {
		t.Fatal(err)
	}
	stamps, err := ent.Timestamps()
	if err != nil {
		t.Fatal(err)
	}
	pb := &protobundle.Bundle{
		// The virtual log gives a signed entry timestamp and no inclusion
		// proof, which is what the first bundle version carries.
		MediaType: "application/vnd.dev.sigstore.bundle+json;version=0.1",
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_X509CertificateChain{X509CertificateChain: &protocommon.X509CertificateChain{
				Certificates: []*protocommon.X509Certificate{{RawBytes: vc.Certificate().Raw}}}},
			TimestampVerificationData: &protobundle.TimestampVerificationData{},
		},
		Content: &protobundle.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{Algorithm: protocommon.HashAlgorithm_SHA2_256, Digest: ms.Digest()},
			Signature:     ms.Signature(),
		}},
	}
	for _, e := range tlogs {
		pb.VerificationMaterial.TlogEntries = append(pb.VerificationMaterial.TlogEntries, wireEntry(t, vs, e))
	}
	for _, s := range stamps {
		pb.VerificationMaterial.TimestampVerificationData.Rfc3161Timestamps = append(pb.VerificationMaterial.TimestampVerificationData.Rfc3161Timestamps,
			&protocommon.RFC3161SignedTimestamp{SignedTimestamp: s})
	}
	out, err := protojson.Marshal(pb)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// wireEntry is the log entry as Rekor would hand it back: the virtual
// entry has no kind, and its signed entry timestamp is private, so both are
// made again.
func wireEntry(t *testing.T, vs *ca.VirtualSigstore, e *tlog.Entry) *protorekor.TransparencyLogEntry {
	t.Helper()
	tle := e.TransparencyLogEntry()
	logID, err := vs.RekorLogID()
	if err != nil {
		t.Fatal(err)
	}
	set, err := vs.RekorSignPayload(tlog.RekorPayload{
		Body: base64.StdEncoding.EncodeToString(tle.CanonicalizedBody), IntegratedTime: tle.IntegratedTime, LogIndex: tle.LogIndex, LogID: logID})
	if err != nil {
		t.Fatal(err)
	}
	tle.KindVersion = &protorekor.KindVersion{Kind: "hashedrekord", Version: "0.0.1"}
	tle.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: set}
	return tle
}

func newVirtual(t *testing.T) *ca.VirtualSigstore {
	t.Helper()
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

func TestVerifyReleaseAcceptsTheReleaseWorkflow(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	if err := verifyRelease(t.Context(), rel.vs, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err != nil {
		t.Fatalf("a release signed by release.yml: %v", err)
	}
	// The tag may come without its v, as an archive name has it.
	if err := verifyRelease(t.Context(), rel.vs, "1.8.0", rel.checksums, rel.bundle, testArtifact, strings.ToUpper(rel.sum)); err != nil {
		t.Fatalf("version without v, upper-case sum: %v", err)
	}
}

func TestVerifyReleaseRejectsWrongIdentity(t *testing.T) {
	for name, identity := range map[string]string{
		"another workflow":      "https://github.com/xraph/loomplane/.github/workflows/ci.yml@refs/tags/" + testTag,
		"another repository":    "https://github.com/someone/loomplane/.github/workflows/release.yml@refs/tags/" + testTag,
		"a branch, not a tag":   "https://github.com/xraph/loomplane/.github/workflows/release.yml@refs/heads/main",
		"another tag":           ReleaseIdentity + "v9.9.9",
		"a longer tag name":     ReleaseIdentity + testTag + "-evil",
		"the reusable workflow": "https://github.com/xraph/workflows/.github/workflows/go-binary-release.yml@refs/tags/" + testTag,
	} {
		rel := signRelease(t, newVirtual(t), identity, ReleaseIssuer)
		if err := verifyRelease(t.Context(), rel.vs, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err == nil || !strings.Contains(err.Error(), "not signed by") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestVerifyReleaseRejectsWrongIssuer(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, "https://accounts.google.com")
	if err := verifyRelease(t.Context(), rel.vs, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err == nil || !strings.Contains(err.Error(), "not signed by") {
		t.Fatalf("a certificate from another issuer: %v", err)
	}
}

func TestVerifyReleaseRejectsChecksumMismatch(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	err := verifyRelease(t.Context(), rel.vs, testTag, rel.checksums, rel.bundle, testArtifact, archiveSum("a different archive"))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("an archive that differs from its signed checksum: %v", err)
	}
	err = verifyRelease(t.Context(), rel.vs, testTag, rel.checksums, rel.bundle, "loomctl_1.8.0_linux_arm64.tar.gz", rel.sum)
	if err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("an archive the checksums do not list: %v", err)
	}
}

func TestVerifyReleaseRejectsChecksumsThatWereNotSigned(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	forged := []byte(archiveSum("evil") + "  " + testArtifact + "\n")
	if err := verifyRelease(t.Context(), rel.vs, testTag, forged, rel.bundle, testArtifact, archiveSum("evil")); err == nil || !strings.Contains(err.Error(), "not signed by") {
		t.Fatalf("checksums the bundle does not cover were accepted: %v", err)
	}
}

func TestVerifyReleaseRejectsAnUntrustedSigner(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	if err := verifyRelease(t.Context(), newVirtual(t), testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err == nil || !strings.Contains(err.Error(), "not signed by") {
		t.Fatalf("a bundle from a Sigstore this node does not trust: %v", err)
	}
}

func TestVerifyReleaseRejectsGarbage(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	if err := verifyRelease(t.Context(), rel.vs, testTag, rel.checksums, []byte("not json"), testArtifact, rel.sum); err == nil {
		t.Fatal("a bundle that is not a bundle was accepted")
	}
	if err := verifyRelease(t.Context(), rel.vs, "", rel.checksums, rel.bundle, testArtifact, rel.sum); err == nil {
		t.Fatal("no version was accepted")
	}
}
