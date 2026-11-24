package deployments

import (
	"context"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

func TestConnectionPartReadsEachPiece(t *testing.T) {
	for _, tc := range []struct {
		address string
		want    map[string]string
	}{
		{"postgres://app:s%40cret@db.example.com:6543/orders?sslmode=require",
			map[string]string{"host": "db.example.com", "port": "6543", "user": "app", "password": "s@cret", "database": "orders"}},
		{"postgresql://app:pw@db.example.com/orders",
			map[string]string{"host": "db.example.com", "port": "5432", "database": "orders"}},
		{"mysql://app:pw@db.example.com/shop?sslmode=require", map[string]string{"port": "3306", "database": "shop"}},
		{"rediss://default:pw@cache.example.com:6380/2", map[string]string{"host": "cache.example.com", "port": "6380", "user": "default", "database": "2"}},
		{"redis://:pw@cache.example.com", map[string]string{"user": "", "password": "pw", "port": "6379", "database": ""}},
		{"mongodb://app:pw@a.example:27018,b.example:27019/events?replicaSet=rs0", map[string]string{"host": "a.example", "port": "27018", "database": "events"}},
		{"mongodb+srv://app:pw@cluster0.example.net/events", map[string]string{"host": "cluster0.example.net", "port": "27017"}},
		{"postgres://app:pw@[2001:db8::1]:5432/orders", map[string]string{"host": "2001:db8::1", "port": "5432"}},
		// An @ left unescaped in a password belongs to the password.
		{"postgres://app:p@ss@db.example.com/orders", map[string]string{"host": "db.example.com", "user": "app", "password": "p@ss"}},
	} {
		for part, want := range tc.want {
			got, err := connectionPart(tc.address, part)
			if err != nil || got != want {
				t.Errorf("%s of %s: %q %v, want %q", part, tc.address, got, err, want)
			}
		}
	}
}

func TestConnectionPartNeverRepeatsTheAddress(t *testing.T) {
	for _, tc := range []struct{ address, part string }{
		{"not a url with s3cret", "host"},
		{"postgres://app:s3cret@db.example.com/orders", "schema"},
		{"custom://app:s3cret@db.example.com/orders", "port"},
		{"postgres://app:s3cret%zz@db.example.com/orders", "password"},
	} {
		_, err := connectionPart(tc.address, tc.part)
		if err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "db.example.com") {
			t.Errorf("%s of %s: %v", tc.part, tc.address, err)
		}
	}
}

func TestAWiredVariableHoldsItsPart(t *testing.T) {
	secrets := &memSecrets{m: map[string][]byte{ResourceURLKey("org", "env", "orders"): []byte("postgres://app:pw@db.example.com:5432/orders?sslmode=require")}}
	r := &Runner{resources: secrets}
	d := &Deployment{OrgID: "org", EnvironmentID: "env"}
	link := planning.EnvLink{Key: "PGHOST", From: "orders", Subject: planning.ResourceSubject, Part: "host"}
	got, extra, err := r.serviceResourceAddress(context.Background(), d, "api", nil, nil, link)
	if err != nil || got != "db.example.com" || extra != nil {
		t.Fatalf("host: %q %v %v", got, extra, err)
	}
	link.Part, link.Key = "", "DATABASE_URL"
	if got, _, err = r.serviceResourceAddress(context.Background(), d, "api", nil, nil, link); err != nil || !strings.HasPrefix(got, "postgres://") {
		t.Fatalf("whole: %q %v", got, err)
	}
	// Through the gateway, only the whole address stands in.
	link.Part, link.Gateway = "password", "us-east.db.example.com"
	if _, _, err = r.serviceResourceAddress(context.Background(), d, "api", nil, nil, link); err == nil || !strings.Contains(err.Error(), "database gateway") {
		t.Fatalf("gateway: %v", err)
	}
}
