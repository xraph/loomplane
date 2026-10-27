// Package issuer makes Loomplane an OpenID Connect issuer, so a cloud can
// trust it in place of a stored secret. It publishes discovery and its
// public keys, and signs short-lived tokens for keyless handouts. AWS,
// Google Cloud and Azure check a token against those keys and trade it
// for their own temporary credentials.
//
// Signing keys are RSA, rotated every rotateEvery. Their public halves are
// rows in loomplane_oidc_keys, and their private halves, with the key
// that makes tickets, are in Vault and nowhere else. None of them, and no
// token or ticket, ever goes in an error or a log.
package issuer

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/xraph/loomplane/internal/clouds/keyless"
)

const (
	// tokenTTL is how long a minted token works.
	tokenTTL = 10 * time.Minute
	// rotateEvery is how old the current key gets before a new one
	// replaces it.
	rotateEvery = 30 * 24 * time.Hour
	// prePublish is how long a new key is in the JWKS before it signs.
	// Azure caches an issuer's keys for up to a day, so a key that signed
	// as soon as it was made could be refused for that long.
	prePublish = 24 * time.Hour
	// publishRetired is how long a retired key stays in the JWKS, so the
	// tokens it signed last still verify.
	publishRetired = 24 * time.Hour
	// cacheFor is how long a Service trusts what it read of its keys
	// before reading them again.
	cacheFor = time.Minute
)

// VaultAppID is the Vault app the issuer's secrets are kept under. Every
// one of them is the instance's, under vaultOrg: a private key under its
// kid, and the ticket key under ticketKeyID.
const VaultAppID = "loomplane-oidc-issuer"

const (
	vaultOrg       = "instance"
	ticketKeyID    = "ticket-key"
	ticketKeyBytes = 32
)

// ErrForged is what Mint returns for a handout whose ticket the issuer
// didn't make for its subject and audience.
var ErrForged = errors.New("the keyless handout's ticket isn't Loomplane's")

// Secrets is where the issuer keeps its private keys and its ticket key.
// connections.VaultSecrets satisfies it.
type Secrets interface {
	Put(ctx context.Context, orgID, id string, v []byte) error
	Reveal(ctx context.Context, orgID, id string) ([]byte, error)
	Delete(ctx context.Context, orgID, id string) error
}

// Service is the issuer.
type Service struct {
	url, environment string
	keys             KeyStore
	secrets          Secrets
	now              func() time.Time

	mu    sync.Mutex
	cache *cached
}

// cached is what a Service read of its keys, and when. It's replaced,
// never changed, so a reader can keep one after the lock is let go.
type cached struct {
	at      time.Time
	keys    []Key
	current *Key
	// signer and ticketKey are read from Vault once there's a current
	// key to read.
	signer    *rsa.PrivateKey
	ticketKey []byte
}

var _ keyless.Minter = (*Service)(nil)

// New returns the issuer at issuerURL, without a trailing slash. now is
// the clock, and nil means time.Now.
func New(issuerURL, environment string, keys KeyStore, secrets Secrets, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{url: strings.TrimRight(issuerURL, "/"), environment: environment, keys: keys, secrets: secrets, now: now}
}

// URL is the issuer, as tokens' iss and discovery's issuer name it.
func (s *Service) URL() string { return s.url }

// Usable says whether a cloud can use this issuer. See Usable.
func (s *Service) Usable() (ok bool, reason string) { return Usable(s.URL(), s.environment) }

// Discovery is the issuer's OpenID configuration, served at
// /.well-known/openid-configuration.
func (s *Service) Discovery() map[string]any {
	return map[string]any{
		"issuer":                                s.url,
		"jwks_uri":                              s.url + "/.well-known/jwks.json",
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"claims_supported":                      []string{"sub", "aud", "exp", "iat", "iss", "jti", "nbf"},
	}
}

