package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCloudClientPreservesNullableAssignmentAndGeneration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/p/environments/env/cloud" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"assignment":null,"revision":4}`))
	}))
	defer srv.Close()
	value, err := New(srv.URL, "key").EnvironmentCloud(t.Context(), "p", "env")
	if err != nil || value.Assignment != nil || value.Revision != 4 {
		t.Fatalf("%+v %v", value, err)
	}
}
func TestCloudClientWaitReturnsLatestOnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"sync","status":"running","attempt":3}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	v, err := New(srv.URL, "key").WaitCloudSync(ctx, "project", "sync")
	if !errors.Is(err, context.DeadlineExceeded) || v == nil || v.Attempt != 3 {
		t.Fatalf("lost latest record: %+v %v", v, err)
	}
}
