package gcp

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cloud.google.com/go/cloudsqlconn"
)

type sqlPSCConnectorFactory interface {
	PostgresPSCConnector(context.Context, string, string) (SQLConnector, error)
}

func TestSQLPSCConnectorDoesNotFallBackToPublicIPOrCallerAddress(t *testing.T) {
	for _, scenario := range []string{"psc-dns", "no-psc", "wrong-region"} {
		t.Run(scenario, func(t *testing.T) {
			s := &Session{key: sqlConnectorTestKey(t)}
			factory, ok := any(s).(sqlPSCConnectorFactory)
			if !ok {
				t.Fatal("native PITR has no explicit PSC connector transport")
			}
			connector, err := factory.PostgresPSCConnector(t.Context(), "us-central1", "recovery-copy")
			if err != nil {
				t.Fatal(err)
			}
			if err = connector.Close(); err != nil {
				t.Fatal(err)
			}
			var cert string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/sql/v1beta4/projects/recovery-project/instances/recovery-copy/connectSettings" && r.URL.Path != "/sql/v1beta4/projects/recovery-project/instances/recovery-copy:generateEphemeralCert" {
					t.Errorf("connector escaped its fixed instance: %s", r.URL)
				}
				if r.Method == http.MethodPost {
					_ = json.NewEncoder(w).Encode(map[string]any{"ephemeralCert": map[string]string{"cert": cert}})
					return
				}
				region := "us-central1"
				if scenario == "wrong-region" {
					region = "europe-west1"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"region": region, "backendType": "SECOND_GEN", "databaseVersion": "POSTGRES_17", "pscEnabled": scenario != "no-psc", "dnsName": "recovery-copy.us-central1.sql-psc.goog", "ipAddresses": []any{map[string]string{"type": "PRIMARY", "ipAddress": "198.51.100.1"}}, "serverCaCert": map[string]string{"cert": cert}})
			}))
			defer server.Close()
			cert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.TLS.Certificates[0].Certificate[0]}))
			attempts := make(chan string, 3)
			transport, err := cloudsqlconn.NewDialer(t.Context(), cloudsqlconn.WithCredentialsJSON(s.key.raw), cloudsqlconn.WithLazyRefresh(), cloudsqlconn.WithHTTPClient(server.Client()), cloudsqlconn.WithAdminAPIEndpoint(server.URL), cloudsqlconn.WithDialFunc(func(_ context.Context, _, address string) (net.Conn, error) {
				attempts <- address
				return nil, errors.New("test PSC endpoint is unreachable")
			}))
			if err != nil {
				t.Fatal(err)
			}
			// Replace only this test object's API/TCP transport. Keep the factory's
			// target identity and network selection, then run the official dialer.
			connector.(*sqlConnector).dialer = transport
			defer func() { _ = connector.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, err := connector.DialContext(ctx, "tcp", "127.0.0.1:5432")
			if conn != nil || err == nil {
				t.Fatal("unreachable PSC route unexpectedly connected", err)
			}
			close(attempts)
			count := 0
			for address := range attempts {
				count++
				if scenario != "psc-dns" || address != "recovery-copy.us-central1.sql-psc.goog:3307" {
					t.Fatal("connector used a public, mismatched or caller-supplied route", address)
				}
			}
			if scenario == "psc-dns" && count != 1 {
				t.Fatal("official connector did not try the fixed PSC endpoint exactly once", count, err)
			}
		})
	}
}
