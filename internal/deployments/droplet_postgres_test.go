package deployments

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type dropletDial func(context.Context, string, string) (net.Conn, error)

func (d dropletDial) TargetDial(_ context.Context, org, target string) (catalogconnect.DialFunc, io.Closer, error) {
	if org != "org" || target != "ssh" {
		return nil, nil, errors.New("wrong SSH connection")
	}
	return catalogconnect.DialFunc(d), nil, nil
}

func TestDropletPostgresDeploymentCheck(t *testing.T) {
	const host = "private-db.db.ondigitalocean.com"
	const uri = "postgresql://app:fixture-password@" + host + ":25060/app?sslmode=require"
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	creds := buildCredentialFunc(func(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
		return &connections.Connection{OrgID: org, ID: id, Cloud: "digitalocean", Status: connections.StatusReady}, []byte("token"), nil
	})
	dep := &Deployment{OrgID: "org", EnvironmentID: "env", ProjectID: "project"}
	link := planning.EnvLink{Key: "DATABASE_URL", From: "db", Subject: planning.ResourceSubject, DropletPostgres: true}
	bindings := map[string]*planning.Binding{"db": {Name: "db", Subject: planning.ResourceSubject, OrgID: "org", EnvironmentID: "env", ProjectID: "project", Provider: "digitalocean", ConnectionID: "do", ExternalID: "db/123", Config: []byte(`{"kind":"postgres"}`)}}
	for _, tc := range []struct {
		name            string
		private         string
		mode            string
		dialed, success bool
	}{
		{"private login accepted", uri, "allow", true, true},
		{"trusted sources deny login", uri, "reject", true, false},
		{"no private route", uri, "unreachable", true, false},
		{"only public endpoint", "", "", false, false},
		{"public address override", uri + "&host=public.example", "", false, false},
		{"deployment variable overrides private URL", uri, "override", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("unexpected provider request")
				}
				switch req.URL.Path {
				case "/v2/databases/123":
					_ = json.NewEncoder(w).Encode(map[string]any{"database": map[string]any{"id": "123", "engine": "pg", "connection": map[string]string{"uri": "postgresql://public.example/app"}, "private_connection": map[string]string{"uri": tc.private}}})
				case "/v2/databases/123/ca":
					_ = json.NewEncoder(w).Encode(map[string]any{"ca": map[string][]byte{"certificate": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}})
				default:
					t.Errorf("unexpected provider path %s", req.URL.Path)
				}
			}))
			defer api.Close()
			called := false
			dial := dropletDial(func(_ context.Context, network, addr string) (net.Conn, error) {
				called = true
				if network != "tcp" || addr != host+":25060" {
					return nil, fmt.Errorf("unexpected dial %s", addr)
				}
				if tc.mode == "unreachable" {
					return nil, errors.New("no route")
				}
				local, remote := net.Pipe()
				go func() {
					defer remote.Close()
					if err := serveDropletPostgres(remote, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, tc.mode == "reject"); err != nil {
						t.Errorf("Postgres fixture: %v", err)
					}
				}()
				return local, nil
			})
			r := Runner{dropletPostgres: &DropletPostgresChecker{Client: digitalocean.New(api.URL), Credentials: creds, Dialer: dial}}
			private, _, resolveErr := r.serviceResourceAddress(t.Context(), dep, "worker", nil, bindings, link)
			if (resolveErr == nil) != (tc.dialed || tc.mode == "override") || (resolveErr == nil && private != uri) {
				t.Fatal("unexpected private address resolution")
			}
			if tc.mode == "override" {
				private = "override"
			}
			job := &Job{Deployment: dep, Env: map[string]string{link.Key: private}, Connection: &connections.Connection{ID: "ssh", Cloud: "server"}, Action: planning.Action{Provider: "server", Service: &planning.ServiceConfig{Target: "ssh", Env: []planning.EnvLink{link}}}}
			err := r.checkDropletPostgres(t.Context(), job, bindings)
			if called != tc.dialed || (err == nil) != tc.success || err != nil && strings.Contains(err.Error(), "fixture-password") {
				t.Fatalf("called=%t err=%v", called, err)
			}
			if tc.mode == "reject" && !strings.Contains(err.Error(), "database refused") {
				t.Fatalf("missing database refusal: %v", err)
			}
			called = false
			if r.checkDropletPostgres(t.Context(), job, nil) == nil || called {
				t.Fatal("missing binding reached network")
			}
		})
	}
}

// serveDropletPostgres exercises pgx's TLS negotiation, startup and read-only version query over the forwarded connection.
func serveDropletPostgres(conn net.Conn, cert tls.Certificate, reject bool) error {
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	wire := pgproto3.NewBackend(conn, conn)
	msg, err := wire.ReceiveStartupMessage()
	if err != nil {
		return err
	}
	if _, ok := msg.(*pgproto3.SSLRequest); !ok {
		return fmt.Errorf("expected SSL request, got %T", msg)
	}
	if _, err = conn.Write([]byte("S")); err != nil {
		return err
	}
	secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	wire = pgproto3.NewBackend(secure, secure)
	msg, err = wire.ReceiveStartupMessage()
	if err != nil {
		return err
	}
	if _, ok := msg.(*pgproto3.StartupMessage); !ok {
		return fmt.Errorf("expected startup, got %T", msg)
	}
	if reject {
		wire.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28000", Message: "access denied"})
		return wire.Flush()
	}
	wire.Send(&pgproto3.AuthenticationOk{})
	wire.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err = wire.Flush(); err != nil {
		return err
	}
	description := &pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("server_version"), DataTypeOID: 25, DataTypeSize: -1}}}
	for {
		message, readErr := wire.Receive()
		if readErr != nil {
			return readErr
		}
		switch q := message.(type) {
		case *pgproto3.Parse:
			if q.Query != "SHOW server_version" {
				return errors.New("unexpected query")
			}
			wire.Send(&pgproto3.ParseComplete{})
		case *pgproto3.Describe:
			if q.ObjectType == 'S' {
				wire.Send(&pgproto3.ParameterDescription{})
			}
			wire.Send(description)
		case *pgproto3.Bind:
			wire.Send(&pgproto3.BindComplete{})
		case *pgproto3.Execute:
			wire.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("17.1")}})
			wire.Send(&pgproto3.CommandComplete{CommandTag: []byte("SHOW")})
		case *pgproto3.Sync:
			wire.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if err = wire.Flush(); err != nil {
				return err
			}
		case *pgproto3.Terminate:
			return nil
		default:
			return fmt.Errorf("unexpected message %T", q)
		}
	}
}
