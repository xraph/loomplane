package kubernetes

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestPullSecret_requiresCredentialFreeTableAndNeverFallsBackToSecretData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		valid  bool
	}{
		{"existing", 200, `{"kind":"Table","apiVersion":"meta.k8s.io/v1","columnDefinitions":[{"name":"Name"},{"name":"Type"}],"rows":[{"cells":["existing-pull","kubernetes.io/dockerconfigjson"]}]}`, true},
		{"missing", 404, `{}`, false}, {"denied", 403, `{}`, false}, {"unsupported", 406, `{}`, false},
		{"full Secret fallback", 200, `{"kind":"Secret","apiVersion":"v1","data":{"credential":"private"}}`, false},
		{"wrong type", 200, `{"kind":"Table","apiVersion":"meta.k8s.io/v1","columnDefinitions":[{"name":"Name"},{"name":"Type"}],"rows":[{"cells":["existing-pull","Opaque"]}]}`, false},
		{"embedded object", 200, `{"kind":"Table","apiVersion":"meta.k8s.io/v1","columnDefinitions":[{"name":"Name"},{"name":"Type"}],"rows":[{"cells":["existing-pull","kubernetes.io/dockerconfigjson"],"object":{"kind":"Secret"}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads++
				if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/selected/secrets/existing-pull" || r.URL.Query().Get("includeObject") != "None" || r.Header.Get("Accept") != "application/json;as=Table;g=meta.k8s.io;v=v1" {
					t.Errorf("unsafe Secret request: %s %s Accept=%s", r.Method, r.URL, r.Header.Get("Accept"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer api.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})
			c, err := New(&Config{Namespace: "selected", Details: map[string]string{"image_pull_secret": "existing-pull"}, REST: &rest.Config{Host: api.URL, TLSClientConfig: rest.TLSClientConfig{CAData: ca}}})
			if err != nil {
				t.Fatal(err)
			}
			err = c.CheckPullSecret(t.Context())
			if (err == nil) != tc.valid || reads != 1 {
				t.Fatalf("table check: err=%v reads=%d", err, reads)
			}
			if !tc.valid {
				runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) { return c, nil }}
				if _, err := runtime.Apply(t.Context(), id.New(id.PrefixInstance), cpprovider.ServiceSpec{Name: "main", Image: "private/image@sha256:fixture"}, map[string]string{owned.LabelOrg: "org_one", owned.LabelProject: "proj_one", owned.LabelEnvironment: "env_one"}); err == nil {
					t.Fatal("invalid pull Secret reached rollout")
				}
				if reads != 2 {
					t.Fatalf("invalid pull Secret triggered other object requests: %d", reads)
				}
			}
		})
	}
}
