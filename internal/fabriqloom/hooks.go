package fabriqloom

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
)

// Console is the component and port Loomplane reaches Fabriq's admin API
// through: fabriq-admin's nginx, which adds the operator key.
const (
	consoleComponent = "admin"
	consolePort      = "http"
)

// Where consuming services reach the Fabriq server.
const (
	serverComponent = "server"
	serverPort      = "api"
)

// Channels opens channels to a composed resource's components for
// Loomplane's own background work. *deployments.CatalogAccess is the real
// one.
type Channels interface {
	InternalChannel(ctx context.Context, orgID string, ref deployments.ComponentRef) (*deployments.ComponentChannel, error)
}

var _ Channels = (*deployments.CatalogAccess)(nil)

// HookDeps are what the fabriq kind's hooks and its release observer use.
type HookDeps struct {
	Keys    KeyStore
	Secrets deployments.ResourceSecrets
	Access  Channels
	// Warn logs what failed without failing anything. It may be nil.
	Warn func(msg string, err error)
	// Now is the clock. Nil is time.Now.
	Now func() time.Time
}

func (d HookDeps) warn(msg string, err error) {
	if d.Warn != nil {
		d.Warn(msg, err)
	}
}

// Hooks are the fabriq kind's hooks. They issue each consuming service a
// Fabriq key of its own (deployments.ConsumerBinder), check the operator
// key once the console is up, and forget every key when the resource goes.
// Deleting the resource ends its admin proxy sessions elsewhere: the
// owned targets' CompositionRemoved.
type Hooks struct {
	deployments.NoHooks
	d   HookDeps
	now func() time.Time
}

var (
	_ deployments.CatalogHooks   = (*Hooks)(nil)
	_ deployments.ConsumerBinder = (*Hooks)(nil)
)

// NewHooks returns the fabriq kind's hooks.
func NewHooks(d HookDeps) *Hooks {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Hooks{d: d, now: now}
}

// Ready implements deployments.CatalogHooks. Once the console is up, the
// operator key it sends for Loomplane must open Fabriq's admin API. A
// server that isn't answering, or that speaks another admin API version,
// says so in its own words instead. With the key proven, it revokes every
// key Loomplane labelled that no live record of the resource names: keys a
// removed Fabriq left in a Postgres that outlived it, and keys a crash kept
// Loomplane from recording.
func (h *Hooks) Ready(ctx context.Context, run deployments.ComponentRun) error {
	if run.Component.Name != consoleComponent {
		return nil
	}
	ch, err := run.Channel(ctx, consoleComponent, consolePort)
	if err != nil {
		return fmt.Errorf("reach Fabriq's console: %w", err)
	}
	defer func() { _ = ch.Close() }() //nolint:errcheck // nothing to do about it
	admin := &AdminClient{Channel: ch}
	_, err = admin.Tenants(ctx)
	var status *StatusError
	if errors.As(err, &status) && !errors.Is(err, ErrUnreachable) && !errors.Is(err, ErrAPIVersion) {
		return fmt.Errorf("Fabriq refused Loomplane's operator key (HTTP %d)", status.Status) //nolint:staticcheck // ST1005: the step shows this sentence as written
	}
	if err != nil {
		return err
	}
	return h.sweep(ctx, admin, run.OrgID, run.EnvironmentID, run.Resource)
}

// sweep revokes, for every service, the keys labelled for it that no live
// record of the resource names, each service under its lock as a bind
// takes it.
func (h *Hooks) sweep(ctx context.Context, admin *AdminClient, orgID, environmentID, resource string) error {
	keys, err := admin.Keys(ctx)
	if err != nil {
		return fmt.Errorf("list Fabriq's keys: %w", err)
	}
	records, err := h.d.Keys.ForResource(ctx, orgID, environmentID, resource)
	if err != nil {
		return err
	}
	live := liveIDs(records)
	// Only services with a key no record names are locked; under the lock
	// revokeUnrecorded reads their records again.
	var services []string
	for _, k := range keys {
		service, ours := strings.CutPrefix(k.Label, keyLabelPrefix)
		if ours && k.RevokedAt == nil && !live[k.ID] && !slices.Contains(services, service) {
			services = append(services, service)
		}
	}
	slices.Sort(services)
	var errs []error
	for _, service := range services {
		errs = append(errs, h.sweepService(ctx, admin, keys, orgID, environmentID, resource, service))
	}
	return errors.Join(errs...)
}

func (h *Hooks) sweepService(ctx context.Context, admin *AdminClient, keys []KeyInfo, orgID, environmentID, resource, service string) error {
	ctx, unlock, err := h.d.Keys.LockService(ctx, orgID, environmentID, resource, service)
	if err != nil {
		return err
	}
	defer unlock()
	return h.revokeUnrecorded(ctx, admin, keys, orgID, environmentID, resource, service)
}

