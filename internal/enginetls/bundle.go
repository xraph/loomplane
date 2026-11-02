// Package enginetls keeps stable resource trust and issues instance certificates.
package enginetls

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const Generator = "engine-tls-v1"

// Identity binds material to one resource across reviewed replacements.
type Identity struct{ Org, Environment, Resource, Kind string }

// Bundle is stored only in Vault. Only CACert and NodeKey may be mounted;
// CAKey signs instance certificates inside the control plane.
type Bundle struct {
	Version      int       `json:"version"`
	Identity     Identity  `json:"identity"`
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	CACert       []byte    `json:"ca_cert"`
	CAKey        []byte    `json:"ca_key"`
	NodeKey      []byte    `json:"node_key"`
	PasswordHash string    `json:"password_hash,omitempty"`
}

func identityName(identity Identity) (string, error) {
	parts := []string{identity.Org, identity.Environment, identity.Resource, identity.Kind}
	for _, p := range parts {
		if p == "" || strings.ContainsAny(p, "\x00/\r\n") {
			return "", errors.New("invalid engine credential identity")
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "lp-engine-" + hex.EncodeToString(sum[:12]), nil
}

// Generate creates trust once, including a stable password hash when needed.
func Generate(identity Identity, password string, now time.Time) ([]byte, error) {
	name, err := identityName(identity)
	if err != nil {
		return nil, err
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	nodeKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	before, after := now.UTC().Truncate(time.Second).Add(-5*time.Minute), now.UTC().Truncate(time.Second).Add(365*24*time.Hour)
	certificate := &x509.Certificate{SerialNumber: serial(name), Subject: pkix.Name{CommonName: name + " CA"}, NotBefore: before, NotAfter: after, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SignatureAlgorithm: x509.SHA256WithRSA}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caPEM, err := privatePEM(caKey)
	if err != nil {
		return nil, err
	}
	nodePEM, err := privatePEM(nodeKey)
	if err != nil {
		return nil, err
	}
	b := Bundle{Version: 1, Identity: identity, NotBefore: before, NotAfter: after, CACert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), CAKey: caPEM, NodeKey: nodePEM}
	if password != "" {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), 12)
		if hashErr != nil {
			return nil, hashErr
		}
		b.PasswordHash = string(hash)
	}
	return json.Marshal(b)
}

func privatePEM(key *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func privateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid engine private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid engine private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() != 2048 || key.Validate() != nil {
		return nil, errors.New("invalid engine private key")
	}
	return key, nil
}

// Parse refuses missing, expired, corrupt or foreign material. It never rotates it.
func Parse(raw []byte, identity Identity, now time.Time) (*Bundle, error) {
	name, err := identityName(identity)
	if err != nil {
		return nil, err
	}
	if len(raw) > 32768 {
		return nil, errors.New("invalid engine TLS bundle")
	}
	var b Bundle
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&b); err != nil {
		return nil, errors.New("invalid engine TLS bundle")
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid engine TLS bundle")
	}
	if b.Version != 1 || b.Identity != identity || now.Before(b.NotBefore) || !now.Before(b.NotAfter) {
		return nil, errors.New("engine TLS bundle identity or validity is incompatible; recover the recorded material")
	}
	pair, err := tls.X509KeyPair(b.CACert, b.CAKey)
	if err != nil || len(pair.Certificate) != 1 {
		return nil, errors.New("invalid engine CA pair")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid engine CA certificate")
	}
	if !cert.IsCA || cert.Subject.CommonName != name+" CA" || !cert.NotBefore.Equal(b.NotBefore) || !cert.NotAfter.Equal(b.NotAfter) || cert.CheckSignatureFrom(cert) != nil {
		return nil, errors.New("incompatible engine CA certificate")
	}
	caKey, err := privateKey(b.CAKey)
	if err != nil {
		return nil, err
	}
	nodeKey, err := privateKey(b.NodeKey)
	if err != nil {
		return nil, err
	}
	if caKey.PublicKey.Equal(&nodeKey.PublicKey) {
		return nil, errors.New("engine CA signing key cannot be used as a node key")
	}
	if b.PasswordHash != "" {
		if _, err = bcrypt.Cost([]byte(b.PasswordHash)); err != nil {
			return nil, errors.New("invalid engine password hash")
		}
	}
	return &b, nil
}

// CheckPassword verifies that a persisted bootstrap hash still matches its credential.
func (b *Bundle) CheckPassword(password string) error {
	if b.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(b.PasswordHash), []byte(password)) != nil {
		return errors.New("engine password hash does not match the recorded credential")
	}
	return nil
}

// NodeName is the stable certificate subject used by a single-node engine.
func (b *Bundle) NodeName() string {
	name, err := identityName(b.Identity)
	if err != nil {
		return ""
	}
	return name
}

func serial(value string) *big.Int {
	sum := sha256.Sum256([]byte(value))
	sum[0] &= 0x7f
	sum[19] |= 1
	return new(big.Int).SetBytes(sum[:20])
}

// Certificate signs the actual private endpoint with unchanged key and validity.
// RSA PKCS1 signatures make replay for the same hostname byte-for-byte stable.
func (b *Bundle) Certificate(host string, now time.Time) ([]byte, error) {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/:*\x00\r\n ") || net.ParseIP(host) != nil {
		return nil, errors.New("invalid engine endpoint hostname")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	if _, err = Parse(raw, b.Identity, now); err != nil {
		return nil, err
	}
	caBlock, _ := pem.Decode(b.CACert)
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, err
	}
	caKey, err := privateKey(b.CAKey)
	if err != nil {
		return nil, err
	}
	nodeKey, err := privateKey(b.NodeKey)
	if err != nil {
		return nil, err
	}
	names := []string{host}
	if host != "localhost" {
		names = append(names, "localhost")
	}
	leaf := &x509.Certificate{SerialNumber: serial(b.NodeName() + "\x00" + host), Subject: pkix.Name{CommonName: b.NodeName()}, DNSNames: names, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, NotBefore: b.NotBefore, NotAfter: b.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, SignatureAlgorithm: x509.SHA256WithRSA}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &nodeKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
