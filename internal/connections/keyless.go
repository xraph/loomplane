package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/keyless"
	"github.com/xraph/loomplane/internal/security"
)

// reservationTTL is how long a keyless connection's ID is held while you
// set up the cloud's trust, which names the ID before the connection
// exists.
const reservationTTL = 24 * time.Hour

// KeylessIssuer is Loomplane's own OpenID Connect issuer, which the clouds
// trust. *issuer.Service is the real one.
type KeylessIssuer interface {
	URL() string
	// Ticket is a secret: it never goes in an error, a log or a response.
	Ticket(ctx context.Context, subject, audience string) (string, error)
	// Usable says whether a cloud could reach the issuer, and why not.
	Usable() (ok bool, reason string)
}

// KeylessCloud is a cloud that can trust Loomplane's tokens. Each cloud's
// client implements it.
type KeylessCloud interface {
	// KeylessSettings checks the settings a keyless connection takes, such
	// as a role to assume, and keeps only those.
	KeylessSettings(in map[string]string) (map[string]string, error)
	// KeylessAudience is the aud the cloud's trust expects.
	KeylessAudience(settings map[string]string) string
	// KeylessSetup is how to set up the cloud's trust in subject.
	KeylessSetup(issuer, subject string) []keyless.SetupStep
}

// KeylessSetup is what you need to set up a cloud's trust in Loomplane:
// the issuer, the subject the trust names, the audience it expects and
// the steps to take. It carries no ticket. ExpiresAt is when a new
// connection's reserved ID lapses.
type KeylessSetup struct {
	ConnectionID string              `json:"connection_id"`
	Issuer       string              `json:"issuer"`
	Subject      string              `json:"subject"`
	Audience     string              `json:"audience,omitempty"`
	Steps        []keyless.SetupStep `json:"steps"`
	ExpiresAt    *time.Time          `json:"expires_at,omitempty"`
}

// KeylessInput connects a cloud keylessly, with the ID PrepareKeyless
// reserved.
type KeylessInput struct {
	ConnectionID string            `json:"connection_id"`
	Name         string            `json:"name"`
	Settings     map[string]string `json:"settings"`
}

// Reservation holds a keyless connection's ID for the person setting it up.
type Reservation struct {
	ID, OrgID, UserID, Cloud string
	ExpiresAt, CreatedAt     time.Time
}

// keylessJSON reports whether credential is a keyless handout, usable or
// not.
func keylessJSON(credential []byte) bool {
	_, ok, _ := keyless.Parse(credential) //nolint:errcheck // only whether it's a handout matters here
	return ok
}

// keylessIssuer is the issuer, if a cloud could reach it.
func (s *Service) keylessIssuer() (KeylessIssuer, error) {
	if s.Keyless == nil {
		return nil, fmt.Errorf("%w: Loomplane has no keyless issuer", ErrUnavailable)
	}
	if ok, why := s.Keyless.Usable(); !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	return s.Keyless, nil
}

func (s *Service) keylessCloud(cloud string) (KeylessCloud, error) {
	kc, ok := s.KeylessClouds[cloud]
	if !ok && len(s.KeylessClouds) == 0 {
		return nil, fmt.Errorf("%w: Loomplane connects no cloud keylessly", ErrInvalid)
	}
	if !ok {
		return nil, fmt.Errorf("%w: Loomplane connects %s keylessly, not %q", ErrInvalid, strings.Join(slices.Sorted(maps.Keys(s.KeylessClouds)), ", "), cloud)
	}
	if _, err := s.checker(cloud); err != nil {
		return nil, err
	}
	return kc, nil
}

// keylessSettings checks settings for cloud's keyless trust.
func keylessSettings(kc KeylessCloud, settings map[string]string) (map[string]string, error) {
	out, err := kc.KeylessSettings(settings)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return out, nil
}

// setup describes c's keyless trust. Google's audience names the workload
// provider you make during the setup, so until a Google connection is
// keyless there's none to show.
func (s *Service) setup(iss KeylessIssuer, kc KeylessCloud, c *Connection) *KeylessSetup {
	subject := keyless.Subject(c.OrgID, c.ID)
	out := &KeylessSetup{ConnectionID: c.ID, Issuer: iss.URL(), Subject: subject, Steps: kc.KeylessSetup(iss.URL(), subject)}
	if c.Cloud != GCP || c.Method == MethodKeyless {
		out.Audience = kc.KeylessAudience(c.Details)
	}
	return out
}

// PrepareKeyless reserves a new connection's ID for a day and says how to
// set up cloud's trust in it.
func (s *Service) PrepareKeyless(ctx context.Context, p security.Principal, cloud string) (*KeylessSetup, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	iss, err := s.keylessIssuer()
	if err != nil {
		return nil, err
	}
	cloud = strings.TrimSpace(cloud)
	kc, err := s.keylessCloud(cloud)
	if err != nil {
		return nil, err
	}
	id, err := newConnectionID()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	r := &Reservation{ID: id, OrgID: p.OrgID, UserID: p.UserID, Cloud: cloud, ExpiresAt: now.Add(reservationTTL), CreatedAt: now}
	if err := s.Apps.PutReservation(ctx, r); err != nil {
		return nil, err
	}
	out := s.setup(iss, kc, &Connection{ID: id, OrgID: p.OrgID, Cloud: cloud})
	out.ExpiresAt = &r.ExpiresAt
	return out, nil
}

// KeylessSetupFor says how to set up the cloud's trust in an existing
// connection, so it can move to keyless. Its ID is its own, so nothing is
// reserved.
func (s *Service) KeylessSetupFor(ctx context.Context, p security.Principal, connectionID string) (*KeylessSetup, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	iss, err := s.keylessIssuer()
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, connectionID)
	if err != nil {
		return nil, err
	}
	kc, err := s.keylessCloud(c.Cloud)
	if err != nil {
		return nil, err
	}
	return s.setup(iss, kc, c), nil
}

