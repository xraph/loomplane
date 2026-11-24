package catalogconnect

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestParseMongoDSN(t *testing.T) {
	for _, tc := range []struct {
		dsn  string
		want Endpoint
	}{
		{"mongodb://app:secret@db.example:27018/orders?authSource=admin",
			Endpoint{Host: "db.example", Port: 27018, Username: "app", Database: "orders", TLS: TLSDisable}},
		{"mongodb://app:secret@db.example",
			Endpoint{Host: "db.example", Port: 27017, Username: "app", TLS: TLSDisable}},
		// A replica set shows its first member.
		{"mongodb://app:secret@a.example:27017,b.example:27017,c.example/orders?replicaSet=rs0&tls=true",
			Endpoint{Host: "a.example", Port: 27017, Username: "app", Database: "orders", TLS: TLSVerifyFull}},
		// +srv is TLS unless it says otherwise, and isn't looked up here.
		{"mongodb+srv://app:secret@cluster0.abcd.mongodb.net/orders?retryWrites=true&w=majority",
			Endpoint{Host: "cluster0.abcd.mongodb.net", Port: 27017, Username: "app", Database: "orders", TLS: TLSVerifyFull}},
		{"mongodb+srv://app:secret@cluster0.abcd.mongodb.net/?tls=false",
			Endpoint{Host: "cluster0.abcd.mongodb.net", Port: 27017, Username: "app", TLS: TLSDisable}},
		{"mongodb://app:secret@db.example/?tls=true&tlsAllowInvalidCertificates=true",
			Endpoint{Host: "db.example", Port: 27017, Username: "app", TLS: TLSRequire}},
		// An escaped @ or / in a password is the password's.
		{"mongodb://app:p%40ss%2Fword@db.example/orders?authMechanism=SCRAM-SHA-256",
			Endpoint{Host: "db.example", Port: 27017, Username: "app", Database: "orders", TLS: TLSDisable}},
		{"mongodb://app:secret@[::1]:27017/orders",
			Endpoint{Host: "::1", Port: 27017, Username: "app", Database: "orders", TLS: TLSDisable}},
	} {
		got := Endpoint{Engine: EngineMongoDB}
		if err := parseMongoDSN(&got, tc.dsn); err != nil {
			t.Errorf("%s: %v", tc.dsn, err)
			continue
		}
		tc.want.Engine = EngineMongoDB
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.dsn, got, tc.want)
		}
	}
}

func TestParseMongoDSNRefusesWhatReachesPastTheURL(t *testing.T) {
	for name, dsn := range map[string]string{
		"another scheme":       "postgres://app:secret@db.example/orders",
		"no user":              "mongodb://db.example/orders",
		"a CA file":            "mongodb://app:secret@db.example/?tlsCAFile=/etc/ssl/ca.pem",
		"a client key":         "mongodb://app:secret@db.example/?tlsCertificateKeyFile=/root/key.pem",
		"a proxy":              "mongodb://app:secret@db.example/?proxyHost=10.0.0.1",
		"Loomplane's AWS role": "mongodb://app:secret@db.example/?authMechanism=MONGODB-AWS",
		"Kerberos":             "mongodb://app:secret@db.example/?authMechanism=GSSAPI",
		"a socket":             "mongodb://app:secret@%2Ftmp%2Fmongodb-27017.sock/orders",
		"a srv port":           "mongodb+srv://app:secret@cluster0.example:27017/orders",
		"two srv hosts":        "mongodb+srv://app:secret@a.example,b.example/orders",
		"a bad port":           "mongodb://app:secret@db.example:70000/orders",
		"an empty host":        "mongodb://app:secret@a.example,,b.example/orders",
		"two databases":        "mongodb://app:secret@db.example/orders/more",
	} {
		e := Endpoint{Engine: EngineMongoDB}
		err := parseMongoDSN(&e, dsn)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the error repeats the password: %v", name, err)
		}
	}
}

func TestMongoEndpointsRegisterLikeTheOthers(t *testing.T) {
	svc := NewService(newMemStore(), memSecrets{}, roleAuthz{})
	e, err := svc.Create(t.Context(), admin, CreateInput{Name: "events", Engine: EngineMongoDB, DSN: "mongodb://app:secret@db.example:27017/events?authSource=admin"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Host != "db.example" || e.Port != 27017 || e.Database != "events" || e.Username != "app" {
		t.Fatalf("endpoint: %+v", e)
	}
	if got := dsnAddress("mongodb://app:secret@db.example/events"); got != "db.example:27017" {
		t.Fatalf("audited address: %q", got)
	}
	if EngineTitle(EngineMongoDB) != "MongoDB" {
		t.Fatalf("title: %q", EngineTitle(EngineMongoDB))
	}
}

// TestMongoProberAgainstAServer runs with LOOMPLANE_TEST_MONGO_URL set to a
// MongoDB the test may connect to, such as
// mongodb://root:root@localhost:27017/?authSource=admin.
func TestMongoProberAgainstAServer(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_MONGO_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_MONGO_URL to check the MongoDB prober against a server")
	}
	report, err := MongoProber{}.Probe(WithCapabilities(t.Context()), directDial, dsn, recipes.Requirement{Engine: EngineMongoDB})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version == "" || report.Capabilities == nil {
		t.Fatalf("report: %+v", report)
	}
	t.Logf("version %s, capabilities %+v", report.Version, *report.Capabilities)
	bad := strings.Replace(dsn, "://", "://nobody:wrong@", 1)
	if i := strings.Index(dsn, "@"); i > 0 {
		bad = dsn[:strings.Index(dsn, "://")+3] + "nobody:wrong" + dsn[i:]
	}
	if _, err = (MongoProber{}).Probe(t.Context(), directDial, bad, recipes.Requirement{Engine: EngineMongoDB}); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("a wrong password: %v", err)
	}
}

// directDial dials from the test's own network.
func directDial(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}
