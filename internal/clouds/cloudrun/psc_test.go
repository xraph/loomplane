package cloudrun

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/gcp"
)

const reviewedPSC = `{"network":{"consumer_project":"app-project","network":"projects/app-project/global/networks/apps","subnetwork":"projects/app-project/regions/us-central1/subnetworks/run"},"instances":["app-project:us-central1:orders"]}`

func TestPSCProviderSendsPrivateProxyAndPreservesLegacySocket(t *testing.T) {
	p, captured := pscProviderFixture(t)
	labels := map[string]string{LabelPublic: "true", "loomplane-cloudsql-psc": reviewedPSC, LabelCloudSQL: "app-project:us-central1:legacy"}
	_, err := p.Provision(t.Context(), cpprovider.ProvisionRequest{InstanceID: id.New(id.PrefixInstance), Labels: labels, Services: []cpprovider.ServiceSpec{{Name: "main", Image: "app:fixture", Env: map[string]string{"MODE": "production"}, Ports: []cpprovider.PortSpec{{Container: 8080}}}}})
	if err != nil {
		t.Fatal(err)
	}
	writes := captured()
	if len(writes) != 1 {
		t.Fatalf("expected one Cloud Run request, got %d", len(writes))
	}
	template := writes[0]["template"].(map[string]any)
	containers := template["containers"].([]any)
	if len(containers) != 2 {
		t.Fatalf("PSC proxy missing from deployment: %+v", template)
	}
	app, proxy := containers[0].(map[string]any), containers[1].(map[string]any)
	if app["name"] != "main" || !reflect.DeepEqual(app["dependsOn"], []any{"cloudsql-psc"}) {
		t.Fatalf("application does not await proxy: %+v", app)
	}
	if proxy["name"] != "cloudsql-psc" || !strings.Contains(proxy["image"].(string), "@sha256:") {
		t.Fatalf("proxy image is not pinned: %+v", proxy)
	}
	args := proxy["args"].([]any)
	for _, arg := range []string{"--psc", "--unix-socket=/cloudsql-psc", "--health-check", "--run-connection-test", "--http-port=9090", "app-project:us-central1:orders"} {
		if !containsArgument(args, arg) {
			t.Errorf("missing proxy argument %q in %v", arg, args)
		}
	}
	if _, ok := proxy["env"]; ok {
		t.Fatal("application environment leaked into proxy")
	}
	if _, ok := proxy["ports"]; ok {
		t.Fatal("proxy must not become the ingress container")
	}
	if proxy["resources"].(map[string]any)["cpuIdle"] != true {
		t.Errorf("sidecar must preserve request-based CPU allocation: %+v", proxy["resources"])
	}
	probe := proxy["startupProbe"].(map[string]any)["httpGet"].(map[string]any)
	if probe["path"] != "/readiness" || probe["port"] != float64(9090) {
		t.Fatalf("startup does not wait for proxy readiness: %+v", probe)
	}
	route := template["vpcAccess"].(map[string]any)
	if route["egress"] != "PRIVATE_RANGES_ONLY" {
		t.Fatal("wrong egress", route)
	}
	nic := route["networkInterfaces"].([]any)[0].(map[string]any)
	if nic["network"] != "projects/app-project/global/networks/apps" || nic["subnetwork"] != "projects/app-project/regions/us-central1/subnetworks/run" {
		t.Fatal("reviewed route lost", nic)
	}
	mounts := app["volumeMounts"].([]any)
	if len(mounts) != 2 {
		t.Fatal("legacy socket mount lost", mounts)
	}
	volumes := template["volumes"].([]any)
	if len(volumes) != 2 {
		t.Fatal("legacy volume lost", volumes)
	}
	memory := volumes[1].(map[string]any)["emptyDir"].(map[string]any)
	if memory["medium"] != "MEMORY" || memory["sizeLimit"] != "1Mi" {
		t.Fatal("socket storage must be bounded and ephemeral", memory)
	}
}