// CreateKeyless checks the cloud's trust in the reserved ID and, if the
// cloud accepts it, stores the connection. Vault holds nothing for it. A
// reservation works once, in the organization that made it, and a create
// that fails leaves it for another try.
func (s *Service) CreateKeyless(ctx context.Context, p security.Principal, in KeylessInput) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	if _, err := s.keylessIssuer(); err != nil {
		return nil, err
	}
	name, err := validName(in.Name)
	if err != nil {
		return nil, err
	}
	unknown := fmt.Errorf("%w: this keyless setup is unknown or already used; start again", ErrInvalid)
	id := strings.TrimSpace(in.ConnectionID)
	if id == "" {
		return nil, unknown
	}
	// Another organization's reservation isn't found, so it stays theirs
	// and nothing says it exists.
	r, err := s.Apps.TakeReservation(ctx, p.OrgID, id)
	if errors.Is(err, ErrNotFound) {
		return nil, unknown
	}
	if err != nil {
		return nil, err
	}
	if !s.now().Before(r.ExpiresAt) {
		return nil, fmt.Errorf("%w: this keyless setup expired; start again", ErrInvalid)
	}
	c, err := s.createKeyless(ctx, p, r, name, in.Settings)
	if err != nil {
		s.keepReservation(ctx, r)
		return nil, err
	}
	return c, nil
}

func (s *Service) createKeyless(ctx context.Context, p security.Principal, r *Reservation, name string, settings map[string]string) (*Connection, error) {
	kc, err := s.keylessCloud(r.Cloud)
	if err != nil {
		return nil, err
	}
	if settings, err = keylessSettings(kc, settings); err != nil {
		return nil, err
	}
	handout, err := s.keylessHandout(ctx, &Connection{ID: r.ID, OrgID: p.OrgID, Cloud: r.Cloud, Details: settings})
	if err != nil {
		return nil, err
	}
	return s.create(ctx, p, r.ID, r.Cloud, name, nil, handout, settings, MethodKeyless)
}

// keepReservation puts back a reservation a create took but didn't use.
func (s *Service) keepReservation(ctx context.Context, r *Reservation) {
	if err := s.Apps.PutReservation(context.WithoutCancel(ctx), r); err != nil {
		s.warn("keep the reservation of a keyless connection that wasn't made", err)
	}
}

// ReplaceKeyless moves a connection to keyless, or changes a keyless
// connection's settings, if the cloud's trust reaches the same account.
// It keeps the connection's ID, and Vault no longer holds a secret for
// it. settings go over the connection's own, so an optional one it isn't
// given, such as a certificate, stays, and an empty one clears it.
func (s *Service) ReplaceKeyless(ctx context.Context, p security.Principal, id string, settings map[string]string) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	if _, err := s.keylessIssuer(); err != nil {
		return nil, err
	}
	c, err := s.store.GetConnection(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	kc, err := s.keylessCloud(c.Cloud)
	if err != nil {
		return nil, err
	}
	merged := maps.Clone(c.Details)
	if merged == nil {
		merged = map[string]string{}
	}
	maps.Copy(merged, settings)
	if settings, err = keylessSettings(kc, merged); err != nil {
		return nil, err
	}
	handout, err := s.keylessHandout(ctx, &Connection{ID: c.ID, OrgID: c.OrgID, Cloud: c.Cloud, Details: settings})
	if err != nil {
		return nil, err
	}
	if c, err = s.replace(ctx, p, id, nil, handout, settings, MethodKeyless); err != nil {
		return nil, err
	}
	s.dropSecret(ctx, c)
	return c, nil
}

// dropSecret removes the secret of c, which just went keyless. It holds
// the connection's lock and checks c is still keyless, so a credential
// pasted since stays. A failure leaves a secret nothing reads, so it's
// warned about.
func (s *Service) dropSecret(ctx context.Context, c *Connection) {
	ctx = context.WithoutCancel(ctx)
	drop := func() error {
		current, err := s.store.GetConnection(ctx, c.OrgID, c.ID)
		if err != nil {
			return err
		}
		if current.Method != MethodKeyless {
			return nil
		}
		return s.secrets.Delete(ctx, c.OrgID, c.ID)
	}
	var err error
	if s.grants != nil {
		err = s.grants.WithConnectionLock(ctx, c.OrgID, c.ID, drop)
	} else {
		err = drop()
	}
	if err != nil {
		s.warn("remove the secret of a connection that went keyless", err)
	}
}

// keylessHandout is what a cloud client gets for keyless c: who it is to
// the cloud, the audience the cloud expects, its settings and a ticket
// from the issuer. It's a secret.
func (s *Service) keylessHandout(ctx context.Context, c *Connection) ([]byte, error) {
	iss, err := s.keylessIssuer()
	if err != nil {
		return nil, err
	}
	kc, err := s.keylessCloud(c.Cloud)
	if err != nil {
		return nil, err
	}
	h := keyless.Handout{Type: keyless.Type, Subject: keyless.Subject(c.OrgID, c.ID), Audience: kc.KeylessAudience(c.Details), Settings: c.Details}
	if h.Ticket, err = iss.Ticket(ctx, h.Subject, h.Audience); err != nil {
		return nil, fmt.Errorf("%w: make a keyless ticket: %w", ErrUnavailable, err)
	}
	out, err := json.Marshal(h) // #nosec G117 -- the handout is the credential, bound for a check or a deployment, never a response.
	if err != nil {
		return nil, fmt.Errorf("encode keyless handout: %w", err)
	}
	return out, nil
}
