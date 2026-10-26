package aws

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// Official commercial-region bundle, retrieved from
// https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem.
// Only self-signed CA roots from this bundle enter the trust store.
//
//go:embed rds-ca-bundle.pem
var rdsCABundle []byte

// DBConnector supplies authenticated transport for the shared SQL verifier.
type DBConnector interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	Close() error
}

var rdsEndpointPart = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// PostgresConnector fixes both the SQL destination and TLS server name. It
// never falls back to plaintext, public IPs, ambient roots or caller addresses.
func (s *Session) PostgresConnector(ctx context.Context, instance, host string, port int32) (DBConnector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.account == "" || CheckRegion(s.region) != nil || !rdsEndpointPart.MatchString(instance) || port != 5432 {
		return nil, errors.New("RDS recovery requires its private PostgreSQL endpoint")
	}
	suffix := "." + s.region + ".rds.amazonaws.com"
	if !strings.HasPrefix(host, instance+".") || !strings.HasSuffix(host, suffix) {
		return nil, errors.New("RDS recovery endpoint belongs to another instance or region")
	}
	token := strings.TrimSuffix(strings.TrimPrefix(host, instance+"."), suffix)
	if !rdsEndpointPart.MatchString(token) {
		return nil, errors.New("RDS recovery endpoint has an invalid native name")
	}
	roots, err := rdsRoots(rdsCABundle)
	if err != nil {
		return nil, err
	}
	return &rdsConnector{host: host, roots: roots, lookup: net.DefaultResolver.LookupNetIP, dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}, nil
}

func rdsRoots(bundle []byte) (*x509.CertPool, error) {
	roots := x509.NewCertPool()
	count := 0
	for len(bundle) > 0 {
		block, rest := pem.Decode(bundle)
		if block == nil {
			return nil, errors.New("RDS CA bundle is invalid")
		}
		bundle = rest
		cert, err := x509.ParseCertificate(block.Bytes)
		if block.Type != "CERTIFICATE" || err != nil {
			return nil, errors.New("RDS CA bundle contains an invalid certificate")
		}
		if cert.IsCA && cert.CheckSignatureFrom(cert) == nil {
			roots.AddCert(cert)
			count++
		}
	}
	if count == 0 {
		return nil, errors.New("RDS CA bundle has no root certificates")
	}
	return roots, nil
}

type rdsConnector struct {
	host   string
	roots  *x509.CertPool
	lookup func(context.Context, string, string) ([]netip.Addr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
}

func (c *rdsConnector) Close() error { return nil }

func (c *rdsConnector) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.host == "" || c.roots == nil || c.lookup == nil || c.dial == nil {
		return nil, errors.New("RDS recovery transport is incomplete")
	}
	addresses, err := c.lookup(ctx, "ip", c.host)
	if err != nil {
		return nil, errors.New("resolve the private RDS recovery endpoint")
	}
	if len(addresses) == 0 {
		return nil, errors.New("RDS recovery endpoint has no private address")
	}
	// Validate the whole answer, then dial the selected literal. DNS cannot
	// change between a separate policy check and the connection attempt.
	for _, address := range addresses {
		if !address.IsPrivate() {
			return nil, errors.New("RDS recovery endpoint resolves outside the private network")
		}
	}
	raw, err := c.dial(ctx, "tcp", net.JoinHostPort(addresses[0].String(), "5432"))
	if err != nil {
		return nil, errors.New("connect to the private RDS recovery endpoint")
	}
	established := false
	defer func() {
		if !established {
			_ = raw.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if deadlineErr := raw.SetDeadline(deadline); deadlineErr != nil {
		return nil, deadlineErr
	}
	// PostgreSQL negotiates TLS with SSLRequest before the TLS handshake.
	if _, err = raw.Write([]byte{0, 0, 0, 8, 4, 210, 22, 47}); err != nil {
		return nil, errors.New("negotiate RDS recovery TLS")
	}
	var accepted [1]byte
	if _, err = io.ReadFull(raw, accepted[:]); err != nil || accepted[0] != 'S' {
		return nil, errors.New("RDS recovery refused mandatory TLS")
	}
	secured := tls.Client(raw, &tls.Config{RootCAs: c.roots, ServerName: c.host, MinVersion: tls.VersionTLS12})
	if err = secured.HandshakeContext(ctx); err != nil {
		return nil, errors.New("authenticate the RDS recovery TLS endpoint")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if deadlineErr := raw.SetDeadline(time.Time{}); deadlineErr != nil {
		return nil, deadlineErr
	}
	established = true
	return secured, nil
}
