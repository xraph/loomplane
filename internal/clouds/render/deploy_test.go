package render

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeployRequiresProviderDeploymentIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"build_in_progress"}`))
	}))
	defer server.Close()
	client := New(server.URL)
	if _, err := client.DeployCommit(context.Background(), Key{Value: "test"}, "srv-test", "approved-commit"); err == nil {
		t.Fatal("accepted deployment without provider identity")
	}
}
