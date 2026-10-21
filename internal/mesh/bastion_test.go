package mesh

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestBastionPrivateHTTPAndRevocation(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoke", true: "expire"}[expiry], func(t *testing.T) {
			f := newGatewayFixture(t)
			lease := time.Minute
			if expiry {
				lease = domain.MaxClockSkew + time.Second
			}
			entered, closed := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write([]byte("started\n")); err != nil {
					t.Error(err)
				}
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
				close(closed)
			}))
			defer backend.Close()
			origin, err := url.Parse(backend.URL)
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.ParseUint(origin.Port(), 10, 16)
			if err != nil {
				t.Fatal(err)
			}
			proxy, err := NewLoopbackHTTP(origin.Hostname(), uint16(port))
			if err != nil {
				t.Fatal(err)
			}
			f.setPort(t, uint16(port))
			f.apply(t, 1, lease)
			server, client := f.server(t, proxy)
			done := make(chan struct{})
			go func() {
				defer close(done)
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/orders", nil)
				req.Host = "orders.internal"
				res, err := client.Do(req)
				if err == nil {
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("proxy did not reach backend")
			}
			if !expiry {
				f.input.Grants = nil
				f.input.Generation++
				f.apply(t, 2, time.Minute)
			}
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("revocation did not close the actual upstream request")
			}
			<-done
			if code := request(t, client, server.URL+"/orders"); code != http.StatusForbidden || calls.Load() != 1 {
				t.Fatalf("revoked call forwarded: %d %d", code, calls.Load())
			}
		})
	}
}
func TestBastionLocalBackendBoundaries(t *testing.T) {
	for _, host := range []string{"example.com", "localhost", "10.0.0.1", "169.254.169.254", "0.0.0.0", "::", "127.0.0.1.evil.test"} {
		if _, err := NewLoopbackHTTP(host, 8080); err == nil {
			t.Fatalf("accepted non-literal loopback %s", host)
		}
	}
	if _, err := NewLoopbackHTTP("127.0.0.1", 0); err == nil {
		t.Fatal("accepted zero port")
	}
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer sink.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := NewLoopbackHTTP(host, uint16(port))
	if err != nil {
		t.Fatal(err)
	}
	f := newGatewayFixture(t)
	f.setPort(t, uint16(port))
	f.apply(t, 1, time.Minute)
	server, client := f.server(t, proxy)
	if code := request(t, client, server.URL+"/orders"); code != http.StatusTemporaryRedirect || leaked.Load() != 0 {
		t.Fatalf("proxy followed backend redirect: %d %d", code, leaked.Load())
	}
}

func TestBastionPortMustMatchExport(t *testing.T) {
	f := newGatewayFixture(t)
	handler, err := NewLoopbackHTTP("127.0.0.1", 8081)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewHTTPGuard(HTTPConfig{Trust: f.trust, Policy: f.policy, Target: f.target, ExportID: "orders", Port: 8080, Authority: "orders.internal", Handler: handler}); err == nil {
		t.Fatal("proxy can forward an approved export to another port")
	}
}