// BindConsumer implements deployments.ConsumerBinder. Each service gets
// one key, labelled loomplane:<service>, for as long as its default
// tenant stays the same and Vault still holds the key.
func (h *Hooks) BindConsumer(ctx context.Context, c deployments.Consumer) (string, error) {
	tenant := c.Options["tenant"]
	if tenant == "" {
		return "", fmt.Errorf("%s needs a default tenant: set services.%s.need_options.%s.tenant", c.Service, c.Service, c.Resource)
	}
	if !ValidTenant(tenant) {
		return "", fmt.Errorf("%s's default tenant %q isn't a tenant id: use lowercase letters, digits and dashes", c.Service, tenant)
	}
	ctx, unlock, err := h.d.Keys.LockService(ctx, c.OrgID, c.EnvironmentID, c.Resource, c.Service)
	if err != nil {
		return "", err
	}
	defer unlock()
	address, err := c.Address(serverComponent, serverPort)
	if err != nil {
		return "", err
	}
	ch, err := c.Channel(ctx, consoleComponent, consolePort)
	if err != nil {
		return "", fmt.Errorf("reach Fabriq's console: %w", err)
	}
	defer func() { _ = ch.Close() }() //nolint:errcheck // nothing to do about it
	admin := &AdminClient{Channel: ch}
	if _, _, err = admin.EnsureTenant(ctx, tenant); err != nil {
		return "", fmt.Errorf("create Fabriq tenant %s: %w", tenant, err)
	}
	if orphanErr := h.revokeOrphans(ctx, admin, c); orphanErr != nil {
		return "", orphanErr
	}
	key, err := h.current(ctx, admin, c, tenant)
	if err != nil {
		return "", err
	}
	if key == "" {
		if key, err = h.issue(ctx, admin, c, tenant); err != nil {
			return "", err
		}
	}
	return DSN(key, address.Host, tenant), nil
}

// current is the key the service already holds for tenant, promoting a
// pending one, or "" when it needs a new one. What it can't release any
// more it retires (an active key, which the running release may still
// hold) or revokes (a pending key, which no release holds).
func (h *Hooks) current(ctx context.Context, admin *AdminClient, c deployments.Consumer, tenant string) (string, error) {
	records, err := h.d.Keys.ForService(ctx, c.OrgID, c.EnvironmentID, c.Resource, c.Service)
	if err != nil {
		return "", err
	}
	active, pending := inState(records, KeyActive), inState(records, KeyPending)
	if pending != nil {
		key, kerr := h.kept(ctx, c, pending.ID)
		if kerr != nil {
			return "", kerr
		}
		if key != "" && pending.Tenant == tenant {
			if active != nil {
				if err = h.d.Keys.SetState(ctx, c.OrgID, active.ID, []KeyState{KeyActive}, KeyRetiring); err != nil {
					return "", fmt.Errorf("retire %s's Fabriq key: %w", c.Service, err)
				}
			}
			if err = h.d.Keys.SetState(ctx, c.OrgID, pending.ID, []KeyState{KeyPending}, KeyActive); err != nil {
				return "", fmt.Errorf("release %s's rotated Fabriq key: %w", c.Service, err)
			}
			return key, nil
		}
		if rerr := h.revoke(ctx, admin, c.OrgID, c.EnvironmentID, c.Resource, pending); rerr != nil {
			return "", rerr
		}
	}
	if active == nil {
		return "", nil
	}
	key, err := h.kept(ctx, c, active.ID)
	if err != nil || (key != "" && active.Tenant == tenant) {
		return key, err
	}
	if err := h.d.Keys.SetState(ctx, c.OrgID, active.ID, []KeyState{KeyActive}, KeyRetiring); err != nil {
		return "", fmt.Errorf("retire %s's Fabriq key: %w", c.Service, err)
	}
	return "", nil
}

