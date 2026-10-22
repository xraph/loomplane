package planning

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

// A deploy tells a database gateway extension that isn't registered, a
// wiring error, from one that is but has no signing key.
func TestDatabaseGatewayTrust(t *testing.T) {
	c := forge.NewContainer()
	trust := databaseGatewayTrust(c)
	if keys, err := trust(); err == nil || !strings.Contains(err.Error(), "isn't registered") || keys != nil {
		t.Fatalf("no service: %v %v", keys, err)
	}
	if err := forge.ProvideValue(c, dbgateway.NewService(nil, nil, nil, plane.TrustKey{}, nil)); err != nil {
		t.Fatal(err)
	}
	if keys, err := trust(); err != nil || len(keys) != 0 {
		t.Fatalf("no signing key: %v %v", keys, err)
	}

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	configured := forge.NewContainer()
	want := plane.TrustKey{PublicKey: pub, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	if err = forge.ProvideValue(configured, dbgateway.NewService(nil, nil, key, want, nil)); err != nil {
		t.Fatal(err)
	}
	keys, err := databaseGatewayTrust(configured)()
	if err != nil || len(keys) != 1 || !bytes.Equal(keys[0].PublicKey, pub) {
		t.Fatalf("configured: %v %v", keys, err)
	}
}
