package server

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"sync/atomic"
)

// ACMEIssuer replaces Let's Encrypt for every route certificate a server's
// proxy orders. The zero value keeps Let's Encrypt.
type ACMEIssuer struct {
	// Directory is the ACME directory URL. It must be https.
	Directory string
	// Roots is a PEM bundle of the CA certificates the directory's own
	// TLS certificate chains to, for a directory the proxy's system roots
	// don't trust, such as a test CA. Each proxy gets a copy in /config.
	// Empty uses the system roots.
	Roots []byte
}

// ingressACMERoots is where a proxy keeps the issuer's roots, in /config.
const ingressACMERoots = "acme-roots.pem"

var acmeIssuer atomic.Pointer[ACMEIssuer]

// SetACMEIssuer sets the ACME directory, and the roots that trust it, that
// every server proxy orders certificates from from now on. Every config
// rendered in this process carries it, so a stored config and the one a
// proxy runs still compare equal. The zero value goes back to Let's
// Encrypt.
func SetACMEIssuer(v ACMEIssuer) error {
	if v.Directory == "" {
		if len(v.Roots) > 0 {
			return fmt.Errorf("%w: ACME roots need an ACME directory", ErrInvalid)
		}
		acmeIssuer.Store(nil)
		return nil
	}
	if u, err := url.Parse(v.Directory); err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("%w: the ACME directory must be an https URL", ErrInvalid)
	}
	if len(v.Roots) > 0 {
		rest, found := v.Roots, false
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				return fmt.Errorf("%w: the ACME roots hold a %s, not only certificates", ErrInvalid, block.Type)
			}
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return fmt.Errorf("%w: the ACME roots hold a certificate that doesn't parse: %w", ErrInvalid, err)
			}
			found = true
		}
		if !found {
			return fmt.Errorf("%w: the ACME roots hold no PEM certificate", ErrInvalid)
		}
	}
	v.Roots = append([]byte(nil), v.Roots...)
	acmeIssuer.Store(&v)
	return nil
}

// configuredACMEIssuer is the issuer SetACMEIssuer set, or nil for Let's
// Encrypt.
func configuredACMEIssuer() *ACMEIssuer { return acmeIssuer.Load() }