// issue issues the service a new key for tenant and records it active.
// BindConsumer has already revoked the keys labelled for the service that
// no live record names.
func (h *Hooks) issue(ctx context.Context, admin *AdminClient, c deployments.Consumer, tenant string) (string, error) {
	issued, err := admin.IssueKey(ctx, KeyLabel(c.Service))
	if err != nil {
		return "", fmt.Errorf("issue %s's Fabriq key: %w", c.Service, err)
	}
	vault := VaultKey(c.OrgID, c.EnvironmentID, c.Resource, issued.ID)
	if err = h.d.Secrets.Put(ctx, vault, []byte(issued.Key)); err != nil {
		h.discard(ctx, admin, c, issued.ID)
		return "", fmt.Errorf("keep %s's Fabriq key: %w", c.Service, err)
	}
	now := h.now().UTC()
	err = h.d.Keys.Insert(ctx, &KeyRecord{ID: issued.ID, OrgID: c.OrgID, ProjectID: c.ProjectID, EnvironmentID: c.EnvironmentID, Resource: c.Resource,
		Service: c.Service, Tenant: tenant, Prefix: issued.Prefix, State: KeyActive, CreatedAt: now, UpdatedAt: now})
	if err == nil {
		return issued.Key, nil
	}
	h.discard(ctx, admin, c, issued.ID)
	if !errors.Is(err, ErrConflict) {
		return "", fmt.Errorf("record %s's Fabriq key: %w", c.Service, err)
	}
	// The service lock keeps a second writer out, so this is defensive: if
	// an active key was recorded anyway, use it.
	records, rerr := h.d.Keys.ForService(ctx, c.OrgID, c.EnvironmentID, c.Resource, c.Service)
	if rerr != nil {
		return "", rerr
	}
	if winner := inState(records, KeyActive); winner != nil && winner.Tenant == tenant {
		key, kerr := h.kept(ctx, c, winner.ID)
		if kerr != nil || key != "" {
			return key, kerr
		}
	}
	return "", fmt.Errorf("another release of %s issued its Fabriq key at the same time; deploy again", c.Service)
}

// revokeOrphans revokes the keys labelled for the service that no live
// record names, and forgets them. The service lock ensures a rotation cannot
// still be recording one while this reconciliation runs.
func (h *Hooks) revokeOrphans(ctx context.Context, admin *AdminClient, c deployments.Consumer) error {
	keys, err := admin.Keys(ctx)
	if err != nil {
		return &fabriqError{fmt.Errorf("list Fabriq's keys: %w", err)}
	}
	return h.revokeUnrecorded(ctx, admin, keys, c.OrgID, c.EnvironmentID, c.Resource, c.Service)
}

// revokeUnrecorded revokes the keys in keys labelled for service that no
// live record of it names, and forgets them. The caller holds the
// service's lock, so the records it reads are the last word.
func (h *Hooks) revokeUnrecorded(ctx context.Context, admin *AdminClient, keys []KeyInfo, orgID, environmentID, resource, service string) error {
	records, err := h.d.Keys.ForService(ctx, orgID, environmentID, resource, service)
	if err != nil {
		return err
	}
	live := liveIDs(records)
	label := KeyLabel(service)
	for _, k := range keys {
		if k.Label != label || k.RevokedAt != nil || live[k.ID] {
			continue
		}
		if err := admin.RevokeKey(ctx, k.ID); err != nil {
			return &fabriqError{fmt.Errorf("revoke an unrecorded Fabriq key of %s: %w", service, err)}
		}
		if err := h.d.Secrets.Delete(ctx, VaultKey(orgID, environmentID, resource, k.ID)); err != nil {
			return fmt.Errorf("forget an unrecorded Fabriq key of %s (%s): %w", service, k.Prefix, err)
		}
	}
	return nil
}

// fabriqError is an error from Fabriq's admin API or the channel to it,
// as opposed to one from the key store or Vault. The panel reports the
// first as Fabriq being unavailable and the second as what it is.
type fabriqError struct{ err error }

func (e *fabriqError) Error() string { return e.err.Error() }
func (e *fabriqError) Unwrap() error { return e.err }

// liveIDs are the ids of the records that aren't revoked.
func liveIDs(records []*KeyRecord) map[string]bool {
	live := map[string]bool{}
	for _, r := range records {
		if r.State != KeyRevoked {
			live[r.ID] = true
		}
	}
	return live
}

// discard revokes a key just issued that Loomplane won't record, and
// forgets it. What fails is left to the next release's orphan check.
func (h *Hooks) discard(ctx context.Context, admin *AdminClient, c deployments.Consumer, id string) {
	if err := admin.RevokeKey(ctx, id); err != nil {
		h.d.warn("revoke an unrecorded Fabriq key of "+c.Service, err)
		return
	}
	if err := h.d.Secrets.Delete(ctx, VaultKey(c.OrgID, c.EnvironmentID, c.Resource, id)); err != nil {
		h.d.warn("forget an unrecorded Fabriq key of "+c.Service, err)
	}
}

// kept is a key's plaintext from Vault, or "" when Vault doesn't hold it.
func (h *Hooks) kept(ctx context.Context, c deployments.Consumer, id string) (string, error) {
	value, err := h.d.Secrets.Get(ctx, VaultKey(c.OrgID, c.EnvironmentID, c.Resource, id))
	if errors.Is(err, deployments.ErrNoCredential) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s's Fabriq key: %w", c.Service, err)
	}
	return string(value), nil
}

