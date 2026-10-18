package workloadidentity

import (
	"crypto/x509"
	"errors"
	"sync"

	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

var ErrIdentity = errors.New("workload identity is not authorized")

// Trust is an explicitly pinned trust domain. Updates replace the complete root
// set atomically; malformed local updates close verification until repaired.
type Trust struct {
	mu     sync.RWMutex
	domain spiffeid.TrustDomain
	bundle *x509bundle.Bundle
}

func NewTrust(name string, roots []*x509.Certificate) (*Trust, error) {
	td, err := spiffeid.TrustDomainFromString(name)
	if err != nil || name != td.Name() {
		return nil, ErrIdentity
	}
	t := &Trust{domain: td}
	if err := t.Replace(name, roots); err != nil {
		return nil, err
	}
	return t, nil
}
func (t *Trust) Replace(name string, roots []*x509.Certificate) error {
	if name != t.domain.Name() {
		return ErrIdentity
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bundle = nil
	if len(roots) == 0 || len(roots) > 32 {
		return ErrIdentity
	}
	for _, root := range roots {
		if root == nil || !root.IsCA || root.KeyUsage&x509.KeyUsageCertSign == 0 {
			return ErrIdentity
		}
	}
	t.bundle = x509bundle.FromX509Authorities(t.domain, roots)
	return nil
}
func (t *Trust) GetX509BundleForTrustDomain(td spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if td != t.domain || t.bundle == nil {
		return nil, ErrIdentity
	}
	return t.bundle.Clone(), nil
}
func (t *Trust) Domain() string { return t.domain.Name() }
