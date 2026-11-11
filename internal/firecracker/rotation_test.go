package firecracker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	fpA = strings.Repeat("a", 64)
	fpB = strings.Repeat("b", 64)
	fpC = strings.Repeat("c", 64)
)

func TestValidatePlatformRotationFields(t *testing.T) {
	base := HostConfig{
		DeniedPorts: []int{25, 465, 587},
		Limits:      Limits{VMs: 20, VCPUs: 32, MemoryMiB: 65536, DiskMiB: 1 << 20, PerOrg: OrgLimits{VMs: 10, VCPUs: 8, MemoryMiB: 16384}},
	}
	ok := []PlatformPeer{
		{Fingerprint: fpA, Next: fpB},
		{Fingerprint: fpB, Identity: fpA},
		{Fingerprint: fpB, Next: fpC, Identity: fpA},
	}
	for _, p := range ok {
		c := base
		c.Platform = &p
		if err := ValidatePlatform(c); err != nil {
			t.Fatal(p, err)
		}
	}
	bad := []PlatformPeer{
		{Fingerprint: fpA, Next: "short"},
		{Fingerprint: fpA, Next: fpA},
		{Fingerprint: fpA, Next: strings.Repeat("A", 64)},
		{Fingerprint: fpA, Identity: "short"},
	}
	for _, p := range bad {
		c := base
		c.Platform = &p
		if !errors.Is(ValidatePlatform(c), ErrInvalid) {
			t.Fatal("accepted", p)
		}
	}
}

// A rotation must not change the runtime identity: VM records, routes and
// tunnels are bound to it.
func TestRuntimeIdentitySurvivesRotation(t *testing.T) {
	cfg := HostConfig{Subnet: "10.9.0.0/16", DeniedPorts: []int{25, 465, 587}}
	identity := func(p PlatformPeer) string {
		c := cfg
		c.Platform = &p
		raw, err := identityInput(c)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := identity(PlatformPeer{Fingerprint: fpA})
	for _, p := range []PlatformPeer{
		{Fingerprint: fpA, Next: fpB},
		{Fingerprint: fpB, Identity: fpA},
		{Fingerprint: fpB, Next: fpC, Identity: fpA},
		{Fingerprint: fpC, Identity: fpA},
	} {
		if got := identity(p); got != before {
			t.Fatal("identity changed for", p)
		}
	}
	if identity(PlatformPeer{Fingerprint: fpB}) == before {
		t.Fatal("a different platform shares the identity")
	}
}

func TestPlatformPeerTrusted(t *testing.T) {
	if got := (&PlatformPeer{Fingerprint: fpB, Next: fpA, Identity: fpC}).Trusted(); !slices.Equal(got, []string{fpA, fpB}) {
		t.Fatal(got)
	}
	if got := (&PlatformPeer{Fingerprint: fpB, Identity: fpA}).Trusted(); !slices.Equal(got, []string{fpB}) {
		t.Fatal("identity is trusted", got)
	}
	var none *PlatformPeer
	if none.Trusted() != nil {
		t.Fatal("nil peer trusts something")
	}
}

func capabilitiesRequest(t *testing.T, api Handler, cert *x509.Certificate) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/capabilities?trust=1", nil)
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func fingerprintOf(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

func TestHandlerTrustsBothDuringOverlapAndDropsTheOld(t *testing.T) {
	h, _, q := testHost(t)
	oldCert := &x509.Certificate{Raw: []byte("old")}
	newCert := &x509.Certificate{Raw: []byte("new")}
	trust := NewPlatformTrust(fingerprintOf(oldCert))
	api := Handler{Host: h, Platform: trust}
	q.Scope.Connection = PlatformConnection

	if w := capabilitiesRequest(t, api, newCert); w.Code != http.StatusForbidden {
		t.Fatal("the new certificate was trusted before the overlap", w.Code)
	}
	trust.Store([]string{fingerprintOf(oldCert), fingerprintOf(newCert)})
	for _, c := range []*x509.Certificate{oldCert, newCert} {
		w := capabilitiesRequest(t, api, c)
		if w.Code != http.StatusOK {
			t.Fatal(string(c.Raw), w.Code, w.Body.String())
		}
		var caps Capabilities
		if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
			t.Fatal(err)
		}
		want := []string{fingerprintOf(oldCert), fingerprintOf(newCert)}
		slices.Sort(want)
		if !slices.Equal(caps.PlatformFingerprints, want) {
			t.Fatal("capabilities report", caps.PlatformFingerprints)
		}
	}
	if w := platformRequest(t, api, newCert, "/v1/apply", q); w.Code != http.StatusOK {
		t.Fatal("the new certificate can't apply during the overlap", w.Code, w.Body.String())
	}
	trust.Store([]string{fingerprintOf(newCert)})
	if w := capabilitiesRequest(t, api, oldCert); w.Code != http.StatusForbidden {
		t.Fatal("the retired certificate is still trusted", w.Code)
	}
	if w := capabilitiesRequest(t, api, newCert); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
}

// A per-org peer never learns the platform's fingerprints.
func TestCapabilitiesHidePlatformTrustFromPeers(t *testing.T) {
	h, _, q := testHost(t)
	peer := &x509.Certificate{Raw: []byte("peer")}
	api := Handler{Host: h, Peers: []Peer{{Fingerprint: fingerprintOf(peer), Org: q.Scope.Org, Connection: q.Scope.Connection}}, Platform: NewPlatformTrust(fpA)}
	w := capabilitiesRequest(t, api, peer)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PlatformFingerprints") {
		t.Fatal("a peer was told the platform trust", w.Body.String())
	}
}

