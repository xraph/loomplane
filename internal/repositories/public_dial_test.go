package repositories

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicDialer_refusesPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := PublicDialer()(ctx, "tcp", srv.Listener.Addr().String())
	if err == nil {
		conn.Close()
	}
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("dialing a loopback server = %v, want ErrPrivateAddress", err)
	}
	// A name that resolves to loopback is refused after resolution.
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	conn, err = PublicDialer()(ctx, "tcp", "localhost:"+port)
	if err == nil {
		conn.Close()
	}
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("dialing localhost = %v, want ErrPrivateAddress", err)
	}
}

func TestRefusePrivate(t *testing.T) {
	tests := []struct {
		address string
		refused bool
	}{
		{"127.0.0.1:80", true},
		{"[::1]:80", true},
		{"10.0.0.1:80", true},
		{"172.16.5.4:443", true},
		{"192.168.1.1:80", true},
		{"169.254.169.254:80", true},
		{"[fe80::1]:80", true},
		{"[fd00::1]:80", true},
		{"0.0.0.0:80", true},
		{"0.1.2.3:80", true},
		{"[::]:80", true},
		{"224.0.0.1:80", true},
		{"[ff02::1]:80", true},
		{"100.64.0.1:80", true},
		{"100.127.255.254:80", true},
		{"[::ffff:10.0.0.1]:80", true},
		{"[::ffff:127.0.0.1]:80", true},
		{"[64:ff9b::a00:1]:80", true},
		{"[2002:a00:1::]:80", true},
		{"not an address", true},
		{"100.128.0.1:80", false},
		{"93.184.216.34:443", false},
		{"[2606:4700:4700::1111]:443", false},
		{"[64:ff9b::5db8:d822]:443", false},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			err := refusePrivate("tcp", tt.address, nil)
			if tt.refused != errors.Is(err, ErrPrivateAddress) {
				t.Fatalf("refusePrivate(%q) = %v, want refused %v", tt.address, err, tt.refused)
			}
		})
	}
}

// getWithOrgRedirects sends a request carrying a bearer token through the organization
// client's redirect policy. The transport is the test server's, which trusts
// its certificate; the policy under test is the real client's.
func getWithOrgRedirects(t *testing.T, trust *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()
	c := &http.Client{CheckRedirect: orgClient().CheckRedirect, Transport: trust.Transport}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	return c.Do(req)
}

func TestOrgClient_refusesRedirectsToHttp(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
	}))
	defer plain.Close()
	tls1 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/steal", http.StatusFound)
	}))
	defer tls1.Close()

	resp, err := getWithOrgRedirects(t, tls1.Client(), tls1.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, errOrgRedirect) {
		t.Fatalf("redirect to http = %v, want errOrgRedirect", err)
	}
	if plainHits.Load() != 0 {
		t.Fatalf("the http server saw %d requests, want none", plainHits.Load())
	}
}

func TestOrgClient_refusesRedirectsToAnotherHost(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		otherHits.Add(1)
	}))
	defer other.Close()
	tls1 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer tls1.Close()

	resp, err := getWithOrgRedirects(t, tls1.Client(), tls1.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, errOrgRedirect) {
		t.Fatalf("redirect to another host = %v, want errOrgRedirect", err)
	}
	if otherHits.Load() != 0 {
		t.Fatalf("the other server saw %d requests, want none", otherHits.Load())
	}
}

func TestOrgClient_followsRedirectsWithinTheHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/here", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	resp, err := getWithOrgRedirects(t, srv.Client(), srv.URL+"/moved")
	if err != nil {
		t.Fatalf("redirect within the host = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
