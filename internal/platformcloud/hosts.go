package platformcloud

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"time"

	"github.com/xraph/loomplane/internal/buildpolicy"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

// EvidenceMaxAge is how old a host's evidence may be before the host takes
// no new placements.
const EvidenceMaxAge = 10 * time.Minute

const (
	hostPending  = "pending"
	hostReady    = "ready"
	hostDraining = "draining"
	hostRetired  = "retired"

	clientCertName     = "loomplane-platform"
	clientCertValidity = 2 * 365 * 24 * time.Hour
)

// HostInput describes a host the platform owner is registering.
type HostInput struct {
	RegionID, Endpoint, HostID, ServerCA string
	Allotment                            Allotment
}

// NewHost is a registered host with the client certificate its agent must
// trust. The certificate is shown once, here, and the private key never
// leaves Vault.
type NewHost struct {
	Host              Host
	ClientCertificate string
	ClientFingerprint string
}

// Fresh reports whether the host's last check is recent enough at now. A
// failed check stamps CheckedAt too, so a host takes placements only when
// Status == "ready" && Fresh(now).
func (h *Host) Fresh(now time.Time) bool {
	return !h.Evidence.CheckedAt.IsZero() && now.Sub(h.Evidence.CheckedAt) <= EvidenceMaxAge
}

func hostSecretKey(hostID string) string { return hostSecretPrefix + hostID }

func validAllotment(a Allotment) error {
	if a.VCPUs < 1 || a.MemoryMiB < 1 || a.DiskMiB < 1 || a.VMs < 1 {
		return errors.New("platformcloud: an allotment needs at least one vCPU, MiB of memory and disk, and one VM")
	}
	return nil
}

