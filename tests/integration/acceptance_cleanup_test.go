//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAcceptanceCleanupClientSurvivesTestCancellation(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Run("deployment", func(t *testing.T) {
		c := &client{t: t, base: server.URL}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c.ctx = ctx
			c.must(http.StatusNoContent, http.MethodDelete, "/test-owned-service", nil, nil)
		})
	})
	if !called {
		t.Fatal("cleanup did not reach the provider")
	}
}
