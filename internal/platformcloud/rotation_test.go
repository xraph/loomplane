//go:build integration

package platformcloud

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

// trustCerts makes the fake agent trust exactly the given client
// certificates, as an agent reloaded with them as Platform.Fingerprint and
// Next would.
func (e *hostEnv) trustCerts(t *testing.T, pems ...string) {
	t.Helper()
	pool := x509.NewCertPool()
	var fps []string
	for _, p := range pems {
		block, _ := pem.Decode([]byte(p))
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		pool.AddCert(cert)
		sum := sha256.Sum256(cert.Raw)
		fps = append(fps, hex.EncodeToString(sum[:]))
	}
	e.server.TLS.ClientCAs = pool
	e.server.Config.Handler = fc.Handler{Host: &fc.Host{Driver: e.driver, Limits: e.driver.caps.Limits}, Platform: fc.NewPlatformTrust(fps...)}
}

// storedFingerprint is the fingerprint of the certificate stored under key,
// or "" when there is none.
func (e *hostEnv) storedFingerprint(t *testing.T, key string) string {
	t.Helper()
	raw, err := e.secret.Reveal(t.Context(), key)
	if errors.Is(err, connections.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	fp, _, err := credentialCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func (e *hostEnv) readyHost(t *testing.T) *NewHost {
	t.Helper()
	e.serve(t, agentLimits())
	nh, err := e.svc.AddHost(t.Context(), "owner", e.in)
	if err != nil {
		t.Fatal(err)
	}
	e.trustCerts(t, nh.ClientCertificate)
	h, err := e.svc.CheckHost(t.Context(), "owner", nh.Host.ID)
	if err != nil || h.Status != hostReady {
		t.Fatalf("%v %+v", err, h)
	}
	if h.ClientExpiresAt == nil || h.ClientExpiresAt.Before(time.Now().Add(700*24*time.Hour)) {
		t.Fatalf("expiry %v", h.ClientExpiresAt)
	}
	return nh
}

func TestRotateHostCertificateWithoutDroppingTheHost(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	id, old := nh.Host.ID, nh.ClientFingerprint
	cur, next, prev := hostSecretKey(id), nextSecretKey(id), previousSecretKey(id)

	nc, err := e.svc.RotateHostCertificate(t.Context(), "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nc.ClientCertificate, "BEGIN CERTIFICATE") || strings.Contains(nc.ClientCertificate, "PRIVATE") ||
		nc.ClientFingerprint == old || nc.Host.NextFingerprint != nc.ClientFingerprint || nc.Host.ClientFingerprint != old {
		t.Fatalf("rotate: %+v", nc)
	}
	if e.storedFingerprint(t, next) != nc.ClientFingerprint || e.storedFingerprint(t, cur) != old {
		t.Fatal("vault doesn't hold the next key beside the current one")
	}
	views, err := e.svc.Hosts(t.Context())
	if err != nil || views[0].Certificate.Rotation != "waiting" {
		t.Fatalf("view %v %+v", err, views)
	}

	// The host hasn't been told yet: nothing changes and it stays ready.
	h, err := e.svc.CheckHost(t.Context(), "owner", id)
	if err != nil || h.Status != hostReady || h.ClientFingerprint != old || h.NextFingerprint != nc.ClientFingerprint {
		t.Fatalf("before the overlap: %v %+v", err, h)
	}

	// The overlap: the host trusts both. The check switches the platform.
	e.trustCerts(t, nh.ClientCertificate, nc.ClientCertificate)
	h, err = e.svc.CheckHost(t.Context(), "owner", id)
	if err != nil || h.Status != hostReady || h.ClientFingerprint != nc.ClientFingerprint || h.PreviousFingerprint != old || h.NextFingerprint != "" {
		t.Fatalf("switch: %v %+v", err, h)
	}
	if e.storedFingerprint(t, cur) != nc.ClientFingerprint || e.storedFingerprint(t, prev) != old || e.storedFingerprint(t, next) != "" {
		t.Fatal("vault after the switch is wrong")
	}
	if _, err = e.svc.RotateHostCertificate(t.Context(), "owner", id); !errors.Is(err, ErrConflict) {
		t.Fatalf("a rotation while the old certificate is trusted: %v", err)
	}
	// Still trusted: the previous is kept.
	if h, err = e.svc.CheckHost(t.Context(), "owner", id); err != nil || h.PreviousFingerprint != old {
		t.Fatalf("%v %+v", err, h)
	}

	// The host drops the old certificate: its key goes.
	e.trustCerts(t, nc.ClientCertificate)
	h, err = e.svc.CheckHost(t.Context(), "owner", id)
	if err != nil || h.Status != hostReady || h.PreviousFingerprint != "" {
		t.Fatalf("retire: %v %+v", err, h)
	}
	if e.storedFingerprint(t, prev) != "" {
		t.Fatal("the previous key is still in Vault")
	}
	views, _ = e.svc.Hosts(t.Context())
	if views[0].Certificate.Rotation != "" {
		t.Fatalf("view %+v", views[0].Certificate)
	}
	if _, err = e.svc.RotateHostCertificate(t.Context(), "owner", id); err != nil {
		t.Fatal("the next rotation is refused:", err)
	}
}

// An owner who swaps the old certificate straight out, with no overlap,
// still gets the host back on the next check.
func TestRotationRecoversFromADirectSwap(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	nc, err := e.svc.RotateHostCertificate(t.Context(), "owner", nh.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.trustCerts(t, nc.ClientCertificate)
	h, err := e.svc.CheckHost(t.Context(), "owner", nh.Host.ID)
	if err != nil || h.Status != hostReady || h.ClientFingerprint != nc.ClientFingerprint || h.PreviousFingerprint != "" || h.Evidence.Reason != "" {
		t.Fatalf("%v %+v", err, h)
	}
	if e.storedFingerprint(t, previousSecretKey(nh.Host.ID)) != "" {
		t.Fatal("the old key outlived a host that never trusted it again")
	}
}

// An agent from before rotation can't report its trust and can't trust two
// certificates. Once the owner swaps it straight to the new one, the old
// key goes and the next rotation isn't blocked.
func TestRotationWithAnAgentThatReportsNoTrust(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	nc, err := e.svc.RotateHostCertificate(t.Context(), "owner", nh.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.trustCerts(t, nc.ClientCertificate)
	inner := e.server.Config.Handler
	e.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.RawQuery = "" // an old agent never reads trust=1
		inner.ServeHTTP(w, r)
	})
	h, err := e.svc.CheckHost(t.Context(), "owner", nh.Host.ID)
	if err != nil || h.Status != hostReady || h.ClientFingerprint != nc.ClientFingerprint || h.PreviousFingerprint != "" {
		t.Fatalf("%v %+v", err, h)
	}
	if e.storedFingerprint(t, previousSecretKey(nh.Host.ID)) != "" {
		t.Fatal("the old key outlived the switch")
	}
	if _, err = e.svc.RotateHostCertificate(t.Context(), "owner", nh.Host.ID); err != nil {
		t.Fatal("the next rotation is blocked:", err)
	}
}

func TestRotateAgainReplacesTheNextCertificate(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	first, err := e.svc.RotateHostCertificate(t.Context(), "owner", nh.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.RotateHostCertificate(t.Context(), "owner", nh.Host.ID)
	if err != nil || second.ClientFingerprint == first.ClientFingerprint || second.Host.NextFingerprint != second.ClientFingerprint {
		t.Fatalf("%v %+v", err, second)
	}
	if e.storedFingerprint(t, nextSecretKey(nh.Host.ID)) != second.ClientFingerprint {
		t.Fatal("vault holds the replaced next key")
	}
	// A host that trusts the replaced certificate isn't switched to.
	e.trustCerts(t, nh.ClientCertificate, first.ClientCertificate)
	if h, _ := e.svc.CheckHost(t.Context(), "owner", nh.Host.ID); h.ClientFingerprint != nh.ClientFingerprint {
		t.Fatal("switched to a replaced certificate")
	}
}

// A switch that stopped after replacing the current key is finished at the
// next check, and the old key isn't lost.
func TestSwitchFinishesAfterAnInterruptedOne(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	id := nh.Host.ID
	nc, err := e.svc.RotateHostCertificate(t.Context(), "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := e.secret.Reveal(t.Context(), hostSecretKey(id))
	next, _ := e.secret.Reveal(t.Context(), nextSecretKey(id))
	if err = e.secret.Put(t.Context(), previousSecretKey(id), cur); err != nil {
		t.Fatal(err)
	}
	if err = e.secret.Put(t.Context(), hostSecretKey(id), next); err != nil {
		t.Fatal(err)
	}
	// Rotating now would leave a credential matching neither: refused.
	if _, err = e.svc.RotateHostCertificate(t.Context(), "owner", id); !errors.Is(err, ErrConflict) {
		t.Fatalf("rotated mid-switch: %v", err)
	}
	e.trustCerts(t, nh.ClientCertificate, nc.ClientCertificate)
	h, err := e.svc.CheckHost(t.Context(), "owner", id)
	if err != nil || h.ClientFingerprint != nc.ClientFingerprint || h.PreviousFingerprint != nh.ClientFingerprint {
		t.Fatalf("%v %+v", err, h)
	}
	if e.storedFingerprint(t, previousSecretKey(id)) != nh.ClientFingerprint {
		t.Fatal("the old key was overwritten")
	}
}

// A next key whose certificate isn't the one shown is never switched to.
func TestMismatchedNextKeyIsNotUsed(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	id := nh.Host.ID
	nc, err := e.svc.RotateHostCertificate(t.Context(), "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := e.secret.Reveal(t.Context(), hostSecretKey(id))
	if err = e.secret.Put(t.Context(), nextSecretKey(id), cur); err != nil {
		t.Fatal(err)
	}
	e.trustCerts(t, nh.ClientCertificate, nc.ClientCertificate)
	h, err := e.svc.CheckHost(t.Context(), "owner", id)
	if err != nil || h.ClientFingerprint != nh.ClientFingerprint || h.NextFingerprint != nc.ClientFingerprint {
		t.Fatalf("%v %+v", err, h)
	}
}

func TestRetireHostDeletesEveryKey(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	id := nh.Host.ID
	nc, err := e.svc.RotateHostCertificate(t.Context(), "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	e.trustCerts(t, nh.ClientCertificate, nc.ClientCertificate)
	if _, err = e.svc.CheckHost(t.Context(), "owner", id); err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.RotateHostCertificate(t.Context(), "owner", id); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = e.svc.DrainHost(t.Context(), "owner", id); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.RetireHost(t.Context(), "owner", id); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{hostSecretKey(id), nextSecretKey(id), previousSecretKey(id)} {
		if e.storedFingerprint(t, key) != "" {
			t.Fatal(key, "outlived the host")
		}
	}
	if _, err = e.svc.RotateHostCertificate(t.Context(), "owner", id); !errors.Is(err, ErrConflict) {
		t.Fatalf("rotated a retired host: %v", err)
	}
}

type memOwnerNotifier struct {
	mu      sync.Mutex
	notices []CertificateNotice
}

func (n *memOwnerNotifier) HostCertificateExpiring(_ context.Context, c CertificateNotice) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notices = append(n.notices, c)
}

func (n *memOwnerNotifier) take() []CertificateNotice {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.notices
	n.notices = nil
	return out
}

func TestExpiryWarningsOncePerStage(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	notifier := &memOwnerNotifier{}
	e.svc.WithOwnerNotifier(notifier)
	h, err := e.store.GetHost(t.Context(), nh.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	expires := *h.ClientExpiresAt
	at := func(d time.Duration) { e.svc.now = func() time.Time { return expires.Add(-d) } }

	steps := []struct {
		left    time.Duration
		notices int
		warning string
	}{
		{200 * 24 * time.Hour, 0, ""},
		{89 * 24 * time.Hour, 1, "expiring"},
		{80 * 24 * time.Hour, 0, "expiring"},
		{29 * 24 * time.Hour, 1, "urgent"},
		{6 * 24 * time.Hour, 1, "urgent"},
		{5 * 24 * time.Hour, 0, "urgent"},
		{-time.Hour, 1, "expired"},
		{-48 * time.Hour, 0, "expired"},
	}
	for _, s := range steps {
		at(s.left)
		if err = e.svc.WarnExpiringCertificates(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := notifier.take()
		if len(got) != s.notices {
			t.Fatalf("%v left: %d notices, want %d", s.left, len(got), s.notices)
		}
		for _, n := range got {
			if n.HostID != nh.Host.ID || n.HostName != e.hostID || !n.ExpiresAt.Equal(expires) {
				t.Fatalf("notice %+v", n)
			}
		}
		views, viewErr := e.svc.Hosts(t.Context())
		if viewErr != nil || views[0].Certificate.Warning != s.warning {
			t.Fatalf("%v left: warning %q, want %q", s.left, views[0].Certificate.Warning, s.warning)
		}
	}

	// A host registered before expiry was recorded gets it from Vault.
	if _, err = e.pg.Exec(t.Context(), `UPDATE loomplane_platform_hosts SET client_expires_at=NULL,cert_notice_stage=0 WHERE id=$1`, nh.Host.ID); err != nil {
		t.Fatal(err)
	}
	at(10 * 24 * time.Hour)
	if err = e.svc.WarnExpiringCertificates(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := notifier.take(); len(got) != 1 {
		t.Fatalf("legacy host: %d notices", len(got))
	}
	h, _ = e.store.GetHost(t.Context(), nh.Host.ID)
	if h.ClientExpiresAt == nil || !h.ClientExpiresAt.Equal(expires) {
		t.Fatalf("expiry filled as %v, want %v", h.ClientExpiresAt, expires)
	}
}

// A switch starts the warnings over for the new certificate.
func TestSwitchResetsExpiryWarnings(t *testing.T) {
	e := newHostEnv(t)
	nh := e.readyHost(t)
	notifier := &memOwnerNotifier{}
	e.svc.WithOwnerNotifier(notifier)
	h, _ := e.store.GetHost(t.Context(), nh.Host.ID)
	expires := *h.ClientExpiresAt
	real := e.svc.now
	late := func() time.Time { return expires.Add(-20 * 24 * time.Hour) }
	// The certificates are made, and the agent checks them, on the real
	// clock; only the warnings run late.
	nc, err := e.svc.RotateHostCertificate(t.Context(), "owner", nh.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.now = late
	if err = e.svc.WarnExpiringCertificates(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := notifier.take(); len(got) != 1 || !got[0].Rotating {
		t.Fatalf("notices %+v", got)
	}
	e.svc.now = real
	e.trustCerts(t, nh.ClientCertificate, nc.ClientCertificate)
	if h, err = e.svc.CheckHost(t.Context(), "owner", nh.Host.ID); err != nil || h.CertNoticeStage != 0 || h.ClientFingerprint != nc.ClientFingerprint {
		t.Fatalf("%v %+v", err, h)
	}
	e.svc.now = late
	if err = e.svc.WarnExpiringCertificates(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Both certificates were made within the same second, so the new one is
	// as close to expiry here. Without the reset its stage would still be
	// the old one's and it would never be warned about.
	got := notifier.take()
	if len(got) != 1 || got[0].Rotating {
		t.Fatalf("the new certificate's warnings didn't start over: %+v", got)
	}
}
