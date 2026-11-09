package digitalocean

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestDOKSBootstrapCertificateExpiry(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(3 * time.Minute).UTC().Truncate(time.Second)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"server": "https://cluster.example", "certificate_authority_data": base64.StdEncoding.EncodeToString(cert), "client_certificate_data": base64.StdEncoding.EncodeToString(cert), "client_key_data": base64.StdEncoding.EncodeToString(keyPEM)})
	})
	access, err := c.BootstrapCluster(t.Context(), doksAccess(), provider.ClusterRef{ID: "cluster", AccountID: "account"}, 10*time.Minute)
	if err != nil || !access.ExpiresAt.Equal(expiry) {
		t.Fatalf("certificate expiry: %s %v", access.ExpiresAt, err)
	}
}

func doksTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
func TestDOKSBootstrapRejectsMalformedResponse(t *testing.T) {
	ca := doksTestCA(t)
	for _, tc := range []struct{ name, field, value string }{
		{"query", "server", "https://cluster.example?secret=token"},
		{"fragment", "server", "https://cluster.example#token"},
		{"empty-hostname", "server", "https://:443"},
		{"http", "server", "http://cluster.example"},
		{"userinfo", "server", "https://user:secret@cluster.example"},
		{"invalid-base64", "certificate_authority_data", "invalid"},
		{"arbitrary-bytes", "certificate_authority_data", "Y2E="},
		{"invalid-pem", "certificate_authority_data", base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid")}))},
		{"space-token", "token", "private token"},
		{"tab-token", "token", "private\ttoken"},
		{"newline-token", "token", "private\ntoken"},
		{"whitespace-token", "token", "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				body := map[string]any{"server": "https://cluster.example", "certificate_authority_data": ca, "token": "private-token", "expires_at": time.Now().Add(time.Minute)}
				body[tc.field] = tc.value
				_ = json.NewEncoder(w).Encode(body)
			})
			access, err := c.BootstrapCluster(t.Context(), doksAccess(), provider.ClusterRef{ID: "cluster", AccountID: "account"}, 10*time.Minute)
			if err == nil || access.Token != "" || len(access.CAData) != 0 {
				t.Fatal("malformed bootstrap response accepted")
			}
		})
	}
}