// An old control plane decodes capabilities strictly and never asks for the
// trust set, so it never gets a field it doesn't know.
func TestCapabilitiesOmitTrustUnlessAsked(t *testing.T) {
	h, _, _ := testHost(t)
	cert := &x509.Certificate{Raw: []byte("platform")}
	api := Handler{Host: h, Platform: NewPlatformTrust(fingerprintOf(cert))}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/capabilities", nil)
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "PlatformFingerprints") {
		t.Fatal(w.Code, w.Body.String())
	}
}

// selfSigned returns a certificate's PEM and fingerprint.
func selfSigned(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), hex.EncodeToString(sum[:])
}

func TestReloadPlatformTrust(t *testing.T) {
	dir := t.TempDir()
	pemA, a := selfSigned(t)
	pemB, b := selfSigned(t)
	caPath := filepath.Join(dir, "clients.pem")
	writeCA := func(pems ...string) {
		t.Helper()
		if err := os.WriteFile(caPath, []byte(strings.Join(pems, "")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCA(pemA)
	current := HostConfig{
		HostID: "h1", ClientCA: caPath, Subnet: "10.9.0.0/16",
		DeniedPorts: []int{25, 465, 587},
		Limits:      Limits{VMs: 20, VCPUs: 32, MemoryMiB: 65536, DiskMiB: 1 << 20, PerOrg: OrgLimits{VMs: 10, VCPUs: 8, MemoryMiB: 16384}},
		Platform:    &PlatformPeer{Fingerprint: a},
	}
	path := filepath.Join(dir, "host.json")
	write := func(c HostConfig) {
		t.Helper()
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	with := func(p PlatformPeer, edit func(*HostConfig)) HostConfig {
		c := current
		c.Platform = &p
		if edit != nil {
			edit(&c)
		}
		return c
	}

	// Next set before its certificate is in the file is refused: the
	// rotation would stall.
	write(with(PlatformPeer{Fingerprint: a, Next: b}, nil))
	if _, _, err := ReloadPlatformTrust(path, current); !errors.Is(err, ErrInvalid) {
		t.Fatal("trusted a fingerprint with no certificate:", err)
	}

	// The overlap, then the switch, each reload with no restart.
	writeCA(pemA, pemB)
	next, pool, err := ReloadPlatformTrust(path, current)
	want := []string{a, b}
	slices.Sort(want)
	if err != nil || pool == nil || !slices.Equal(next.Platform.Trusted(), want) {
		t.Fatal(err, next.Platform)
	}
	write(with(PlatformPeer{Fingerprint: b, Identity: a}, nil))
	writeCA(pemB)
	last, _, err := ReloadPlatformTrust(path, next)
	if err != nil || !slices.Equal(last.Platform.Trusted(), []string{b}) {
		t.Fatal(err)
	}

	writeCA(pemA, pemB)
	refused := map[string]HostConfig{
		"identity changes": with(PlatformPeer{Fingerprint: b}, nil),
		"limits change":    with(PlatformPeer{Fingerprint: a, Next: b}, func(c *HostConfig) { c.Limits.VMs = 21 }),
		"ports change":     with(PlatformPeer{Fingerprint: a, Next: b}, func(c *HostConfig) { c.DeniedPorts = []int{25, 465, 587, 22} }),
		"platform off":     with(PlatformPeer{}, func(c *HostConfig) { c.Platform = nil }),
		"invalid next":     with(PlatformPeer{Fingerprint: a, Next: a}, nil),
	}
	for name, c := range refused {
		write(c)
		if _, _, reloadErr := ReloadPlatformTrust(path, current); !errors.Is(reloadErr, ErrInvalid) {
			t.Fatal(name, "reloaded:", reloadErr)
		}
	}

	// A client CA file with no certificate refuses the reload too.
	writeCA("not pem")
	write(with(PlatformPeer{Fingerprint: a, Next: b}, nil))
	if _, _, err = ReloadPlatformTrust(path, current); !errors.Is(err, ErrInvalid) {
		t.Fatal("an unreadable client CA file reloaded", err)
	}
}