func TestPSCProviderRejectsUnreviewedOrConflictingRoutesBeforeCloudMutation(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed": "{", "empty": "{}", "blank": "",
		"duplicate key":          strings.Replace(reviewedPSC, `"instances":`, `"instances":["app-project:us-central1:shadow"],"instances":`, 1),
		"case alias":             strings.Replace(reviewedPSC, `"instances":`, `"INSTANCES":["app-project:us-central1:shadow"],"instances":`, 1),
		"duplicate network":      strings.Replace(reviewedPSC, `"network":{`, `"network":{"consumer_project":"other-project"},"network":{`, 1),
		"duplicate instance":     strings.Replace(reviewedPSC, `"app-project:us-central1:orders"]`, `"app-project:us-central1:orders","app-project:us-central1:orders"]`, 1),
		"other instance project": strings.Replace(reviewedPSC, "app-project:us-central1:orders", "other-project:us-central1:orders", 1),
		"missing subnet":         strings.Replace(reviewedPSC, `"subnetwork":"projects/app-project/regions/us-central1/subnetworks/run"`, `"subnetwork":""`, 1),
		"other region":           strings.Replace(reviewedPSC, "regions/us-central1", "regions/europe-west1", 1),
		"other network project":  strings.Replace(reviewedPSC, "projects/app-project/global", "projects/other-project/global", 1),
		"other instance region":  strings.Replace(reviewedPSC, ":us-central1:", ":europe-west1:", 1),
		"argument injection":     strings.Replace(reviewedPSC, "app-project:us-central1:orders", "--private-ip", 1),
		"instance overrides":     strings.Replace(reviewedPSC, "app-project:us-central1:orders", "app-project:us-central1:orders?psc=false", 1),
		"unknown field":          strings.Replace(reviewedPSC, `"instances":`, `"fallback":true,"instances":`, 1),
		"trailing input":         reviewedPSC + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, captured := pscProviderFixture(t)
			_, err := p.Provision(t.Context(), cpprovider.ProvisionRequest{InstanceID: id.New(id.PrefixInstance), Labels: map[string]string{LabelPublic: "true", "loomplane-cloudsql-psc": raw}, Services: []cpprovider.ServiceSpec{{Name: "main", Image: "app:fixture"}}})
			if err == nil || len(captured()) != 0 {
				t.Fatalf("unsafe transport reached provider: error=%v writes=%v", err, captured())
			}
		})
	}
	for _, labels := range []map[string]string{
		{LabelPublic: "true", "loomplane-cloudsql-psc": reviewedPSC, LabelVPC: "default"},
		{LabelPublic: "true", "loomplane-cloudsql-psc": reviewedPSC, LabelCPU: "0.5"},
		{LabelPublic: "true", "loomplane-cloudsql-psc": reviewedPSC, LabelCloudSQL: "app-project:us-central1:orders"},
	} {
		p, captured := pscProviderFixture(t)
		_, err := p.Provision(t.Context(), cpprovider.ProvisionRequest{InstanceID: id.New(id.PrefixInstance), Labels: labels, Services: []cpprovider.ServiceSpec{{Name: "main", Image: "app:fixture"}}})
		if err == nil || len(captured()) != 0 {
			t.Fatalf("conflicting transport reached provider: error=%v writes=%v", err, captured())
		}
	}
}

func containsArgument(args []any, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func pscProviderFixture(t *testing.T) (*Provider, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var writes []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"fixture","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && len(writes) == 0:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/services"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			writes = append(writes, body)
			_, _ = w.Write([]byte(`{"done":true}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"uri":"https://fixture.run.app"}`))
		case strings.HasSuffix(r.URL.Path, ":setIamPolicy"):
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"type": "service_account", "project_id": "app-project", "client_email": "test@app-project.iam.gserviceaccount.com", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})), "token_uri": server.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := gcp.ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := gcp.New(server.URL)
	return New("connection", "us-central1", func(ctx context.Context) (*gcp.Session, error) { return client.Session(ctx, key) }, nil), func() []map[string]any { mu.Lock(); defer mu.Unlock(); return append([]map[string]any(nil), writes...) }
}

func TestPSCProviderAvoidsIngressPortCollision(t *testing.T) {
	p, captured := pscProviderFixture(t)
	_, err := p.Provision(t.Context(), cpprovider.ProvisionRequest{InstanceID: id.New(id.PrefixInstance), Labels: map[string]string{LabelPublic: "true", LabelCloudSQLPSC: reviewedPSC}, Services: []cpprovider.ServiceSpec{{Name: "main", Image: "app:fixture", Ports: []cpprovider.PortSpec{{Container: 9090}}}}})
	if err != nil {
		t.Fatal(err)
	}
	containers := captured()[0]["template"].(map[string]any)["containers"].([]any)
	proxy := containers[1].(map[string]any)
	probe := proxy["startupProbe"].(map[string]any)["httpGet"].(map[string]any)
	if probe["port"] != float64(9091) || !containsArgument(proxy["args"].([]any), "--http-port=9091") {
		t.Fatalf("health server collides with ingress: %+v", proxy)
	}
}

