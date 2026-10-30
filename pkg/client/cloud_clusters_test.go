package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClusterClientWireContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/clouds/cloud/cluster-operations/operation/apply" || r.Method != "POST" {
			t.Errorf("wrong route %s %s", r.Method, r.URL.Path)
		}
		var in InfrastructureApplyInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in.Review != "exact-digest" {
			t.Errorf("review=%q", in.Review)
		}
		_, _ = w.Write([]byte(`{"id":"operation","digest":"exact-digest","status":"accepted","revision":2}`))
	}))
	defer server.Close()
	out, err := New(server.URL, "test-key").ApplyCloudCluster(t.Context(), "cloud", "operation", "exact-digest")
	if err != nil || out.ID != "operation" || out.Revision != 2 {
		t.Fatalf("%+v %v", out, err)
	}
}
