package deployments

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/clouds/ecs"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

var measureWindow = metrics.Day.Window(time.Date(2026, 10, 24, 13, 10, 0, 0, time.UTC))

func serviceAccountKey(t *testing.T, tokenURL string) []byte {
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
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs})), "token_uri": tokenURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

// Cloud Run measures the service Ctrl Plane named after the instance, in
// the binding's region.
func TestCloudRun_measuresTheServiceItRan(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	var filters []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path != "/v3/projects/shop/timeSeries" {
			t.Errorf("asked %s", r.URL)
		}
		filters = append(filters, r.URL.Query().Get("filter"))
		_, _ = w.Write([]byte(`{"timeSeries":[]}`))
	}))
	defer srv.Close()
	exec := &CloudRun{Google: gcp.New(srv.URL)}
	b := &planning.Binding{Name: "api", Provider: "cloud-run", Region: "europe-west1", ExternalID: iid.String()}
	m, err := exec.Measure(t.Context(), &connections.Connection{Cloud: connections.GCP}, serviceAccountKey(t, srv.URL+"/token"), b, measureWindow)
	if err != nil || m == nil {
		t.Fatalf("measure: %v %v", m, err)
	}
	if len(filters) != 3 {
		t.Fatalf("asked %d times", len(filters))
	}
	for _, f := range filters {
		if !strings.Contains(f, `service_name="`+cloudrun.ServiceName(iid)+`"`) || !strings.Contains(f, `location="europe-west1"`) {
			t.Errorf("filter %s", f)
		}
	}
	if _, err := exec.Measure(t.Context(), nil, nil, &planning.Binding{ExternalID: "not-an-instance"}, measureWindow); err == nil {
		t.Error("a binding naming no instance was measured")
	}
}

// ECS measures the service Ctrl Plane named after the instance, in
// Loomplane's cluster, in the connection's region.
func TestECS_measuresTheServiceItRan(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Cluster string }
		var services struct{ Services []string }
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		_ = json.Unmarshal(body, &in)
		_ = json.Unmarshal(body, &services)
		if !strings.Contains(r.Header.Get("X-Amz-Target"), "DescribeServices") || in.Cluster != ecsCluster ||
			len(services.Services) != 1 || services.Services[0] != ecs.ServiceName(iid) {
			t.Errorf("asked %s %s", r.Header.Get("X-Amz-Target"), body)
		}
		if !strings.Contains(r.Header.Get("Authorization"), "/eu-west-2/") {
			t.Errorf("signed for %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(`{"services":[{"status":"ACTIVE","loadBalancers":[]}]}`))
	}))
	defer srv.Close()
	exec := &ECS{AWS: aws.New(srv.URL)}
	conn := &connections.Connection{Cloud: connections.AWS, Account: "123456789012", Details: map[string]string{"region": "eu-west-2"}}
	cred := []byte(`{"access_key_id":"AKIATESTTESTTESTTEST","secret_access_key":"secret-test-secret-test"}`)
	_, err := exec.Measure(t.Context(), conn, cred, &planning.Binding{Name: "worker", ExternalID: iid.String()}, measureWindow)
	if _, ok := metrics.IsUnavailable(err); !ok {
		t.Fatalf("a service without a load balancer: %v", err)
	}
}

// Azure measures the container app the binding names.
func TestAzure_measuresTheAppItRan(t *testing.T) {
	app := "/subscriptions/" + azuretest.Subscription + "/resourceGroups/loomplane/providers/Microsoft.App/containerApps/api"
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			_, _ = w.Write([]byte(`{"access_token":"token","expires_in":3600}`))
			return
		}
		if r.URL.Path != app+"/providers/Microsoft.Insights/metrics" {
			t.Errorf("asked %s", r.URL)
		}
		asked++
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer srv.Close()
	exec := &Azure{Client: azure.New(srv.URL)}
	conn := &connections.Connection{Cloud: connections.Azure, Details: azuretest.Settings()}
	if _, err := exec.Measure(t.Context(), conn, []byte(azuretest.Secret), &planning.Binding{Name: "api", ExternalID: app}, measureWindow); err != nil {
		t.Fatal(err)
	}
	if asked != 2 {
		t.Fatalf("asked Azure Monitor %d times", asked)
	}
}

// Render measures the service the binding names, with the connection's
// key and workspace.
func TestRender_measuresTheServiceItRan(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rnd_key" || r.URL.Query().Get("resource") != "srv-api" {
			t.Errorf("asked %s with %q", r.URL, r.Header.Get("Authorization"))
		}
		asked++
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	exec := &Render{Client: render.New(srv.URL)}
	conn := &connections.Connection{Cloud: connections.Render, Details: map[string]string{"owner_id": "tea-x"}}
	if _, err := exec.Measure(t.Context(), conn, []byte("rnd_key\n"), &planning.Binding{Name: "api", ExternalID: "srv-api"}, measureWindow); err != nil {
		t.Fatal(err)
	}
	if asked != 2 {
		t.Fatalf("asked Render %d times", asked)
	}
	_, err := exec.Measure(t.Context(), conn, []byte("rnd_key"), &planning.Binding{Name: "db", ExternalID: "postgres/dpg-1"}, measureWindow)
	if _, ok := metrics.IsUnavailable(err); !ok {
		t.Fatalf("a datastore was measured as a service: %v", err)
	}
}
