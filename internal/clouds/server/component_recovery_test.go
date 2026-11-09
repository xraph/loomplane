package server

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/enginebackup"
)

func TestNativeRepositoryCopyBoundsAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int64
		data  string
		valid bool
	}{{"valid", 6, "native", true}, {"oversized", enginebackup.MaxPayload + 1, "", false}, {"truncated", 10, "short", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var raw bytes.Buffer
			tw := tar.NewWriter(&raw)
			if err := tw.WriteHeader(&tar.Header{Name: "repo/index-0", Typeflag: tar.TypeReg, Size: tc.size}); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(tw, tc.data); err != nil {
				t.Fatal(err)
			}
			_ = tw.Close() // Deliberately incomplete archives exercise the copy boundary.
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(raw.Bytes())
			}))
			defer api.Close()
			client := &Client{Config: &Config{}, HTTP: api.Client()}
			client.HTTP.Transport = redirectDocker{base: api.URL, next: api.Client().Transport}
			err := downloadEngineDirectory(t.Context(), client, "source", "/snapshots/repo", io.Discard)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
			if tc.name == "oversized" && !strings.Contains(err.Error(), "size limit") {
				t.Fatalf("oversized entry reached copy: %v", err)
			}
		})
	}
}

type redirectDocker struct {
	base string
	next http.RoundTripper
}

func (r redirectDocker) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	parsed := *req.URL
	cloned.URL = &parsed
	cloned.URL.Scheme = "http"
	cloned.URL.Host = strings.TrimPrefix(r.base, "http://")
	next := r.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(cloned)
}

func TestNativeRepositoryAggregateLimitRejectsSecondEntry(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, name := range []string{"repo/first", "repo/second"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 3}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw.Bytes()) }))
	defer api.Close()
	client := &Client{Config: &Config{}, HTTP: &http.Client{Transport: redirectDocker{base: api.URL, next: api.Client().Transport}}}
	for _, limit := range []int64{5, 6} {
		err := downloadEngineDirectoryLimit(t.Context(), client, "source", "/snapshots/repo", io.Discard, limit)
		if limit == 5 && (err == nil || !strings.Contains(err.Error(), "size limit")) {
			t.Fatalf("aggregate overflow accepted: %v", err)
		}
		if limit == 6 && err != nil {
			t.Fatalf("exact aggregate limit rejected: %v", err)
		}
	}
}
