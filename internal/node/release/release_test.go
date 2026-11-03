package release

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
	"github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/encoding/protojson"
)

// testPolicy is Production without what a virtual Sigstore cannot give: it
// has no certificate transparency log, and its certificates carry no GitHub
// extensions.
var testPolicy = Policy{Options: []verify.VerifierOption{verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1)}}

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
	for _, p := range []string{"linux_arm64", "darwin_amd64", "darwin_arm64"} {
		checksums = append(checksums, []byte(archiveSum(p)+"  "+Archive(testTag, p)+"\n")...)
	}
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
	if err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err != nil {
		t.Fatalf("a release signed by release.yml: %v", err)
	}
	// The tag may come without its v, as an archive name has it.
	if err := verifyRelease(t.Context(), rel.vs, testPolicy, "1.8.0", rel.checksums, rel.bundle, testArtifact, strings.ToUpper(rel.sum)); err != nil {
		t.Fatalf("version without v, upper-case sum: %v", err)
	}
}

func TestVerifyReleaseRejectsWrongIdentity(t *testing.T) {
	for name, identity := range map[string]string{
		"another workflow":      "https://github.com/xraph/loomplane/.github/workflows/ci.yml@refs/tags/" + testTag,
		"another repository":    "https://github.com/someone/loomplane/.github/workflows/release.yml@refs/tags/" + testTag,
		"another tag":           ReleaseIdentity + "v9.9.9",
		"another branch":        "https://github.com/xraph/loomplane/.github/workflows/release.yml@refs/heads/dev",
		"the tag as a prefix":   ReleaseIdentity,
		"a longer tag name":     ReleaseIdentity + testTag + "-evil",
		"the reusable workflow": "https://github.com/xraph/workflows/.github/workflows/go-binary-release.yml@refs/tags/" + testTag,
	} {
		rel := signRelease(t, newVirtual(t), identity, ReleaseIssuer)
		if err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err == nil || !strings.Contains(err.Error(), "not signed by") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestVerifyReleaseRejectsWrongIssuer(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, "https://accounts.google.com")
	if err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err == nil || !strings.Contains(err.Error(), "not signed by") {
		t.Fatalf("a certificate from another issuer: %v", err)
	}
}

func TestVerifyReleaseRejectsChecksumMismatch(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, rel.checksums, rel.bundle, testArtifact, archiveSum("a different archive"))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("an archive that differs from its signed checksum: %v", err)
	}
	if _, err = checksumFor(rel.checksums, "loomctl_1.8.0_freebsd_amd64.tar.gz"); err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("an archive the checksums do not list: %v", err)
	}
}

func TestVerifyReleaseRejectsChecksumsThatWereNotSigned(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	forged := []byte(archiveSum("evil") + "  " + testArtifact + "\n")
	if err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, forged, rel.bundle, testArtifact, archiveSum("evil")); err == nil || !strings.Contains(err.Error(), "not signed by") {
		t.Fatalf("checksums the bundle does not cover were accepted: %v", err)
	}
}

func TestVerifyReleaseRejectsAnUntrustedSigner(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	if err := verifyRelease(t.Context(), newVirtual(t), testPolicy, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err == nil || !strings.Contains(err.Error(), "not signed by") {
		t.Fatalf("a bundle from a Sigstore this node does not trust: %v", err)
	}
}

func TestVerifyReleaseRejectsGarbage(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	if err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, rel.checksums, []byte("not json"), testArtifact, rel.sum); err == nil {
		t.Fatal("a bundle that is not a bundle was accepted")
	}
	if err := verifyRelease(t.Context(), rel.vs, testPolicy, "", rel.checksums, rel.bundle, testArtifact, rel.sum); err == nil {
		t.Fatal("no version was accepted")
	}
}

func TestVerifyReleaseAcceptsAReleaseCutFromMain(t *testing.T) {
	rel := signRelease(t, newVirtual(t), MainIdentity, ReleaseIssuer)
	if err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum); err != nil {
		t.Fatalf("a release cut with workflow_dispatch: %v", err)
	}
}

