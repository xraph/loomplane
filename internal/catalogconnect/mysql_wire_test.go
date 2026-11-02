package catalogconnect

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

func mysqlPacket(conn net.Conn, seq byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	if _, err := conn.Write(append(header, payload...)); err != nil {
		return err
	}
	return nil
}
func readMySQLPacket(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if size > 1<<20 {
		return nil, errors.New("oversized packet")
	}
	payload := make([]byte, size)
	_, err := io.ReadFull(conn, payload)
	return payload, err
}
func mysqlVersionFixture(t *testing.T, noTLS bool) (DialFunc, *[]string) {
	t.Helper()
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certServer.Close)
	certificate := certServer.TLS.Certificates[0]
	var commands []string
	var mu sync.Mutex
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "db.example:3306" {
			t.Errorf("wrong target %s %s", network, address)
		}
		client, server := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
		go func() {
			defer server.Close()
			_ = server.SetDeadline(time.Now().Add(3 * time.Second))
			caps := uint32(1 | 8 | 0x200 | 0x2000 | 0x8000 | 0x80000)
			if !noTLS {
				caps |= 0x800
			}
			salt := []byte("12345678abcdefghijkl")
			payload := append([]byte{10}, []byte("8.4.2\x00")...)
			payload = append(payload, 1, 0, 0, 0)
			payload = append(payload, salt[:8]...)
			payload = append(payload, 0, byte(caps), byte(caps>>8), 45, 2, 0, byte(caps>>16), byte(caps>>24), 21)
			payload = append(payload, make([]byte, 10)...)
			payload = append(payload, salt[8:]...)
			payload = append(payload, 0)
			payload = append(payload, []byte("mysql_native_password\x00")...)
			if err := mysqlPacket(server, 0, payload); err != nil {
				return
			}
			auth, err := readMySQLPacket(server)
			if err != nil {
				return
			}
			if noTLS {
				if len(auth) > 32 {
					t.Error("credentials sent without required TLS")
				}
				return
			}
			if len(auth) != 32 || binary.LittleEndian.Uint32(auth)&0x800 == 0 {
				t.Error("no MySQL SSLRequest before credentials")
				return
			}
			secure := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			if err = secure.HandshakeContext(ctx); err != nil {
				return
			}
			auth, err = readMySQLPacket(secure)
			if err != nil {
				return
			}
			if len(auth) < 37 || !bytes.HasPrefix(auth[32:], []byte("app\x00")) {
				t.Error("wrong authenticated user")
				return
			}
			offset := 36
			length := int(auth[offset])
			offset++
			if length != 20 || len(auth) < offset+length {
				t.Error("missing password authentication")
				return
			}
			first := sha1.Sum([]byte("SECRET"))
			second := sha1.Sum(first[:])
			hash := sha1.New()
			hash.Write(salt)
			hash.Write(second[:])
			scramble := hash.Sum(nil)
			for i := range scramble {
				scramble[i] ^= first[i]
			}
			if !bytes.Equal(auth[offset:offset+length], scramble) {
				t.Error("wrong password proof")
				return
			}
			if err = mysqlPacket(secure, 3, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
				return
			}
			query, err := readMySQLPacket(secure)
			if err != nil {
				return
			}
			if len(query) < 1 || query[0] != 3 {
				t.Error("not a text query")
				return
			}
			mu.Lock()
			commands = append(commands, string(query[1:]))
			mu.Unlock()
			if string(query[1:]) != "SELECT VERSION()" {
				t.Errorf("unexpected query %q", query[1:])
				return
			}
			column := []byte{3, 'd', 'e', 'f', 0, 0, 0, 9, 'V', 'E', 'R', 'S', 'I', 'O', 'N', '(', ')', 0, 0x0c, 45, 0, 64, 0, 0, 0, 0xfd, 0, 0, 0, 0, 0}
			for i, packet := range [][]byte{{1}, column, {0xfe, 0, 0, 2, 0}, {5, '8', '.', '4', '.', '2'}, {0xfe, 0, 0, 2, 0}} {
				if err := mysqlPacket(secure, byte(i+1), packet); err != nil {
					return
				}
			}
			_, _ = readMySQLPacket(secure)
		}()
		return client, nil
	}, &commands
}
func TestMySQLProberAuthenticatesOverTLSAndOnlyReadsVersion(t *testing.T) {
	dial, commands := mysqlVersionFixture(t, false)
	got, err := (MySQLProber{}).Probe(t.Context(), dial, "mysql://app:SECRET@db.example/main?sslmode=require", recipes.Requirement{Engine: EngineMySQL, MinVersion: "8.4"})
	if err != nil || got.Version != "8.4.2" || len(got.Problems) != 0 || len(*commands) != 1 || (*commands)[0] != "SELECT VERSION()" {
		t.Fatalf("%+v %v queries=%v", got, err, *commands)
	}
}
func TestMySQLProberRefusesTLSFallbackAndUntrustedCertificates(t *testing.T) {
	for _, mode := range []string{"no-tls", "untrusted-cert"} {
		t.Run(mode, func(t *testing.T) {
			dial, commands := mysqlVersionFixture(t, mode == "no-tls")
			got, err := (MySQLProber{}).Probe(t.Context(), dial, "mysql://app:SECRET@db.example/main?sslmode=verify-full", recipes.Requirement{Engine: EngineMySQL})
			if err == nil || got != nil || len(*commands) != 0 || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%+v %v queries=%v", got, err, *commands)
			}
		})
	}
}
