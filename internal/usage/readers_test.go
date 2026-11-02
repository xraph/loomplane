package usage

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
)

func readers(endpoint string) map[string]SpendReader {
	return Spenders(gcp.New(endpoint), aws.New(endpoint), azure.New(endpoint), digitalocean.New(endpoint))
}

func TestSpendersCoverTheCloudsWithABillingAPI(t *testing.T) {
	got := readers("http://unused")
	for _, cloud := range []string{connections.GCP, connections.AWS, connections.Azure, connections.DigitalOcean} {
		if got[cloud] == nil {
			t.Errorf("no spend reader for %s", cloud)
		}
	}
	if len(got) != 4 {
		t.Errorf("%d readers", len(got))
	}
}

func googleKey(t *testing.T, tokenURL string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cred, _ := json.Marshal(map[string]string{ //nolint:errchkjson // plain strings
		"type": "service_account", "project_id": "shop", "client_email": "deployer@shop.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs})), "token_uri": tokenURL,
	})
	return cred
}

// Google Cloud's spend is read from the billing export the connection
// names; without one, it says how to add it.
func TestGoogleSpendReadsTheConnectionsBillingExport(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path != "/bigquery/v2/projects/shop/queries" {
			t.Errorf("asked %s", r.URL)
		}
		var in struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		query = in.Query
		_, _ = w.Write([]byte(`{"jobComplete":true,"rows":[{"f":[{"v":"2026-10-02"},{"v":"USD"},{"v":"4.5"}]}]}`))
	}))
	defer srv.Close()
	reader := readers(srv.URL)[connections.GCP]
	cred := googleKey(t, srv.URL+"/token")
	c := &connections.Connection{Cloud: connections.GCP, Details: map[string]string{}}
	if _, err := reader.Spend(t.Context(), c, cred, now); err == nil {
		t.Fatal("read spend without a billing export")
	} else if _, ok := metrics.IsUnavailable(err); !ok {
		t.Fatalf("no billing export: %v", err)
	}
	c.Details[gcp.BillingExportSetting] = "shop-prod.billing.gcp_billing_export_v1_X"
	sp, err := reader.Spend(t.Context(), c, cred, now)
	if err != nil || sp.Total != 4.5 || sp.Currency != "USD" {
		t.Fatalf("spend %+v %v", sp, err)
	}
	if !strings.Contains(query, "`shop-prod.billing.gcp_billing_export_v1_X`") {
		t.Errorf("queried %s", query)
	}
}

// AWS reads Cost Explorer with the connection's key.
func TestAWSSpendReadsCostExplorer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("X-Amz-Target"), "GetCostAndUsage") || !strings.Contains(r.Header.Get("Authorization"), "AKIATESTTESTTESTTEST/") {
			t.Errorf("asked %s with %s", r.Header.Get("X-Amz-Target"), r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(`{"ResultsByTime":[{"TimePeriod":{"Start":"2026-10-01","End":"2026-10-02"},"Total":{"UnblendedCost":{"Amount":"3.25","Unit":"USD"}}}]}`))
	}))
	defer srv.Close()
	c := &connections.Connection{Cloud: connections.AWS, Account: "123456789012", Details: map[string]string{"region": "eu-west-2"}}
	cred := []byte(`{"access_key_id":"AKIATESTTESTTESTTEST","secret_access_key":"secret-test-secret-test"}`)
	sp, err := readers(srv.URL)[connections.AWS].Spend(t.Context(), c, cred, now)
	if err != nil || sp.Total != 3.25 || sp.Currency != "USD" {
		t.Fatalf("spend %+v %v", sp, err)
	}
}

// Azure reads Cost Management on the connection's resource group.
func TestAzureSpendReadsTheResourceGroup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			_, _ = w.Write([]byte(`{"access_token":"token","expires_in":3600}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/resourceGroups/loomplane/providers/Microsoft.CostManagement/query") {
			t.Errorf("asked %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"properties":{"columns":[{"name":"Cost","type":"Number"},{"name":"UsageDate","type":"Number"},{"name":"Currency","type":"String"}],"rows":[[2.5,20261003,"EUR"]]}}`))
	}))
	defer srv.Close()
	c := &connections.Connection{Cloud: connections.Azure, Details: azuretest.Settings()}
	sp, err := readers(srv.URL)[connections.Azure].Spend(t.Context(), c, []byte(azuretest.Secret), now)
	if err != nil || sp.Total != 2.5 || sp.Currency != "EUR" {
		t.Fatalf("spend %+v %v", sp, err)
	}
}

// DigitalOcean reads the team's balance with the token.
func TestDigitalOceanSpendReadsTheBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/customers/my/balance" || r.Header.Get("Authorization") != "Bearer dop_v1_token" {
			t.Errorf("asked %s with %q", r.URL, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"month_to_date_usage":"11.21"}`))
	}))
	defer srv.Close()
	c := &connections.Connection{Cloud: connections.DigitalOcean}
	sp, err := readers(srv.URL)[connections.DigitalOcean].Spend(t.Context(), c, []byte("dop_v1_token\n"), now)
	if err != nil || sp.Total != 11.21 {
		t.Fatalf("spend %+v %v", sp, err)
	}
}