// Checksums signed from main for an old release must not vouch for an
// archive that merely carries another release's name.
func TestVerifyReleaseRejectsAnArchiveOfAnotherRelease(t *testing.T) {
	rel := signRelease(t, newVirtual(t), MainIdentity, ReleaseIssuer)
	for _, name := range []string{
		"loomctl_1.7.0_linux_amd64.tar.gz", "loomplane_1.8.0_linux_amd64.tar.gz", "loomctl_1.8.0_windows_amd64.zip",
		"loomctl_1.8.0_linux_amd64.tar.gz.sig", "../loomctl_1.8.0_linux_amd64.tar.gz", "loomctl_1x8x0_linux_amd64.tar.gz",
	} {
		err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, rel.checksums, rel.bundle, name, rel.sum)
		if err == nil || !strings.Contains(err.Error(), "is not a loomctl archive of release") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The same checksums, listing the old name, still do not pass for this release.
	old := []byte(archiveSum("old") + "  loomctl_1.7.0_linux_amd64.tar.gz\n")
	bundle := signBlob(t, rel.vs, MainIdentity, ReleaseIssuer, old)
	if err := verifyRelease(t.Context(), rel.vs, testPolicy, testTag, old, bundle, "loomctl_1.7.0_linux_amd64.tar.gz", archiveSum("old")); err == nil {
		t.Fatal("an old release's archive passed as this release")
	}
}

// Production demands a signed certificate timestamp. A virtual Sigstore has
// none, so everything it signs must fail under the shipped policy.
func TestProductionPolicyRequiresSCTs(t *testing.T) {
	rel := signRelease(t, newVirtual(t), ReleaseIdentity+testTag, ReleaseIssuer)
	p := Production()
	p.Extensions = testPolicy.Extensions // only the SCT requirement may fail this
	err := verifyRelease(t.Context(), rel.vs, p, testTag, rel.checksums, rel.bundle, testArtifact, rel.sum)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "sct") && !strings.Contains(strings.ToLower(err.Error()), "certificate timestamp") {
		t.Fatalf("a certificate with no SCT passed the production options: %v", err)
	}
}

func TestProductionPinsTheRepositoryRunnerAndBothIdentities(t *testing.T) {
	p := Production()
	if p.Extensions.SourceRepositoryURI != "https://github.com/xraph/loomplane" || p.Extensions.RunnerEnvironment != "github-hosted" {
		t.Fatalf("extensions %+v", p.Extensions)
	}
	ids, err := p.identities("v1.8.0")
	if err != nil || len(ids) != 2 {
		t.Fatalf("identities %v, %v", ids, err)
	}
	for i, want := range []string{ReleaseIdentity + "v1.8.0", MainIdentity} {
		if ids[i].SubjectAlternativeName.SubjectAlternativeName != want || ids[i].SubjectAlternativeName.Regexp.String() != "" ||
			ids[i].Issuer.Issuer != ReleaseIssuer || ids[i].Issuer.Regexp.String() != "" ||
			ids[i].SourceRepositoryURI != SourceRepository || ids[i].RunnerEnvironment != Runner {
			t.Errorf("identity %d: %+v", i, ids[i])
		}
	}
}

func TestSumsReturnsTheSignedSumOfEveryPlatform(t *testing.T) {
	rel := signRelease(t, newVirtual(t), MainIdentity, ReleaseIssuer)
	got, err := sums(t.Context(), rel.vs, testPolicy, testTag, rel.checksums, rel.bundle)
	if err != nil || len(got) != 4 || got["linux_amd64"] != rel.sum || got["darwin_arm64"] != archiveSum("darwin_arm64") {
		t.Fatalf("sums %v, %v", got, err)
	}
	forged := append([]byte{}, rel.checksums...)
	forged = append(forged, '\n')
	if _, err = sums(t.Context(), rel.vs, testPolicy, testTag, forged, rel.bundle); err == nil {
		t.Fatal("checksums the bundle does not cover gave sums")
	}
	rel2 := signRelease(t, newVirtual(t), "https://github.com/evil/x/.github/workflows/release.yml@refs/heads/main", ReleaseIssuer)
	if _, err = sums(t.Context(), rel2.vs, testPolicy, testTag, rel2.checksums, rel2.bundle); err == nil {
		t.Fatal("another repository's signature gave sums")
	}
}