// JWKS is the issuer's public keys, served at /.well-known/jwks.json: the
// current one, and any retired within publishRetired.
//
// It reads the keys afresh every time rather than from the cache. A
// replica that has just rotated signs with its new key at once, and a
// cloud that asks another replica for the keys has to find it there.
func (s *Service) JWKS(ctx context.Context) (map[string]any, error) {
	all, err := s.keys.Keys(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	keys := []json.RawMessage{}
	for _, k := range all {
		if k.RetiredAt == nil || now.Sub(*k.RetiredAt) <= publishRetired {
			keys = append(keys, k.Public)
		}
	}
	return map[string]any{"keys": keys}, nil
}

// Ticket is the issuer's HMAC over subject and audience, in base64url. A
// handout carries it, so Mint signs only for handouts the issuer gave
// out. It's a secret.
func (s *Service) Ticket(ctx context.Context, subject, audience string) (string, error) {
	if subject == "" || audience == "" {
		return "", errors.New("a ticket needs a subject and an audience")
	}
	if strings.Contains(subject, "\n") || strings.Contains(audience, "\n") {
		return "", errors.New("a ticket's subject and audience can't hold a line break")
	}
	c, err := s.signing(ctx)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ticketMAC(c.ticketKey, subject, audience)), nil
}

// ticketLabel names what the ticket key's MAC is for, and its version, so
// the key can't be made to mean anything else.
const ticketLabel = "loomplane-keyless-ticket-v1"

func ticketMAC(key []byte, subject, audience string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(ticketLabel + "\n" + subject + "\n" + audience))
	return mac.Sum(nil)
}

// Mint signs a token for h with the current key: iss is the issuer, sub
// and aud are the handout's, and it works for tokenTTL. A handout whose
// ticket doesn't match its subject and audience gets ErrForged. The token
// is a secret.
func (s *Service) Mint(ctx context.Context, h keyless.Handout) (string, error) {
	if h.Type != keyless.Type || h.Subject == "" || h.Audience == "" || h.Ticket == "" {
		return "", errors.New("a token needs a keyless handout with a subject, an audience and a ticket")
	}
	c, err := s.signing(ctx)
	if err != nil {
		return "", err
	}
	given, err := base64.RawURLEncoding.DecodeString(h.Ticket)
	if err != nil || !hmac.Equal(given, ticketMAC(c.ticketKey, h.Subject, h.Audience)) {
		return "", ErrForged
	}

	jti := make([]byte, 16)
	if _, err = rand.Read(jti); err != nil {
		return "", fmt.Errorf("make a token ID: %w", err)
	}
	now := s.now().Unix()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": s.url,
		"sub": h.Subject,
		"aud": h.Audience,
		"iat": now,
		"nbf": now,
		"exp": now + int64(tokenTTL/time.Second),
		"jti": base64.RawURLEncoding.EncodeToString(jti),
	})
	token.Header["kid"] = c.current.KID
	signed, err := token.SignedString(c.signer)
	if err != nil {
		return "", fmt.Errorf("sign a token: %w", err)
	}
	return signed, nil
}

// signing returns keys that can sign. When there's no current key it
// rotates, once, to make one.
func (s *Service) signing(ctx context.Context) (*cached, error) {
	c, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if c.current != nil {
		return c, nil
	}
	if rerr := s.Rotate(ctx); rerr != nil {
		return nil, rerr
	}
	if c, err = s.load(ctx); err != nil {
		return nil, err
	}
	if c.current == nil {
		return nil, errors.New("the issuer has no signing key")
	}
	return c, nil
}

// load returns the keys, read again once cacheFor has passed, with the
// current key's private half and the ticket key.
func (s *Service) load(ctx context.Context) (*cached, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	c := s.cache
	if c == nil || now.Sub(c.at) >= cacheFor || now.Before(c.at) {
		keys, err := s.keys.Keys(ctx)
		if err != nil {
			return nil, err
		}
		c = &cached{at: now, keys: keys, current: currentKey(keys, now)}
		s.cache = c
	}
	if c.current == nil || c.signer != nil {
		return c, nil
	}

	der, err := s.secrets.Reveal(ctx, vaultOrg, c.current.KID)
	if err != nil {
		return nil, fmt.Errorf("read the issuer's signing key: %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("the issuer's signing key in Vault isn't a PKCS #8 key")
	}
	private, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the issuer's signing key in Vault isn't an RSA key")
	}
	ticketKey, err := s.secrets.Reveal(ctx, vaultOrg, ticketKeyID)
	if err != nil {
		return nil, fmt.Errorf("read the issuer's ticket key: %w", err)
	}
	if len(ticketKey) < ticketKeyBytes {
		return nil, errors.New("the issuer's ticket key is too short")
	}
	next := *c
	next.signer, next.ticketKey = private, ticketKey
	s.cache = &next
	return &next, nil
}

// forget drops what the Service read, so the next call reads again.
func (s *Service) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = nil
}
