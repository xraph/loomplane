package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestPendingCleanupChecksRuntimeReferenceAndRetainsVolumes(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	reference := "another-operation"
	exists := true
	deletes := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/"+ContainerName(iid)+"/json" && exists:
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "exact-container", "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: "env", owned.LabelRuntime: reference}}})
		case r.Method == http.MethodDelete:
			if r.URL.Path != "/containers/exact-container" || r.URL.Query().Get("v") != "0" {
				t.Errorf("unexpected data cleanup: %s", r.URL)
			}
			exists = false
			deletes++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	target, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
			return http.DefaultTransport.RoundTrip(r)
		})}}, nil
	}}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "proj", owned.LabelEnvironment: "env", owned.LabelRuntime: "accepted-operation"}
	if err = runtime.RemovePending(t.Context(), iid, labels); err == nil || deletes != 0 {
		t.Fatal("different operation deleted", err)
	}
	reference = "accepted-operation"
	if err = runtime.RemovePending(t.Context(), iid, labels); err != nil || deletes != 1 {
		t.Fatalf("exact cleanup: %d %v", deletes, err)
	}
	if err = runtime.RemovePending(t.Context(), iid, labels); err != nil || deletes != 1 {
		t.Fatal("absence retry mutated", err)
	}
}
