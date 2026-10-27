package gcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type sqlConnectorFactory interface {
	PostgresConnector(context.Context, string, string) (SQLConnector, error)
}

func sqlConnectorTestKey(t *testing.T) *Key {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"type": "service_account", "project_id": "recovery-project", "client_email": "recovery@recovery-project.iam.gserviceaccount.com", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), "token_uri": "https://oauth2.googleapis.com/token"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestSQLConnectorUsesExplicitCredentialAndNeverFallsBackToCallerAddress(t *testing.T) {
	s := &Session{key: sqlConnectorTestKey(t)}
	factory, ok := any(s).(sqlConnectorFactory)
	if !ok {
		t.Fatal("Cloud SQL has no official PostgreSQL connector transport")
	}
	connector, err := factory.PostgresConnector(t.Context(), "us-central1", "recovery-copy")
	if err != nil {
		t.Fatal(err)
	}
	if err = connector.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	var accepted atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted.Store(true)
			_ = conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	conn, err := connector.DialContext(ctx, "tcp", listener.Addr().String())
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("closed connector bypassed native authentication")
	}
	_ = listener.Close()
	<-done
	if accepted.Load() {
		t.Fatal("connector used the caller's address as a TCP fallback")
	}
}

func TestSQLConnectorRejectsMissingAndChangedCredentialsBeforeADC(t *testing.T) {
	key := sqlConnectorTestKey(t)
	for _, scenario := range []string{"missing-key", "missing-bytes", "changed-project", "changed-email", "missing-region", "path-instance", "empty-instance"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := *key
			s := &Session{key: &candidate}
			region, instance := "us-central1", "recovery-copy"
			switch scenario {
			case "missing-key":
				s.key = nil
			case "missing-bytes":
				candidate.raw = nil
			case "changed-project":
				candidate.ProjectID = "source-project"
			case "changed-email":
				candidate.ClientEmail = "another@example.test"
			case "missing-region":
				region = ""
			case "path-instance":
				instance = "source/instance"
			case "empty-instance":
				instance = ""
			}
			factory, ok := any(s).(sqlConnectorFactory)
			if !ok {
				t.Fatal("Cloud SQL connector factory is missing")
			}
			connector, err := factory.PostgresConnector(t.Context(), region, instance)
			if connector != nil {
				_ = connector.Close()
			}
			if err == nil {
				t.Fatal("connector admitted an unreviewed native credential or target")
			}
		})
	}
}
