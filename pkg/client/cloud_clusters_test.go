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

func TestLifecycleClientExactMoneyAndCleanupSelector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/clouds/cloud/clusters/cluster/capacity":
			if r.Method != "GET" {
				t.Error("read used mutation")
			}
			_, _ = w.Write([]byte(`{"resource_id":"cluster","observation_revision":2}`))
		case "/api/v1/clouds/cloud/clusters/cluster/capacity/refresh":
			if r.Method != "POST" {
				t.Error("refresh used GET")
			}
			_, _ = w.Write([]byte(`{"resource_id":"cluster","observation_revision":3}`))
		case "/api/v1/clouds/cloud/cluster-operations/op":
			_, _ = w.Write([]byte(`{"id":"op","money_estimate":{"hourly_micro_usd":"9223372036854775807"}}`))
		case "/api/v1/deployments/deployment/cleanup/cancel":
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if len(in) != 1 || in["expected_review_id"] != "accepted-review" {
				t.Fatalf("selector: %+v", in)
			}
			_, _ = w.Write([]byte(`{"review_id":"accepted-review","status":"cancelled"}`))
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := New(server.URL, "key")
	if v, e := c.CloudClusterCapacity(t.Context(), "cloud", "cluster"); e != nil || v.ObservationRevision != 2 {
		t.Fatalf("capacity %+v %v", v, e)
	}
	if v, e := c.RefreshCloudClusterCapacity(t.Context(), "cloud", "cluster"); e != nil || v.ObservationRevision != 3 {
		t.Fatalf("refresh %+v %v", v, e)
	}
	if v, e := c.CloudClusterOperation(t.Context(), "cloud", "op"); e != nil || v.MoneyEstimate.HourlyMicroUSD != "9223372036854775807" {
		t.Fatalf("money %+v %v", v, e)
	}
	if v, e := c.CancelFailedCleanup(t.Context(), "deployment", "accepted-review"); e != nil || v.ReviewID != "accepted-review" {
		t.Fatalf("cancel %+v %v", v, e)
	}
}
