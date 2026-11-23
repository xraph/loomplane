package relay

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

type fakeDialer struct {
	owners map[string]string
	dialed []string
}

func (f *fakeDialer) DialVia(_ context.Context, pool, address string) (net.Conn, error) {
	f.dialed = append(f.dialed, pool+"->"+address)
	a, b := net.Pipe()
	_ = b.Close()
	return a, nil
}

func (f *fakeDialer) PoolOwner(_ context.Context, pool string) (string, error) {
	owner, ok := f.owners[pool]
	if !ok {
		return "", errors.New("not found")
	}
	return owner, nil
}

const pool = "rpool_01h2xcejqtf2nbrexx3vqjhp41"

func TestRelayWithoutRunnerPools(t *testing.T) {
	current.Store(nil)
	if _, err := DialFunc(pool)(t.Context(), "tcp", "10.0.0.5:22"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if err := CheckOwner(t.Context(), "org_1", pool); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRelayDialsThroughThePool(t *testing.T) {
	d := &fakeDialer{owners: map[string]string{pool: "org_1"}}
	Set(d)
	t.Cleanup(func() { current.Store(nil) })
	conn, err := DialFunc(pool)(t.Context(), "tcp", "10.0.0.5:22")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if len(d.dialed) != 1 || d.dialed[0] != pool+"->10.0.0.5:22" {
		t.Fatalf("dialed %v", d.dialed)
	}
	if _, err := DialFunc(pool)(t.Context(), "udp", "10.0.0.5:53"); err == nil {
		t.Fatal("a relay carried UDP")
	}
}

func TestCheckOwner(t *testing.T) {
	Set(&fakeDialer{owners: map[string]string{pool: "org_1"}})
	t.Cleanup(func() { current.Store(nil) })
	if err := CheckOwner(t.Context(), "org_1", pool); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwner(t.Context(), "org_2", pool); err == nil || !strings.Contains(err.Error(), "isn't one of this organization's") {
		t.Fatalf("another organization's pool: %v", err)
	}
	if err := CheckOwner(t.Context(), "org_1", "rpool_missing00000000000000000000"); err == nil {
		t.Fatal("a pool that doesn't exist passed")
	}
	if err := CheckOwner(t.Context(), "org_1", "conn_123"); err == nil {
		t.Fatal("a connection ID passed as a pool")
	}
}