// AddHost registers a host: it makes a client key and self-signed
// certificate for the platform to present, keeps the key in Vault, and
// records the host pending. It becomes ready only once CheckHost passes.
func (s *Service) AddHost(ctx context.Context, _ /* actor */ string, in HostInput) (*NewHost, error) {
	if in.RegionID == "" || in.Endpoint == "" || in.HostID == "" || in.ServerCA == "" {
		return nil, errors.New("platformcloud: region, endpoint, host ID and server CA are required")
	}
	if err := validAllotment(in.Allotment); err != nil {
		return nil, err
	}
	vault, err := s.secretStore()
	if err != nil {
		return nil, err
	}
	regions, err := s.store.ListRegions(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(regions, func(r Region) bool { return r.ID == in.RegionID }) {
		return nil, fmt.Errorf("%w: region", ErrNotFound)
	}
	id, err := newClientIdentity(s.now())
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(fc.Credential{CA: in.ServerCA, Certificate: id.CertPEM, Key: id.KeyPEM})
	if err != nil {
		return nil, err
	}
	// Building the client now rejects a malformed endpoint, host ID or CA
	// before anything is stored.
	probe, err := fc.NewClient(raw, map[string]string{"endpoint": in.Endpoint, "host_id": in.HostID})
	if err != nil {
		return nil, errors.New("platformcloud: the endpoint, host ID or server CA is invalid")
	}
	probe.Close()

	expires := id.NotAfter.UTC()
	h := &Host{ID: newID("host_"), RegionID: in.RegionID, Endpoint: in.Endpoint, HostID: in.HostID, Status: hostPending,
		ClientFingerprint: id.Fingerprint, ClientExpiresAt: &expires, Allotment: in.Allotment}
	if err = vault.Put(ctx, hostSecretKey(h.ID), raw); err != nil {
		return nil, fmt.Errorf("platformcloud: store host credential: %w", err)
	}
	if err = s.store.CreateHost(ctx, h); err != nil {
		_ = vault.Delete(ctx, hostSecretKey(h.ID)) //nolint:errcheck // the insert error is the one to report
		return nil, err
	}
	return &NewHost{Host: *h, ClientCertificate: id.CertPEM, ClientFingerprint: id.Fingerprint}, nil
}

// clientIdentity is a client key and the self-signed certificate for it.
type clientIdentity struct {
	CertPEM, KeyPEM, Fingerprint string
	NotAfter                     time.Time
}

// newClientIdentity makes an ECDSA P-256 key and a self-signed client
// certificate for it. The fingerprint is the hex SHA-256 of the
// certificate's DER, which is how the host agent recognizes the platform.
func newClientIdentity(now time.Time) (clientIdentity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return clientIdentity{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return clientIdentity{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: clientCertName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(clientCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return clientIdentity{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return clientIdentity{}, err
	}
	sum := sha256.Sum256(der)
	return clientIdentity{
		CertPEM:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:      string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		Fingerprint: hex.EncodeToString(sum[:]),
		// The certificate keeps whole seconds.
		NotAfter: tmpl.NotAfter.Truncate(time.Second),
	}, nil
}

// client returns an agent client for the host, built from its stored
// credential. The caller closes it.
func (s *Service) client(ctx context.Context, hostID string) (*fc.Client, error) {
	h, err := s.store.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return s.clientFor(ctx, h)
}

func (s *Service) clientFor(ctx context.Context, h *Host) (*fc.Client, error) {
	vault, err := s.secretStore()
	if err != nil {
		return nil, err
	}
	raw, err := vault.Reveal(ctx, hostSecretKey(h.ID))
	if err != nil {
		return nil, fmt.Errorf("platformcloud: read host credential: %w", err)
	}
	c, err := fc.NewClient(raw, map[string]string{"endpoint": h.Endpoint, "host_id": h.HostID})
	if err != nil {
		return nil, errors.New("platformcloud: the stored host credential is unusable")
	}
	return c, nil
}

// CheckHost asks the host about itself and records the answer. The host is
// ready only if it reports its own ID, passes the platform policy and
// offers at least what the allotment takes. Otherwise it is pending, with the
// reason in its evidence. A draining host keeps its status.
func (s *Service) CheckHost(ctx context.Context, actor, hostID string) (*Host, error) {
	h, err := s.store.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if h.Status == hostRetired {
		return nil, fmt.Errorf("%w: the host is retired", ErrConflict)
	}
	now := s.now().UTC()
	ev := Evidence{CheckedAt: now}
	reason := ""
	c, err := s.clientFor(ctx, h)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	caps, checkErr := c.Check(checkCtx)
	if checkErr == nil || caps.Version != 0 {
		ev = evidenceFrom(caps, now)
	}
	switch {
	case caps.Version != 0 && caps.HostID != h.HostID:
		reason = "the host reports a different host ID"
	case checkErr != nil:
		reason = "the host did not pass its check: " + checkErr.Error()
	default:
		if err = buildpolicy.CheckPlatform(platformEvidence(caps)); err != nil {
			reason = err.Error()
		} else if err = withinLimits(h.Allotment, caps.Limits); err != nil {
			reason = err.Error()
		}
	}
	ev.Reason = reason
	h.Evidence = ev
	switch {
	case h.Status == hostDraining:
	case reason == "":
		h.Status = hostReady
	default:
		h.Status = hostPending
	}
	if updateErr := s.store.UpdateHost(ctx, h); updateErr != nil {
		return nil, updateErr
	}
	var trusted []string
	if checkErr == nil {
		trusted = reportedTrust(caps, h.ClientFingerprint)
	}
	pending := h.NextFingerprint
	h = s.advanceRotation(ctx, h, trusted)
	// A host whose owner swapped the old certificate straight out fails the
	// check made with it, then answers the next one. Once switched, check
	// it again with what is now its certificate.
	if checkErr != nil && pending != "" && h.ClientFingerprint == pending {
		return s.CheckHost(ctx, actor, hostID)
	}
	return h, nil
}

func evidenceFrom(caps fc.Capabilities, now time.Time) Evidence {
	return Evidence{
		Isolation: caps.Isolation, Platform: caps.Platform, SMTActive: caps.SMTActive,
		RuntimeIdentity: caps.RuntimeIdentity, Architecture: caps.Architecture,
		ProtocolVersion: strconv.Itoa(caps.Version), DeniedPorts: caps.DeniedPorts,
		PerOrgVMs: caps.Limits.PerOrg.VMs, PerOrgVCPUs: caps.Limits.PerOrg.VCPUs, PerOrgMemoryMiB: caps.Limits.PerOrg.MemoryMiB,
		IngressAddress: caps.IngressAddress, CheckedAt: now,
	}
}

func platformEvidence(caps fc.Capabilities) buildpolicy.PlatformEvidence {
	return buildpolicy.PlatformEvidence{
		Isolation: caps.Isolation, RuntimeIdentity: caps.RuntimeIdentity, HostID: caps.HostID,
		Platform: caps.Platform, SMTActive: caps.SMTActive, DeniedPorts: caps.DeniedPorts,
		PerOrgVMs: caps.Limits.PerOrg.VMs, PerOrgVCPUs: caps.Limits.PerOrg.VCPUs, PerOrgMemoryMiB: caps.Limits.PerOrg.MemoryMiB,
	}
}

func withinLimits(a Allotment, l fc.Limits) error {
	if a.VCPUs > l.VCPUs || a.MemoryMiB > l.MemoryMiB || a.DiskMiB > l.DiskMiB || a.VMs > l.VMs {
		return errors.New("the allotment is larger than the host's own limits")
	}
	return nil
}

// DrainHost stops new placements on the host. What runs there keeps running.
func (s *Service) DrainHost(ctx context.Context, _ /* actor */, hostID string) (*Host, error) {
	h, err := s.store.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if h.Status == hostRetired {
		return nil, fmt.Errorf("%w: the host is retired", ErrConflict)
	}
	h.Status = hostDraining
	if checkErr := s.store.UpdateHost(ctx, h); checkErr != nil {
		return nil, checkErr
	}
	return h, nil
}

// RetireHost takes a drained host out of service for good and deletes its
// credential. It refuses a host that is not draining, and one that still has
// a placement. The status, the placement check and the update happen under
// the host's row lock, so a reservation can't slip in between; the
// credential goes only after that commits.
func (s *Service) RetireHost(ctx context.Context, _ /* actor */, hostID string) error {
	vault, err := s.secretStore()
	if err != nil {
		return err
	}
	err = s.store.withTx(ctx, func(tx driverTx) error {
		h, lockErr := s.store.LockHost(ctx, tx, hostID)
		if lockErr != nil {
			return lockErr
		}
		if h.Status != hostDraining {
			return fmt.Errorf("%w: drain the host before retiring it", ErrConflict)
		}
		var placed int
		if scanErr := tx.QueryRow(ctx, `SELECT count(*) FROM loomplane_platform_placements WHERE host_id=$1 AND state<>'released'`, hostID).Scan(&placed); scanErr != nil {
			return scanErr
		}
		if placed > 0 {
			return fmt.Errorf("%w: %d placements are still on the host", ErrConflict, placed)
		}
		_, execErr := tx.Exec(ctx, `UPDATE loomplane_platform_hosts SET status=$2,revision=revision+1,updated_at=now() WHERE id=$1`, hostID, hostRetired)
		return execErr
	})
	if err != nil {
		return err
	}
	return errors.Join(vault.Delete(ctx, hostSecretKey(hostID)), vault.Delete(ctx, nextSecretKey(hostID)), vault.Delete(ctx, previousSecretKey(hostID)))
}

// SetAllotment changes how much of the host the platform may use. The new
// allotment takes effect once the next check shows the host offers it, so
// the host goes back to pending until then.
func (s *Service) SetAllotment(ctx context.Context, _ /* actor */, hostID string, a Allotment) (*Host, error) {
	if err := validAllotment(a); err != nil {
		return nil, err
	}
	h, err := s.store.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if h.Status == hostRetired {
		return nil, fmt.Errorf("%w: the host is retired", ErrConflict)
	}
	h.Allotment = a
	if h.Status == hostReady {
		h.Status = hostPending
		h.Evidence.CheckedAt = time.Time{}
	}
	if checkErr := s.store.UpdateHost(ctx, h); checkErr != nil {
		return nil, checkErr
	}
	return h, nil
}
