package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func trustServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func TestProbeRequiresUnauthenticatedPost(t *testing.T) {
	requests := make(chan *http.Request, 1)
	bodies := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		requests <- r
		bodies <- string(body)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	trustServer(t, server)

	if err := probe(t.Context(), server.URL+"/"); err != nil {
		t.Fatalf("trusted gateway returning 401 must be ready: %v", err)
	}
	request := <-requests
	if request.Method != http.MethodPost || request.URL.Path != "/v1/send" {
		t.Errorf("request = %s %s, want POST /v1/send", request.Method, request.URL.Path)
	}
	if body := <-bodies; body != "{}" {
		t.Errorf("body = %q, want {}", body)
	}
	if contentType := request.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	for _, header := range []string{"Authorization", "X-Herald-Key", "X-Herald-Timestamp", "X-Herald-Signature"} {
		if request.Header.Get(header) != "" {
			t.Errorf("readiness request must omit %s", header)
		}
	}
}

func TestProbeRejectsUnexpectedStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	trustServer(t, server)

	if err := probe(t.Context(), server.URL); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want status 503 error, got %v", err)
	}
}

func TestProbeRejectsRedirect(t *testing.T) {
	var followed atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			followed.Store(true)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	trustServer(t, server)

	if err := probe(t.Context(), server.URL); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("want redirect status error, got %v", err)
	}
	if followed.Load() {
		t.Fatal("readiness probe followed a redirect")
	}
}

func TestProbeRejectsUntrustedCertificate(t *testing.T) {
	var requested atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requested.Store(true)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	if err := probe(t.Context(), server.URL); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("want certificate verification error, got %v", err)
	}
	if requested.Load() {
		t.Fatal("probe sent HTTP request over untrusted TLS")
	}
}
