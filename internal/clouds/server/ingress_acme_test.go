package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"testing"
	"time"
)

// renderedIssuers is every policy's first issuer, keyed by its first subject.
func renderedIssuers(t *testing.T, v IngressConfig) map[string]map[string]any {
	t.Helper()
	raw, err := v.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Apps struct {
			TLS struct {
				Automation struct {
					Policies []struct {
						Subjects []string         `json:"subjects"`
						Issuers  []map[string]any `json:"issuers"`
					} `json:"policies"`
				} `json:"automation"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, p := range doc.Apps.TLS.Automation.Policies {
		out[p.Subjects[0]] = p.Issuers[0]
	}
	return out
}

func testRootPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ACME root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestServerRoutesUseTheConfiguredACMEDirectory(t *testing.T) {
	t.Cleanup(func() { _ = SetACMEIssuer(ACMEIssuer{}) })
	config := IngressConfig{OrgID: "org", ServerID: "server", Version: "v1", Routes: []IngressRoute{{ID: "r", Hostname: "shop.example.com", Backend: testBackend()}}}

	// Unset, a route orders from Let's Encrypt: no ca, no roots.
	if issuer := renderedIssuers(t, config)["shop.example.com"]; issuer["module"] != "acme" || issuer["ca"] != nil || issuer["trusted_roots_pem_files"] != nil {
		t.Fatalf("default issuer %v", issuer)
	}

	if err := SetACMEIssuer(ACMEIssuer{Directory: "https://acme-staging-v02.api.letsencrypt.org/directory"}); err != nil {
		t.Fatal(err)
	}
	if issuer := renderedIssuers(t, config)["shop.example.com"]; issuer["ca"] != "https://acme-staging-v02.api.letsencrypt.org/directory" || issuer["trusted_roots_pem_files"] != nil {
		t.Fatalf("directory without roots: %v", issuer)
	}

	root := testRootPEM(t)
	if err := SetACMEIssuer(ACMEIssuer{Directory: "https://ca.test:14000/dir", Roots: root}); err != nil {
		t.Fatal(err)
	}
	issuer := renderedIssuers(t, config)["shop.example.com"]
	files, _ := issuer["trusted_roots_pem_files"].([]any)
	if issuer["ca"] != "https://ca.test:14000/dir" || len(files) != 1 || files[0] != "/config/"+ingressACMERoots {
		t.Fatalf("directory with roots: %v", issuer)
	}
	// The setting is copied: changing the caller's slice changes nothing.
	root[0] = 'X'
	if a := configuredACMEIssuer(); a == nil || a.Roots[0] != '-' {
		t.Fatal("the issuer shares the caller's roots")
	}
	// A fixture caller's own directory still wins.
	fixture := config
	fixture.ACMEDir, fixture.ACMERoots = "https://pebble.test:14000/dir", []string{"/config/fixture-root.pem"}
	if issuer = renderedIssuers(t, fixture)["shop.example.com"]; issuer["ca"] != "https://pebble.test:14000/dir" {
		t.Fatalf("fixture issuer %v", issuer)
	}

	if err := SetACMEIssuer(ACMEIssuer{}); err != nil {
		t.Fatal(err)
	}
	if issuer = renderedIssuers(t, config)["shop.example.com"]; issuer["ca"] != nil {
		t.Fatalf("after a reset: %v", issuer)
	}
}

func TestACMEIssuerRefusesWhatCaddyCannotUse(t *testing.T) {
	t.Cleanup(func() { _ = SetACMEIssuer(ACMEIssuer{}) })
	root := testRootPEM(t)
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})
	for _, tc := range []struct {
		name string
		v    ACMEIssuer
	}{
		{"http", ACMEIssuer{Directory: "http://ca.test/dir"}},
		{"no host", ACMEIssuer{Directory: "https:///dir"}},
		{"credentials in the URL", ACMEIssuer{Directory: "https://user:pass@ca.test/dir"}},
		{"roots without a directory", ACMEIssuer{Roots: root}},
		{"roots that are not PEM", ACMEIssuer{Directory: "https://ca.test/dir", Roots: []byte("not pem")}},
		{"a key among the roots", ACMEIssuer{Directory: "https://ca.test/dir", Roots: slices.Concat(root, key)}},
		{"a certificate that doesn't parse", ACMEIssuer{Directory: "https://ca.test/dir", Roots: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := SetACMEIssuer(tc.v); !errors.Is(err, ErrInvalid) {
				t.Fatalf("SetACMEIssuer = %v, want ErrInvalid", err)
			}
			if configuredACMEIssuer() != nil {
				t.Fatal("a refused issuer was kept")
			}
		})
	}
}
