package repositories

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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
