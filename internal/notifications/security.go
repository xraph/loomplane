// Package notifications connects committed deployment results and account mail
// to Herald and Relay without exposing their unrestricted administration APIs.
package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/herald/id"
	"github.com/xraph/herald/message"
	heraldstore "github.com/xraph/herald/store"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/endpoint"
	relayid "github.com/xraph/relay/id"
	relaystore "github.com/xraph/relay/store"
)

var ErrInvalid = errors.New("invalid notification settings")
var ErrNotFound = errors.New("notification resource not found")
var ErrMailDisabled = errors.New("email delivery is disabled")
var ErrDeliveryFailed = errors.New("email delivery failed")

// Redact before writing, rather than after SMTP, so a crash never retains an
// invitation or reset token. Rendering and delivery still receive the original.
type privateHeraldStore struct{ heraldstore.Store }

func (s privateHeraldStore) CreateMessage(ctx context.Context, m *message.Message) error {
	copyMessage := *m
	copyMessage.Recipient, copyMessage.Subject, copyMessage.Body, copyMessage.Error, copyMessage.ProviderMessageID = "", "", "", "", ""
	copyMessage.Metadata = nil
	return s.Store.CreateMessage(ctx, &copyMessage)
}
func (s privateHeraldStore) RecordDelivery(ctx context.Context, messageID id.MessageID, d message.Delivery) error {
	if d.Error != "" {
		d.Error = "delivery failed"
	}
	d.ProviderMessageID = ""
	return s.Store.RecordDelivery(ctx, messageID, d)
}

type encryptedRelayStore struct {
	relaystore.Store
	cipher cipher.AEAD
}

func newRelayStore(s relaystore.Store, key []byte) (*encryptedRelayStore, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &encryptedRelayStore{Store: s, cipher: aead}, nil
}
func (s *encryptedRelayStore) encrypt(ep *endpoint.Endpoint) (*endpoint.Endpoint, error) {
	cp := *ep
	nonce := make([]byte, s.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	encrypted := s.cipher.Seal(nonce, nonce, []byte(ep.Secret), []byte(ep.ID.String()+":"+ep.TenantID))
	cp.Secret = "lp1:" + base64.RawStdEncoding.EncodeToString(encrypted)
	return &cp, nil
}
func (s *encryptedRelayStore) decrypt(ep *endpoint.Endpoint) (*endpoint.Endpoint, error) {
	if !strings.HasPrefix(ep.Secret, "lp1:") {
		return nil, errors.New("webhook signing secret is not encrypted")
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(ep.Secret, "lp1:"))
	if err != nil || len(b) < s.cipher.NonceSize() {
		return nil, errors.New("invalid webhook signing secret")
	}
	n := s.cipher.NonceSize()
	plain, err := s.cipher.Open(nil, b[:n], b[n:], []byte(ep.ID.String()+":"+ep.TenantID))
	if err != nil {
		return nil, errors.New("cannot decrypt webhook signing secret")
	}
	cp := *ep
	cp.Secret = string(plain)
	return &cp, nil
}
func (s *encryptedRelayStore) CreateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	cp, err := s.encrypt(ep)
	if err != nil {
		return err
	}
	return s.Store.CreateEndpoint(ctx, cp)
}
func (s *encryptedRelayStore) UpdateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	cp, err := s.encrypt(ep)
	if err != nil {
		return err
	}
	return s.Store.UpdateEndpoint(ctx, cp)
}
func (s *encryptedRelayStore) GetEndpoint(ctx context.Context, endpointID relayid.ID) (*endpoint.Endpoint, error) {
	ep, err := s.Store.GetEndpoint(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	return s.decrypt(ep)
}
func (s *encryptedRelayStore) ListEndpoints(ctx context.Context, tenant string, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error) {
	eps, err := s.Store.ListEndpoints(ctx, tenant, opts)
	if err != nil {
		return nil, err
	}
	return s.decryptList(eps)
}
func (s *encryptedRelayStore) Resolve(ctx context.Context, tenant, event string) ([]*endpoint.Endpoint, error) {
	eps, err := s.Store.Resolve(ctx, tenant, event)
	if err != nil {
		return nil, err
	}
	return s.decryptList(eps)
}
func (s *encryptedRelayStore) decryptList(eps []*endpoint.Endpoint) ([]*endpoint.Endpoint, error) {
	out := make([]*endpoint.Endpoint, 0, len(eps))
	for _, ep := range eps {
		cp, err := s.decrypt(ep)
		if err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, nil
}

// Preserve the capability that makes Relay acceptance atomic. The PostgreSQL
// store pins recipient IDs; endpoint secrets are decrypted only at delivery.
func (s *encryptedRelayStore) AcceptEvent(ctx context.Context, req acceptance.Request, maxAttempts int) (*acceptance.Receipt, error) {
	a, ok := s.Store.(acceptance.Store)
	if !ok {
		return nil, acceptance.ErrUnsupported
	}
	return a.AcceptEvent(ctx, req, maxAttempts)
}

func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n") {
		return fmt.Errorf("%w: use a public HTTPS webhook URL without credentials or fragments", ErrInvalid)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return fmt.Errorf("%w: webhook destination must be public", ErrInvalid)
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicAddress(ip) {
		return fmt.Errorf("%w: webhook destination must be public", ErrInvalid)
	}
	if port := u.Port(); port != "" && port != "443" {
		return fmt.Errorf("%w: webhook HTTPS port must be 443", ErrInvalid)
	}
	return nil
}

var reservedNetworks = []netip.Prefix{
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("::/96"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, network := range reservedNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}
func webhookClient() *http.Client {
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 20, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid webhook address")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("webhook DNS resolution failed")
		}
		if len(ips) == 0 {
			return nil, errors.New("webhook destination has no addresses")
		}
		for _, ip := range ips {
			if !publicAddress(ip) {
				return nil, errors.New("webhook destination resolves to a restricted address")
			}
		}
		var last error
		for _, ip := range ips {
			var d net.Dialer
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
