package aws

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestRDSConnectorRequiresPrivateAddressAndAuthenticatedTLS(t *testing.T) {
	host := "lp-recovery-" + strings.Repeat("a", 32) + ".abcdef.us-east-1.rds.amazonaws.com"
	for _, scenario := range []string{"trusted", "unknown-ca", "wrong-host", "ssl-refused", "public-dns", "mixed-dns", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			leafHost := host
			if scenario == "wrong-host" {
				leafHost = "another.abcdef.us-east-1.rds.amazonaws.com"
			}
			cert, root := rdsTestCertificate(t, leafHost)
			if scenario == "unknown-ca" {
				_, root = rdsTestCertificate(t, host)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			done := make(chan error, 1)
			dials := 0
			connector := &rdsConnector{host: host, roots: root, lookup: func(context.Context, string, string) ([]netip.Addr, error) {
				addresses := []netip.Addr{netip.MustParseAddr("10.24.0.5")}
				if scenario == "public-dns" {
					addresses = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
				}
				if scenario == "mixed-dns" {
					addresses = append(addresses, netip.MustParseAddr("1.1.1.1"))
				}
				return addresses, nil
			}, dial: func(_ context.Context, network, address string) (net.Conn, error) {
				dials++
				if network != "tcp" || address != "10.24.0.5:5432" {
					t.Error("transport used caller address", network, address)
				}
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					_ = server.SetDeadline(time.Now().Add(3 * time.Second))
					var request [8]byte
					if _, err := io.ReadFull(server, request[:]); err != nil {
						done <- err
						return
					}
					if binary.BigEndian.Uint32(request[:4]) != 8 || binary.BigEndian.Uint32(request[4:]) != 80877103 {
						done <- errors.New("missing PostgreSQL SSL negotiation")
						return
					}
					if scenario == "ssl-refused" {
						_, err := server.Write([]byte{'N'})
						done <- err
						return
					}
					if _, err := server.Write([]byte{'S'}); err != nil {
						done <- err
						return
					}
					encrypted := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
					if err := encrypted.HandshakeContext(ctx); err != nil {
						done <- err
						return
					}
					var marker [1]byte
					_, err := io.ReadFull(encrypted, marker[:])
					if err == nil && marker[0] != 'Q' {
						err = errors.New("SQL payload not protected by TLS")
					}
					done <- err
				}()
				return client, nil
			}}
			conn, err := connector.DialContext(ctx, "unix", "/unreviewed/socket")
			if scenario == "trusted" {
				if err != nil || conn == nil {
					t.Fatal("private TLS connection refused", err)
				}
				if _, err = conn.Write([]byte{'Q'}); err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
			} else if err == nil || conn != nil {
				t.Fatal("unverified transport accepted", scenario)
			}
			if dials > 0 {
				if serverErr := <-done; scenario == "trusted" && serverErr != nil {
					t.Fatal(serverErr)
				}
			}
			if (scenario == "public-dns" || scenario == "mixed-dns" || scenario == "cancelled") && dials != 0 {
				t.Fatal("unsafe address was dialed", dials)
			}
		})
	}
}

func rdsTestCertificate(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test RDS root"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	raw, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{host}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter}
	raw, err = x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key}, pool
}

func TestRDSConnectorBindsCommercialInstanceRegionAndRootBundle(t *testing.T) {
	session := New("").Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "test-test-test-test-secret"}, "us-east-1", "123456789012")
	name := "lp-recovery-" + strings.Repeat("a", 32)
	for _, host := range []string{name + ".abcdef.us-east-1.rds.amazonaws.com", "other.abcdef.us-east-1.rds.amazonaws.com", name + ".abcdef.eu-west-1.rds.amazonaws.com", name + ".abcdef.us-east-1.rds.amazonaws.com.attacker.example", "127.0.0.1", name + ".us-east-1.rds.amazonaws.com"} {
		connector, err := session.PostgresConnector(t.Context(), name, host, 5432)
		expected := host == name+".abcdef.us-east-1.rds.amazonaws.com"
		if expected && (err != nil || connector == nil) {
			t.Fatal("official RDS root bundle or endpoint rejected", err)
		}
		if !expected && (err == nil || connector != nil) {
			t.Fatal("unbound SQL endpoint accepted", host)
		}
	}
	if roots, err := rdsRoots([]byte("not a certificate")); err == nil || roots != nil {
		t.Fatal("invalid root bundle accepted")
	}
}