func TestPSCProviderRejectsUnixSocketPathOverflow(t *testing.T) {
	p, captured := pscProviderFixture(t)
	raw := strings.Replace(reviewedPSC, "app-project:us-central1:orders", "app-project:us-central1:"+strings.Repeat("a", 80), 1)
	_, err := p.Provision(t.Context(), cpprovider.ProvisionRequest{InstanceID: id.New(id.PrefixInstance), Labels: map[string]string{LabelPublic: "true", LabelCloudSQLPSC: raw}, Services: []cpprovider.ServiceSpec{{Name: "main", Image: "app:fixture"}}})
	if err == nil || len(captured()) != 0 {
		t.Fatalf("overlong PostgreSQL socket path reached provider: error=%v writes=%v", err, captured())
	}
}

func TestPSCProviderLeavesOrdinaryServiceUnchanged(t *testing.T) {
	p, captured := pscProviderFixture(t)
	_, err := p.Provision(t.Context(), cpprovider.ProvisionRequest{InstanceID: id.New(id.PrefixInstance), Labels: map[string]string{LabelPublic: "true", LabelCloudSQL: "app-project:us-central1:legacy", LabelVPC: "default"}, Services: []cpprovider.ServiceSpec{{Name: "main", Image: "app:fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	template := captured()[0]["template"].(map[string]any)
	containers := template["containers"].([]any)
	if len(containers) != 1 {
		t.Fatal("unexpected sidecar", containers)
	}
	app := containers[0].(map[string]any)
	for _, field := range []string{"name", "args", "dependsOn", "startupProbe"} {
		if _, ok := app[field]; ok {
			t.Errorf("PSC field %s added to ordinary app", field)
		}
	}
	if len(template["volumes"].([]any)) != 1 {
		t.Fatal("unexpected volume", template)
	}
	route := template["vpcAccess"].(map[string]any)["networkInterfaces"].([]any)[0].(map[string]any)
	if route["network"] != "default" || route["subnetwork"] != "default" {
		t.Fatal("legacy network changed", route)
	}
}

func TestPSCProviderDeclaresDefaultIngressAndValidProbe(t *testing.T) {
	p, captured := pscProviderFixture(t)
	_, err := p.Provision(t.Context(), cpprovider.ProvisionRequest{InstanceID: id.New(id.PrefixInstance), Labels: map[string]string{LabelPublic: "true", LabelCloudSQLPSC: reviewedPSC}, Services: []cpprovider.ServiceSpec{{Name: "main", Image: "app:fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	containers := captured()[0]["template"].(map[string]any)["containers"].([]any)
	app, proxy := containers[0].(map[string]any), containers[1].(map[string]any)
	ports, ok := app["ports"].([]any)
	if !ok || len(ports) != 1 || ports[0].(map[string]any)["containerPort"] != float64(8080) {
		t.Errorf("multi-container revision needs an explicit ingress: %+v", app)
	}
	probe := proxy["startupProbe"].(map[string]any)
	if probe["timeoutSeconds"].(float64) >= probe["periodSeconds"].(float64) {
		t.Errorf("probe timeout must be less than its interval: %+v", probe)
	}
}

func TestPSCProviderRejectsExcessiveProxyArguments(t *testing.T) {
	p, captured := pscProviderFixture(t)
	var transport PSCTransport
	if err := json.Unmarshal([]byte(reviewedPSC), &transport); err != nil {
		t.Fatal(err)
	}
	transport.Instances = make([]string, 993)
	for i := range transport.Instances {
		transport.Instances[i] = "app-project:us-central1:database-" + strconv.Itoa(i)
	}
	raw, err := json.Marshal(transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Provision(t.Context(), cpprovider.ProvisionRequest{InstanceID: id.New(id.PrefixInstance), Labels: map[string]string{LabelPublic: "true", LabelCloudSQLPSC: string(raw)}, Services: []cpprovider.ServiceSpec{{Name: "main", Image: "app:fixture"}}})
	if err == nil || len(captured()) != 0 {
		t.Fatalf("excessive proxy arguments reached Cloud Run: error=%v writes=%d", err, len(captured()))
	}
}