// ReleaseConsumer implements deployments.ConsumerBinder: it revokes every
// key the service holds and forgets them, along with any key labelled for
// it that Loomplane never recorded (a bind that crashed between issuing
// and recording one). With nothing recorded, a Fabriq that can't be
// reached or fails the sweep is no error, only a warning: there is nothing
// Loomplane knows to be valid, and the next sweep that reaches Fabriq
// revokes what is left.
func (h *Hooks) ReleaseConsumer(ctx context.Context, c deployments.Consumer) error {
	ctx, unlock, err := h.d.Keys.LockService(ctx, c.OrgID, c.EnvironmentID, c.Resource, c.Service)
	if err != nil {
		return err
	}
	defer unlock()
	records, err := h.d.Keys.ForService(ctx, c.OrgID, c.EnvironmentID, c.Resource, c.Service)
	if err != nil {
		return err
	}
	var live []*KeyRecord
	for _, r := range records {
		if r.State != KeyRevoked {
			live = append(live, r)
		}
	}
	ch, err := c.Channel(ctx, consoleComponent, consolePort)
	if err != nil {
		if len(live) == 0 {
			h.d.warn("sweep the unrecorded Fabriq keys of "+c.Service, err)
			return nil
		}
		return fmt.Errorf("reach Fabriq's console: %w", err)
	}
	defer func() { _ = ch.Close() }() //nolint:errcheck // nothing to do about it
	admin := &AdminClient{Channel: ch}
	sweep := h.revokeOrphans(ctx, admin, c)
	var remote *fabriqError
	if len(live) == 0 && errors.As(sweep, &remote) {
		h.d.warn("sweep the unrecorded Fabriq keys of "+c.Service, sweep)
		return nil
	}
	errs := []error{sweep}
	for _, r := range live {
		errs = append(errs, h.revoke(ctx, admin, c.OrgID, c.EnvironmentID, c.Resource, r))
	}
	return errors.Join(errs...)
}

// revoke revokes a key in Fabriq, then records it revoked and forgets it.
// A key Fabriq still accepts is never recorded revoked.
func (h *Hooks) revoke(ctx context.Context, admin *AdminClient, orgID, environmentID, resource string, r *KeyRecord) error {
	if err := admin.RevokeKey(ctx, r.ID); err != nil {
		return fmt.Errorf("revoke %s's Fabriq key %s: %w", r.Service, r.Prefix, err)
	}
	return h.forget(ctx, orgID, environmentID, resource, r)
}

// forget records a key revoked and deletes it from Vault.
func (h *Hooks) forget(ctx context.Context, orgID, environmentID, resource string, r *KeyRecord) error {
	err := h.d.Keys.SetState(ctx, orgID, r.ID, []KeyState{KeyPending, KeyActive, KeyRetiring}, KeyRevoked)
	if err != nil && !errors.Is(err, ErrStateChanged) {
		return fmt.Errorf("record %s's Fabriq key %s revoked: %w", r.Service, r.Prefix, err)
	}
	return h.d.Secrets.Delete(ctx, VaultKey(orgID, environmentID, resource, r.ID))
}

// Removed implements deployments.CatalogHooks. It revokes every key the
// resource issued while Fabriq still answers, which it usually doesn't:
// Removed runs once the composition's components are gone. Either way,
// Loomplane records them revoked and forgets them. A Fabriq added again
// on a Postgres that outlived it revokes them when its console is Ready.
func (h *Hooks) Removed(ctx context.Context, res deployments.ComposedResource) error {
	records, err := h.d.Keys.ForResource(ctx, res.OrgID, res.EnvironmentID, res.Resource)
	if err != nil {
		return err
	}
	var live []*KeyRecord
	for _, r := range records {
		if r.State != KeyRevoked {
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return nil
	}
	var admin *AdminClient
	ref := deployments.ComponentRef{ResourceRef: deployments.ResourceRef{ProjectID: res.ProjectID, EnvironmentID: res.EnvironmentID, Resource: res.Resource}, Component: consoleComponent, Port: consolePort}
	if ch, cerr := h.d.Access.InternalChannel(ctx, res.OrgID, ref); cerr == nil {
		defer func() { _ = ch.Close() }() //nolint:errcheck // nothing to do about it
		admin = &AdminClient{Channel: ch}
	}
	var errs []error
	for _, r := range live {
		if admin != nil {
			if rerr := admin.RevokeKey(ctx, r.ID); rerr != nil {
				// The server is going with the resource.
				admin = nil
			}
		}
		errs = append(errs, h.forget(ctx, res.OrgID, res.EnvironmentID, res.Resource, r))
	}
	return errors.Join(errs...)
}

// inState is the first record in state, or nil.
func inState(records []*KeyRecord, state KeyState) *KeyRecord {
	for _, r := range records {
		if r.State == state {
			return r
		}
	}
	return nil
}
