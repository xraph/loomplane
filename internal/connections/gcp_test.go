package connections

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/gcp"
)

func gcpKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "shop", "client_email": "deployer@shop.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

// The billing export is optional: spend is read from it, and nothing a
// deployment needs depends on it.
func TestGCPCheckerKeepsAValidBillingExport(t *testing.T) {
	key := gcpKey(t)
	c, err := GCPChecker{}.Parse(key, map[string]string{gcp.BillingExportSetting: " shop-prod.billing.gcp_billing_export_v1_0A1B2C "})
	if err != nil {
		t.Fatal(err)
	}
	if c.Details[gcp.BillingExportSetting] != "shop-prod.billing.gcp_billing_export_v1_0A1B2C" || c.Details["project_id"] != "shop" {
		t.Fatalf("details %v", c.Details)
	}
	c, err = GCPChecker{}.Parse(key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Details[gcp.BillingExportSetting]; ok {
		t.Fatalf("an empty billing export was kept: %v", c.Details)
	}
	if _, err = (GCPChecker{}).Parse(key, map[string]string{gcp.BillingExportSetting: "shop-prod.billing`; DROP"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a malformed table was accepted: %v", err)
	}
}
