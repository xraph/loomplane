package platformcloud

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

// A host's client certificate is rotated in three steps, none of which cuts
// the platform off from the host:
//
//  1. RotateHostCertificate makes the next certificate and key. The key goes
//     to Vault beside the current one; the certificate is shown once. The
//     owner adds it to the host's client CA file and its fingerprint as the
//     agent's Platform.Next, and reloads the agent. The host now trusts both.
//  2. Each check then tries the next certificate. Once the host answers it,
//     the platform switches: the next credential becomes the current one and
//     the old one is kept as the previous until step 3.
//  3. The owner makes the new fingerprint the agent's Platform.Fingerprint,
//     drops Next and reloads. Once the host reports it no longer trusts the
//     previous certificate, its key is deleted.

func nextSecretKey(hostID string) string     { return hostSecretKey(hostID) + "/next" }
func previousSecretKey(hostID string) string { return hostSecretKey(hostID) + "/previous" }

// NewCertificate is the next client certificate for a host. Like a new
// host's, it is shown once, here, and its key never leaves Vault.
type NewCertificate struct {
	Host              Host
	ClientCertificate string
	ClientFingerprint string
	ExpiresAt         time.Time
}

// RotateHostCertificate makes the next client certificate for the host and
// keeps its key in Vault. Rotating again before the host trusts the next
// certificate replaces it. It refuses while the host still trusts the
// certificate the last rotation replaced: the host would then trust three.
func (s *Service) RotateHostCertificate(ctx context.Context, _ /* actor */, hostID string) (*NewCertificate, error) {
	vault, err := s.secretStore()
	if err != nil {
		return nil, err
	}
	var out *NewCertificate
	err = s.store.withTx(ctx, func(tx driverTx) error {
		h, lockErr := s.store.LockHost(ctx, tx, hostID)
		if lockErr != nil {
			return lockErr
		}
		if h.Status == hostRetired {
			return fmt.Errorf("%w: the host is retired", ErrConflict)
		}
		if h.PreviousFingerprint != "" {
			return fmt.Errorf("%w: the host still trusts the certificate the last rotation replaced; remove it from the host's configuration, reload the agent and run Check first", ErrConflict)
		}
		raw, revealErr := vault.Reveal(ctx, hostSecretKey(h.ID))
		if revealErr != nil {
			return fmt.Errorf("platformcloud: read host credential: %w", revealErr)
		}
		var cur fc.Credential
		if jsonErr := json.Unmarshal(raw, &cur); jsonErr != nil || cur.CA == "" {
			return errors.New("platformcloud: the stored host credential is unusable")
		}
		// A switch that stopped part way has already replaced the current
		// credential. Replacing the next one now would leave a credential
		// that matches neither, so the switch is finished first.
		if curFP, _, certErr := credentialCertificate(raw); certErr != nil || curFP != h.ClientFingerprint {
			return fmt.Errorf("%w: the host's last switch didn't finish; run Check on the host, then rotate", ErrConflict)
		}
		id, idErr := newClientIdentity(s.now())
		if idErr != nil {
			return idErr
		}
		next, jsonErr := json.Marshal(fc.Credential{CA: cur.CA, Certificate: id.CertPEM, Key: id.KeyPEM})
		if jsonErr != nil {
			return jsonErr
		}
		// The key is stored before the row names it. Should the row's
		// update fail, the row still names the certificate it did before,
		// and advanceRotation refuses a key whose certificate doesn't match.
		if putErr := vault.Put(ctx, nextSecretKey(h.ID), next); putErr != nil {
			return fmt.Errorf("platformcloud: store the next host credential: %w", putErr)
		}
		expires := id.NotAfter.UTC()
		h.NextFingerprint, h.NextExpiresAt = id.Fingerprint, &expires
		if updateErr := updateHostCertTx(ctx, tx, h); updateErr != nil {
			return updateErr
		}
		out = &NewCertificate{Host: *h, ClientCertificate: id.CertPEM, ClientFingerprint: id.Fingerprint, ExpiresAt: expires}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// updateHostCertTx writes the host's certificate fields on tx, which holds
// the host's row lock.
func updateHostCertTx(ctx context.Context, tx driverTx, h *Host) error {
	_, err := tx.Exec(ctx, `UPDATE loomplane_platform_hosts SET client_fingerprint=$2,client_expires_at=$3,next_fingerprint=$4,next_expires_at=$5,
previous_fingerprint=$6,cert_notice_stage=$7,revision=revision+1,updated_at=now() WHERE id=$1`,
		h.ID, h.ClientFingerprint, h.ClientExpiresAt, h.NextFingerprint, h.NextExpiresAt, h.PreviousFingerprint, h.CertNoticeStage)
	if err != nil {
		return storeError(err)
	}
	h.Revision++
	return nil
}

// credentialCertificate reads the fingerprint and expiry of the client
// certificate in a stored credential.
func credentialCertificate(raw []byte) (fingerprint string, notAfter time.Time, err error) {
	var cred fc.Credential
	if err = json.Unmarshal(raw, &cred); err != nil {
		return "", time.Time{}, errors.New("platformcloud: the stored host credential is unreadable")
	}
	block, _ := pem.Decode([]byte(cred.Certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", time.Time{}, errors.New("platformcloud: the stored host credential has no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", time.Time{}, errors.New("platformcloud: the stored host certificate is unreadable")
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), cert.NotAfter.UTC(), nil
}

// rotationCheckTimeout bounds the check made with the next certificate.
const rotationCheckTimeout = 30 * time.Second

// advanceRotation moves the host's rotation on, after a check: it switches
// to the next certificate once the host answers it, and forgets the previous
// one once the host no longer trusts it. trusted is the platform
// fingerprints the host trusts, as reportedTrust reads them, or nil when the
// check failed.
// Nothing here fails the check: a step that can't be taken now is tried
// again at the next one, and the host is returned as it then stands.
func (s *Service) advanceRotation(ctx context.Context, h *Host, trusted []string) *Host {
	if h.NextFingerprint == "" && h.PreviousFingerprint == "" {
		return h
	}
	vault, err := s.secretStore()
	if err != nil {
		return h
	}
	if h.NextFingerprint != "" {
		next, nextTrust, ok := s.tryNext(ctx, vault, h)
		if !ok {
			return h
		}
		switched, switchErr := s.switchCertificate(ctx, vault, h.ID, next)
		if switchErr != nil {
			slog.WarnContext(ctx, "platform cloud could not switch a host to its next certificate", "host", h.ID, "error", switchErr)
			return h
		}
		slog.InfoContext(ctx, "platform cloud switched a host to its next client certificate", "host", h.ID, "fingerprint", switched.ClientFingerprint)
		h, trusted = switched, nextTrust
	}
	if h.PreviousFingerprint == "" || len(trusted) == 0 || slices.Contains(trusted, h.PreviousFingerprint) {
		return h
	}
	retired, err := s.forgetPrevious(ctx, vault, h.ID, h.PreviousFingerprint)
	if err != nil {
		slog.WarnContext(ctx, "platform cloud could not forget a host's previous certificate", "host", h.ID, "error", err)
		return h
	}
	slog.InfoContext(ctx, "platform cloud forgot a host's previous client certificate", "host", h.ID)
	return retired
}

// tryNext checks the host with its next credential. It reports the
// credential and the platform fingerprints the host reported when the host
// answered it as itself.
func (s *Service) tryNext(ctx context.Context, vault Secrets, h *Host) (raw []byte, trusted []string, ok bool) {
	raw, err := vault.Reveal(ctx, nextSecretKey(h.ID))
	if err != nil {
		slog.WarnContext(ctx, "platform cloud can't read a host's next certificate; rotate it again", "host", h.ID, "error", err)
		return nil, nil, false
	}
	fp, _, err := credentialCertificate(raw)
	if err != nil || fp != h.NextFingerprint {
		slog.WarnContext(ctx, "platform cloud's stored next certificate for a host doesn't match the one shown; rotate it again", "host", h.ID)
		return nil, nil, false
	}
	c, err := fc.NewClient(raw, map[string]string{"endpoint": h.Endpoint, "host_id": h.HostID})
	if err != nil {
		return nil, nil, false
	}
	defer c.Close()
	checkCtx, cancel := context.WithTimeout(ctx, rotationCheckTimeout)
	defer cancel()
	caps, err := c.Check(checkCtx)
	// A host that names the platform's fingerprints must name this one.
	// An agent too old to name them can't trust two certificates, so it
	// answered because the owner swapped the old one out.
	if err != nil || caps.HostID != h.HostID || (caps.PlatformFingerprints != nil && !slices.Contains(caps.PlatformFingerprints, fp)) {
		return nil, nil, false
	}
	return raw, reportedTrust(caps, fp), true
}

// reportedTrust is the platform fingerprints a host trusts, from a check it
// answered with the certificate fingerprinted answered. An agent too old to
// report them trusts exactly one certificate, so it is the one it answered.
func reportedTrust(caps fc.Capabilities, answered string) []string {
	if caps.PlatformFingerprints != nil {
		return caps.PlatformFingerprints
	}
	return []string{answered}
}

// switchCertificate makes next the host's current credential and keeps the
// current one as the previous. The previous is written before the current is
// replaced, and a switch that stopped after replacing it is finished from
// where it stopped, so no step loses a key the host may still need.
func (s *Service) switchCertificate(ctx context.Context, vault Secrets, hostID string, next []byte) (*Host, error) {
	fp, notAfter, err := credentialCertificate(next)
	if err != nil {
		return nil, err
	}
	var out *Host
	err = s.store.withTx(ctx, func(tx driverTx) error {
		h, lockErr := s.store.LockHost(ctx, tx, hostID)
		if lockErr != nil {
			return lockErr
		}
		if h.NextFingerprint != fp || h.Status == hostRetired {
			return fmt.Errorf("%w: the host's rotation changed", ErrConflict)
		}
		cur, revealErr := vault.Reveal(ctx, hostSecretKey(h.ID))
		if revealErr != nil {
			return fmt.Errorf("read host credential: %w", revealErr)
		}
		curFP, _, certErr := credentialCertificate(cur)
		if certErr != nil {
			return certErr
		}
		switch curFP {
		case fp:
			// An earlier switch replaced the current credential and stopped
			// before the row said so. The previous is already kept.
		case h.ClientFingerprint:
			if putErr := vault.Put(ctx, previousSecretKey(h.ID), cur); putErr != nil {
				return fmt.Errorf("keep the previous host credential: %w", putErr)
			}
			if putErr := vault.Put(ctx, hostSecretKey(h.ID), next); putErr != nil {
				return fmt.Errorf("store the host credential: %w", putErr)
			}
		default:
			return errors.New("the stored host credential matches neither the current nor the next certificate")
		}
		expires := notAfter
		h.PreviousFingerprint = h.ClientFingerprint
		h.ClientFingerprint, h.ClientExpiresAt = fp, &expires
		h.NextFingerprint, h.NextExpiresAt = "", nil
		h.CertNoticeStage = 0
		if updateErr := updateHostCertTx(ctx, tx, h); updateErr != nil {
			return updateErr
		}
		out = h
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The next key is a copy of the current one now. One left behind is
	// overwritten by the next rotation.
	if delErr := vault.Delete(ctx, nextSecretKey(hostID)); delErr != nil {
		slog.WarnContext(ctx, "platform cloud could not delete a host's spent next credential", "host", hostID, "error", delErr)
	}
	return out, nil
}

// forgetPrevious deletes the previous credential's key, then clears it from
// the row. A delete that succeeds before the update fails is repeated at the
// next check, harmlessly.
func (s *Service) forgetPrevious(ctx context.Context, vault Secrets, hostID, previous string) (*Host, error) {
	var out *Host
	err := s.store.withTx(ctx, func(tx driverTx) error {
		h, lockErr := s.store.LockHost(ctx, tx, hostID)
		if lockErr != nil {
			return lockErr
		}
		if h.PreviousFingerprint != previous {
			return fmt.Errorf("%w: the host's rotation changed", ErrConflict)
		}
		if delErr := vault.Delete(ctx, previousSecretKey(h.ID)); delErr != nil {
			return fmt.Errorf("delete the previous host credential: %w", delErr)
		}
		h.PreviousFingerprint = ""
		if updateErr := updateHostCertTx(ctx, tx, h); updateErr != nil {
			return updateErr
		}
		out = h
		return nil
	})
	return out, err
}

// Certificate expiry warnings start well before a host's certificate runs
// out: at 90, 30 and 7 days left, and once more when it has expired.
const (
	certWarnDays   = 90
	certUrgentDays = 30
	certFinalDays  = 7
)

// certStage is how far a certificate expiring at expires is into its
// warnings at now: 0 none, 1 within 90 days, 2 within 30, 3 within 7, and 4
// expired.
func certStage(expires, now time.Time) int {
	left := expires.Sub(now)
	switch {
	case left <= 0:
		return 4
	case left <= certFinalDays*24*time.Hour:
		return 3
	case left <= certUrgentDays*24*time.Hour:
		return 2
	case left <= certWarnDays*24*time.Hour:
		return 1
	default:
		return 0
	}
}

// CertificateView is how a host's client certificate stands, for the owner.
type CertificateView struct {
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// DaysLeft is whole days until it expires, negative once it has.
	DaysLeft *int `json:"days_left,omitempty"`
	// Warning is "", "expiring" within 90 days, "urgent" within 30, or
	// "expired".
	Warning string `json:"warning"`
	// Rotation is "" with none under way, "waiting" until the host trusts
	// the next certificate, or "retiring" until it stops trusting the
	// previous one.
	Rotation string `json:"rotation"`
}

func certificateView(h *Host, now time.Time) CertificateView {
	v := CertificateView{}
	switch {
	case h.NextFingerprint != "":
		v.Rotation = "waiting"
	case h.PreviousFingerprint != "":
		v.Rotation = "retiring"
	}
	if h.ClientExpiresAt == nil {
		return v
	}
	v.ExpiresAt = h.ClientExpiresAt
	days := int(h.ClientExpiresAt.Sub(now) / (24 * time.Hour))
	v.DaysLeft = &days
	switch certStage(*h.ClientExpiresAt, now) {
	case 1:
		v.Warning = "expiring"
	case 2, 3:
		v.Warning = "urgent"
	case 4:
		v.Warning = "expired"
	}
	return v
}

// CertificateNotice tells the platform owner a host's client certificate is
// running out. It names the host and nothing of any workspace.
type CertificateNotice struct {
	HostID    string    // the platform's ID for the host
	HostName  string    // the host ID the agent reports
	ExpiresAt time.Time // when the current certificate expires
	DaysLeft  int       // whole days left, negative once expired
	Rotating  bool      // a next certificate is waiting for the host
}

// OwnerNotifier tells the platform owner about a host. It must never reach
// a workspace: host details are the platform's alone.
type OwnerNotifier interface {
	HostCertificateExpiring(ctx context.Context, n CertificateNotice)
}

// WithOwnerNotifier sets who hears about expiring host certificates and
// returns the service.
func (s *Service) WithOwnerNotifier(n OwnerNotifier) *Service {
	s.ownerNotifier = n
	return s
}

// WarnExpiringCertificates warns the platform owner about every host whose
// client certificate has entered a further warning stage, once per stage.
// A host registered before expiry was recorded gets it from its credential
// first.
func (s *Service) WarnExpiringCertificates(ctx context.Context) error {
	hosts, err := s.store.ListHosts(ctx, "")
	if err != nil {
		return err
	}
	now := s.now().UTC()
	var errs []error
	for i := range hosts {
		h := &hosts[i]
		if h.Status == hostRetired {
			continue
		}
		if h.ClientExpiresAt == nil {
			if fillErr := s.fillExpiry(ctx, h); fillErr != nil {
				errs = append(errs, fmt.Errorf("host %s: %w", h.ID, fillErr))
				continue
			}
		}
		stage := certStage(*h.ClientExpiresAt, now)
		if stage <= h.CertNoticeStage {
			continue
		}
		n := CertificateNotice{HostID: h.ID, HostName: h.HostID, ExpiresAt: *h.ClientExpiresAt,
			DaysLeft: int(h.ClientExpiresAt.Sub(now) / (24 * time.Hour)), Rotating: h.NextFingerprint != ""}
		// The stage is saved first, so a host that changes under the sweep
		// is warned at the next one rather than at every one.
		h.CertNoticeStage = stage
		if updateErr := s.store.UpdateHost(ctx, h); updateErr != nil {
			errs = append(errs, fmt.Errorf("host %s: %w", h.ID, updateErr))
			continue
		}
		slog.WarnContext(ctx, "platform cloud host client certificate is running out; rotate it", "host", h.ID, "host_id", h.HostID,
			"expires_at", n.ExpiresAt, "days_left", n.DaysLeft)
		if s.ownerNotifier != nil {
			s.ownerNotifier.HostCertificateExpiring(ctx, n)
		}
	}
	return errors.Join(errs...)
}

// fillExpiry records when the host's current certificate expires, read from
// its stored credential.
func (s *Service) fillExpiry(ctx context.Context, h *Host) error {
	vault, err := s.secretStore()
	if err != nil {
		return err
	}
	raw, err := vault.Reveal(ctx, hostSecretKey(h.ID))
	if err != nil {
		return fmt.Errorf("read host credential: %w", err)
	}
	_, notAfter, err := credentialCertificate(raw)
	if err != nil {
		return err
	}
	h.ClientExpiresAt = &notAfter
	return s.store.UpdateHost(ctx, h)
}
