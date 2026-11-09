package deployments

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

// platformRedact passes everything but transport failures through as they
// are. That is only safe while fc.Client.call turns every answer the host
// agent gives into a bare fc sentinel, whatever the agent wrote, so this
// holds it to that: whatever a host says, a workspace reads a sentinel or
// errPlatformHost, never the host's address or the agent's text.
func TestPlatformRedactPassesOnlyAgentSentinels(t *testing.T) {
	const leak = "agent secret 10.0.0.7 host-internal"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusInternalServerError
		switch {
		case strings.HasSuffix(r.Header.Get("X-Want"), "404"):
			code = http.StatusNotFound
		case strings.HasSuffix(r.Header.Get("X-Want"), "409"):
			code = http.StatusConflict
		case strings.HasSuffix(r.Header.Get("X-Want"), "400"):
			code = http.StatusBadRequest
		case strings.HasSuffix(r.Header.Get("X-Want"), "503"):
			code = http.StatusServiceUnavailable
		case strings.HasSuffix(r.Header.Get("X-Want"), "garbage"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(leak))
			return
		}
		http.Error(w, leak, code)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	sentinels := []error{fc.ErrMissing, fc.ErrConflict, fc.ErrInvalid, fc.ErrUnavailable}
	for _, want := range []string{"404", "409", "400", "503", "500", "garbage"} {
		c := &fc.Client{HTTP: &http.Client{Transport: headerTransport{want: want}}, Endpoint: srv.URL, HostID: "hw-1", Details: map[string]string{}}
		_, err := c.Inspect(context.Background(), fc.Scope{Org: "org", Connection: fc.PlatformConnection, Resource: "inst"})
		if err == nil {
			t.Fatalf("%s: no error", want)
		}
		got := platformRedact(context.Background(), "inspect", err)
		if strings.Contains(got.Error(), host) || strings.Contains(got.Error(), "10.0.0.7") || strings.Contains(got.Error(), "agent secret") {
			t.Fatalf("%s: %q leaks the host", want, got)
		}
		// Only a bare sentinel is known to carry no host text.
		if want != "garbage" && !slices.Contains(sentinels, got) {
			t.Fatalf("%s: %v is not an agent sentinel", want, got)
		}
	}
	// A host that can't be reached is redacted whole.
	srv.Close()
	c := &fc.Client{HTTP: &http.Client{}, Endpoint: srv.URL, HostID: "hw-1", Details: map[string]string{}}
	_, err := c.Inspect(context.Background(), fc.Scope{Org: "org", Connection: fc.PlatformConnection, Resource: "inst"})
	var netErr net.Error
	if err == nil || !errors.As(err, &netErr) {
		t.Fatalf("unreachable host: %v", err)
	}
	if got := platformRedact(context.Background(), "inspect", err); !errors.Is(got, errPlatformHost) {
		t.Fatalf("unreachable host redacted to %v", got)
	}
}

// headerTransport asks the test server for an answer through a header.
type headerTransport struct{ want string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Want", h.want)
	return http.DefaultTransport.RoundTrip(r)
}
