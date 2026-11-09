package enginetls

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"
)

func TestBundleReusesTrustAndIssuesDeterministicInstanceCertificates(t *testing.T) {
	identity := Identity{Org: "org", Environment: "env", Resource: "search", Kind: "opensearch"}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	raw, err := Generate(identity, "password", now)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(raw, identity, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := bundle.Certificate("lp-instance-one", now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bundle.Certificate("lp-instance-one", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("same instance changed certificate bytes")
	}
	replacement, err := bundle.Certificate("lp-instance-two", now)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, replacement) {
		t.Fatal("replacement reused wrong hostname certificate")
	}
	block, _ := pem.Decode(replacement)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(cert.RawSubject, cert.RawIssuer) {
		t.Fatal("leaf is self-issued; OpenSSL cannot select its distinct CA")
	}
	if len(cert.AuthorityKeyId) == 0 {
		t.Fatal("leaf lacks issuer key identity")
	}
	if err = cert.VerifyHostname("lp-instance-two"); err != nil {
		t.Fatal(err)
	}
	if err = cert.VerifyHostname("lp-instance-one"); err == nil {
		t.Fatal("replacement trusts old hostname")
	}
	if err = bundle.CheckPassword("password"); err != nil {
		t.Fatal(err)
	}
	if err = bundle.CheckPassword("wrong"); err == nil {
		t.Fatal("accepted changed password")
	}
	if bytes.Contains(bundle.CACert, []byte("PRIVATE KEY")) {
		t.Fatal("CA output contains private key")
	}
	for _, edit := range []func(*Bundle){func(b *Bundle) { b.Identity.Resource = "other" }, func(b *Bundle) { b.CACert = []byte("invalid") }, func(b *Bundle) { b.CAKey = b.NodeKey }, func(b *Bundle) { b.NodeKey = []byte("invalid") }, func(b *Bundle) { b.NotAfter = now.Add(-time.Hour) }} {
		copyBundle := *bundle
		edit(&copyBundle)
		bad, _ := json.Marshal(copyBundle)
		if _, err = Parse(bad, identity, now); err == nil {
			t.Fatal("accepted corrupt bundle")
		}
	}
	if _, err = Parse(raw, identity, now.Add(366*24*time.Hour)); err == nil {
		t.Fatal("silently accepted expired bundle")
	}
	if _, err = Parse(append(raw, []byte("{}")...), identity, now); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}

func TestBundleRejectsPreviouslyInvalidSelfIssuedLeafTrust(t *testing.T) {
	identity := Identity{Org: "org", Environment: "env", Resource: "search", Kind: "opensearch"}
	now := time.Now()
	raw, err := Generate(identity, "", now)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(raw, identity, now)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(bundle.CACert)
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ca.Subject.CommonName = bundle.NodeName()
	ca.RawSubject = nil
	key, err := privateKey(bundle.CAKey)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	bundle.CACert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	invalid, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(invalid, identity, now); err == nil {
		t.Fatal("accepted old invalid issuer instead of requiring recorded-material recovery")
	}
}

func TestBundleRejectsIssuerKeyAsNodeKeyByRSAIdentity(t *testing.T) {
	identity := Identity{Org: "org", Environment: "env", Resource: "search", Kind: "opensearch"}
	now := time.Now()
	raw, err := Generate(identity, "", now)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(raw, identity, now)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(bundle.CAKey)
	reencoded := pem.EncodeToMemory(&pem.Block{Type: block.Type, Headers: map[string]string{"Comment": "same RSA key with different PEM bytes"}, Bytes: block.Bytes})
	if bytes.Equal(reencoded, bundle.CAKey) {
		t.Fatal("fixture did not change the key encoding")
	}
	for name, key := range map[string][]byte{"identical PEM": bundle.CAKey, "different PEM same RSA key": reencoded} {
		t.Run(name, func(t *testing.T) {
			corrupt := *bundle
			corrupt.NodeKey = key
			encoded, encodeErr := json.Marshal(corrupt)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if parsed, parseErr := Parse(encoded, identity, now); parseErr == nil || parsed != nil {
				t.Fatal("accepted CA signing key as a mountable node key")
			}
		})
	}
}
